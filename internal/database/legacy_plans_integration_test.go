//go:build integration

package database

// F08：升级前已落投递计划、但事件仍停在 generate 的旧数据。
//
// 恢复纪律：领取时按当前尝试状态把路由换到 deliver/verify，进入 worker 前
// 已完成；旧计划事件不得再经过 Generate（模型/工具零额外调用）。
// 仍持有有效租约的运行中事件不在领取范围内，阶段与状态都不能被改写。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// legacyGenerateStage 把已落计划的事件改回升级前形态：stage=generate。
func legacyGenerateStage(t *testing.T, db *gorm.DB, eventID string) {
	t.Helper()
	if err := db.Exec("UPDATE events SET stage=? WHERE id=?", event.StageGenerate, eventID).Error; err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerLegacyPlansResumeWithoutGeneration(t *testing.T) {
	db, ctx := repositoryDB(t)
	bus := makeBus(t, db)
	repo, err := NewSchedulerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := NewPlannedRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)

	// seed 造一条「计划已落库、阶段被留在 generate」的旧事件。
	seed := func(id, session string, at time.Time, state string) (event.Claim, sender.Attempt) {
		t.Helper()
		c := planFixture(t, db, ctx, id, event.ChComment, session, false)
		key := planKey(event.ChComment, session)
		attempt, err := planned.PreparePlan(ctx, c, key, "旧计划正文:"+id)
		if err != nil {
			t.Fatalf("落旧计划失败：%v", err)
		}
		switch state {
		case sender.StateReady:
		case sender.StateArmed, sender.StateUnknown:
			attempt, err = planned.ArmPlan(ctx, c, key, attempt.ID)
			if err != nil {
				t.Fatalf("旧计划换 armed 失败：%v", err)
			}
			if state == sender.StateUnknown {
				attempt, err = planned.RecordPlan(ctx, c, key, attempt.ID, store.PlanOutcome{State: sender.StateUnknown, ErrorKind: "transport"})
				if err != nil {
					t.Fatalf("旧计划置 unknown 失败：%v", err)
				}
			}
		default:
			t.Fatalf("未支持的旧尝试状态 %s", state)
		}
		if attempt.State != state {
			t.Fatalf("旧尝试状态=%s，期望 %s", attempt.State, state)
		}
		legacyGenerateStage(t, db, id)
		return c, attempt
	}

	readyID := "comment:video:legacy-ready"
	armedID := "comment:video:legacy-armed"
	unknownID := "comment:video:legacy-unknown"
	activeID := "comment:video:legacy-active"
	seed(readyID, "20001", now, sender.StateReady)
	seed(armedID, "20002", now.Add(time.Second), sender.StateArmed)
	seed(unknownID, "20003", now.Add(2*time.Second), sender.StateUnknown)
	// 活跃租约：与旧数据同形，但任务仍在运行，恢复不能改写它。
	seed(activeID, "20004", now.Add(3*time.Second), sender.StateReady)
	// 三条待恢复事件模拟进程崩溃：租约过期后才允许被领取。
	for _, id := range []string{readyID, armedID, unknownID} {
		expireClaim(t, db, id)
	}

	opts := scheduler.DefaultOptions()
	opts.MaxConcurrent = 2
	opts.MaxPending = 10
	opts.ClaimBatch = 10
	opts.PollInterval = 5 * time.Millisecond
	opts.RetryDelay = retryDelayFromDBClock(t, db)
	opts.OperationTimeout = 2 * time.Second
	opts.Channels[event.ChComment] = scheduler.ChannelPolicy{Public: true, Brain: "openai", RatePerMinute: 6000, Burst: 10, DailyQuota: 100, MinRunes: 2}

	var generateCalls atomic.Int32
	delivered := make(chan event.Claim, 1)
	verified := make(chan event.Claim, 2)
	generate := func(c context.Context, w scheduler.Work) (scheduler.Result, error) {
		generateCalls.Add(1)
		// 真的误入生成也要结清，测试末尾再按次数判失败。
		for _, edge := range []struct{ from, to event.Status }{{event.StatusQueued, event.StatusReplying}, {event.StatusReplying, event.StatusSent}, {event.StatusSent, event.StatusDone}} {
			if err := bus.Mark(c, w.Claim, []event.Status{edge.from}, edge.to); err != nil {
				return scheduler.Result{}, err
			}
		}
		return scheduler.Result{Replied: true}, nil
	}
	deliver := func(c context.Context, w scheduler.Work) (scheduler.Result, error) {
		if w.Claim.Stage != event.StageDeliver {
			return scheduler.Result{}, errors.New("ready 旧计划恢复进入了错误阶段")
		}
		key := planKey(event.ChComment, w.Claim.Event.Session)
		attempt, err := planned.LoadPlan(c, w.Claim, key)
		if err != nil {
			return scheduler.Result{}, err
		}
		if attempt.State != sender.StateReady {
			return scheduler.Result{}, errors.New("ready 旧计划的当前尝试状态不符")
		}
		// 与真实 sender 同序：先 arm 再记录平台回执；正文来自计划快照。
		armed, err := planned.ArmPlan(c, w.Claim, key, attempt.ID)
		if err != nil {
			return scheduler.Result{}, err
		}
		if _, err := planned.RecordPlan(c, w.Claim, key, armed.ID, store.PlanOutcome{State: sender.StateAccepted, OK: true, Verified: true, MsgID: "rpid:legacy"}); err != nil {
			return scheduler.Result{}, err
		}
		delivered <- w.Claim
		return scheduler.Result{Replied: true}, nil
	}
	verify := func(c context.Context, w scheduler.Work) (scheduler.Result, error) {
		if w.Claim.Stage != event.StageVerify {
			return scheduler.Result{}, errors.New("armed/unknown 旧计划恢复进入了错误阶段")
		}
		key := planKey(event.ChComment, w.Claim.Event.Session)
		attempt, err := planned.LoadPlan(c, w.Claim, key)
		if err != nil {
			return scheduler.Result{}, err
		}
		if attempt.State != sender.StateArmed && attempt.State != sender.StateUnknown {
			return scheduler.Result{}, errors.New("armed/unknown 旧计划的当前尝试状态不符")
		}
		if err := bus.Mark(c, w.Claim, []event.Status{event.StatusQueued}, event.StatusDone); err != nil {
			return scheduler.Result{}, err
		}
		verified <- w.Claim
		return scheduler.Result{}, nil
	}
	runner, err := scheduler.New(bus, repo, opts, scheduler.Workers{Generate: generate, Deliver: deliver, Verify: verify}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- runner.Run(runCtx) }()

	seenDeliver := map[string]bool{}
	seenVerify := map[string]bool{}
	for len(seenDeliver)+len(seenVerify) < 3 {
		select {
		case c := <-delivered:
			if c.Event.ID != readyID {
				t.Fatalf("非 ready 旧计划走了 deliver：%s", c.Event.ID)
			}
			seenDeliver[c.Event.ID] = true
		case c := <-verified:
			if c.Event.ID != armedID && c.Event.ID != unknownID {
				t.Fatalf("非 armed/unknown 旧计划走了 verify：%s", c.Event.ID)
			}
			seenVerify[c.Event.ID] = true
		case err := <-finished:
			t.Fatalf("调度器提前退出：%v", err)
		case <-runCtx.Done():
			t.Fatal("旧计划恢复超时")
		}
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("调度器未退出")
	}

	// 旧计划恢复全程不得进入 Generate：模型/工具零额外调用。
	if got := generateCalls.Load(); got != 0 {
		t.Fatalf("旧计划恢复了%d次生成", got)
	}
	if !seenDeliver[readyID] || !seenVerify[armedID] || !seenVerify[unknownID] {
		t.Fatalf("恢复路由不完整：deliver=%v verify=%v", seenDeliver, seenVerify)
	}
	for _, id := range []string{readyID, armedID, unknownID} {
		state, err := repo.Snapshot(ctx, id)
		if err != nil || state.Event.Status != event.StatusDone || state.Lease.Owner != "" {
			t.Fatalf("旧计划 %s 未结清：%v", id, err)
		}
	}
	// 有效租约在途：阶段与持有者保持原样。
	active, err := repo.Snapshot(ctx, activeID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Stage != event.StageGenerate || active.Event.Status != event.StatusReplying || active.Lease.Owner == "" {
		t.Fatalf("运行中的旧计划被改写：stage=%s status=%s owner=%q", active.Stage, active.Event.Status, active.Lease.Owner)
	}
}
