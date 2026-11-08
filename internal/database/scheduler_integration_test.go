//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// retryDelayFromDBClock 以数据库时钟为基准派生重试延迟：
// 量一次 clock_timestamp() 的往返与漂移，再留出余量。
// 由于 deferClaim 用 Go 时钟加 RetryDelay，而库层用数据库时钟校验未来，
// 只有把往返与正向漂移都计入延迟，Docker/远程 PG 下才不会判成过去时间。
func retryDelayFromDBClock(t *testing.T, db *gorm.DB) time.Duration {
	t.Helper()
	start := time.Now()
	var row struct {
		Now time.Time `gorm:"column:db_now"`
	}
	if err := db.Raw("SELECT clock_timestamp() AS db_now").Scan(&row).Error; err != nil {
		t.Fatalf("读取数据库时钟失败：%v", err)
	}
	rtt := time.Since(start)
	delay := 2*rtt + 500*time.Millisecond
	if skew := row.Now.Sub(start.Add(rtt / 2)); skew > 0 {
		delay += skew
	}
	return delay
}

func TestSchedulerAdmissionQuotaAtomicAndRestart(t *testing.T) {
	db, ctx := repositoryDB(t)
	bus := makeBus(t, db)
	repo, err := NewSchedulerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	var _ store.SchedulerRepo = repo
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	quota := store.ScheduleQuota{Limit: 1, From: from, Until: from.AddDate(0, 0, 1)}
	a := publishQueueEvent(t, bus, ctx, "live:room:quota-a")
	b := a
	b.ID = "live:other:quota-b"
	b.Session = "other"
	if _, err := bus.Publish(ctx, b); err != nil {
		t.Fatal(err)
	}
	claims, err := bus.ClaimPending(ctx, 2)
	if err != nil || len(claims) != 2 {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan store.ScheduleAdmission, 2)
	errs := make(chan error, 2)
	for _, c := range claims {
		wg.Go(func() {
			task := store.Task{ID: "task-" + c.Event.ID, EventID: c.Event.ID, Channel: c.Event.Channel, Session: c.Event.Session, Brain: "openai", Priority: 2, CreatedAt: now}
			out, err := repo.Admit(ctx, c, task, quota)
			results <- out
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	admitted, limited := 0, 0
	for result := range results {
		if result.Inserted {
			admitted++
		}
		if result.WaitUntil != nil {
			limited++
		}
	}
	if admitted != 1 || limited != 1 {
		t.Fatalf("并发配额越界: %d/%d", admitted, limited)
	}
	restarted, err := NewSchedulerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claims {
		if _, err := restarted.Task(ctx, c.Event.ID); errors.Is(err, store.ErrNotFound) {
			task := store.Task{ID: "restart", EventID: c.Event.ID, Channel: c.Event.Channel, Session: c.Event.Session, Brain: "openai", Priority: 2, CreatedAt: now}
			out, err := restarted.Admit(ctx, c, task, quota)
			if err != nil || out.WaitUntil == nil || !out.WaitUntil.Equal(quota.Until) {
				t.Fatal("重启清空了每日配额", err)
			}
		}
	}
}

func TestSchedulerEarlierEventAndLeaseFencing(t *testing.T) {
	db, ctx := repositoryDB(t)
	bus := makeBus(t, db)
	repo, _ := NewSchedulerRepo(db)
	publishQueueEvent(t, bus, ctx, "live:room:earlier")
	publishQueueEvent(t, bus, ctx, "live:room:later")
	claims, err := bus.ClaimPending(ctx, 2)
	if err != nil || len(claims) != 2 {
		t.Fatal(err)
	}
	if wait, err := repo.Earlier(ctx, claims[1]); err != nil || wait == nil {
		t.Fatal("同会话较早事件未阻止后来的高优先级", err)
	}
	if err := bus.Mark(ctx, claims[0], []event.Status{event.StatusQueued}, event.StatusSkipped); err != nil {
		t.Fatal(err)
	}
	if wait, err := repo.Earlier(ctx, claims[1]); err != nil || wait != nil {
		t.Fatal("已跳过事件仍阻塞", err)
	}
	expireClaim(t, db, claims[1].Event.ID)
	if _, err := repo.Earlier(ctx, claims[1]); !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("旧租约绕过查询前置条件")
	}
}

func TestSchedulerAdmissionRollbackAndExistingTask(t *testing.T) {
	db, ctx := repositoryDB(t)
	bus := makeBus(t, db)
	repo, _ := NewSchedulerRepo(db)
	now := time.Now()
	from := now.Truncate(24 * time.Hour)
	q := store.ScheduleQuota{Limit: 2, From: from, Until: from.Add(24 * time.Hour)}
	for _, ev := range []event.Event{testEvent("live:a:1", event.ChLive, "a", false, now), testEvent("live:b:1", event.ChLive, "b", false, now)} {
		if _, err := bus.Publish(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	claims, err := bus.ClaimPending(ctx, 2)
	if err != nil || len(claims) != 2 {
		t.Fatal(err)
	}
	makeTask := func(c event.Claim, id string) store.Task {
		return store.Task{ID: id, EventID: c.Event.ID, Channel: c.Event.Channel, Session: c.Event.Session, Brain: "openai", Priority: 2, CreatedAt: now}
	}
	if _, err := repo.Admit(ctx, claims[0], makeTask(claims[0], "collision"), q); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Admit(ctx, claims[1], makeTask(claims[1], "collision"), q); !errors.Is(err, store.ErrConflict) {
		t.Fatal("冲突未拒绝", err)
	}
	state, err := repo.Snapshot(ctx, claims[1].Event.ID)
	if err != nil || state.Stage != event.StageSchedule {
		t.Fatal("配额事务留下阶段半成品", err)
	}
	if _, err := repo.Task(ctx, claims[1].Event.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("配额事务留下Task")
	}
	if result, err := repo.Admit(ctx, claims[1], makeTask(claims[1], "second"), q); err != nil || !result.Inserted {
		t.Fatal("失败事务消耗了配额", err)
	}
	if result, err := repo.Admit(ctx, claims[0], makeTask(claims[0], "unused"), q); err != nil || result.Inserted || result.WaitUntil != nil {
		t.Fatal("已有Task重复扣配额", err)
	}
	bad := q
	bad.Limit = 0
	if _, err := repo.Admit(ctx, claims[0], makeTask(claims[0], "bad"), bad); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("无效配额未拒绝")
	}
	if _, err := NewSchedulerRepo(nil); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受nil连接")
	}
}

func TestSchedulerRunFairWhenLiveBacklog(t *testing.T) {
	db, ctx := repositoryDB(t)
	bus := makeBus(t, db)
	repo, _ := NewSchedulerRepo(db)
	now := time.Now().Add(-time.Hour)
	for i := range 20 {
		ev := testEvent(fmt.Sprintf("live:room:%02d", i), event.ChLive, fmt.Sprintf("room%02d", i), false, now.Add(time.Duration(i)*time.Second))
		if _, err := bus.Publish(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	dm := testEvent("dm:user:fair", event.ChDM, "user", true, now.Add(30*time.Minute))
	if _, err := bus.Publish(ctx, dm); err != nil {
		t.Fatal(err)
	}

	opts := scheduler.DefaultOptions()
	opts.MaxConcurrent = 1
	opts.MaxPending = 8
	opts.ClaimBatch = 4
	opts.PollInterval = 5 * time.Millisecond
	opts.RetryDelay = 5 * time.Millisecond
	opts.OperationTimeout = 2 * time.Second
	live := opts.Channels[event.ChLive]
	live.RatePerMinute = 6000
	live.Burst = 10
	live.Cooldown = 0
	opts.Channels[event.ChLive] = live
	opts.Channels[event.ChDM] = scheduler.ChannelPolicy{Private: true, Brain: "openai", RatePerMinute: 6000, Burst: 10, DailyQuota: 100, MinRunes: 2}

	generated := make(chan string, 32)
	worker := func(c context.Context, w scheduler.Work) (scheduler.Result, error) {
		if w.Task == nil || w.Claim.Stage != event.StageGenerate {
			return scheduler.Result{}, errors.New("生成分支缺任务")
		}
		for _, edge := range []struct{ from, to event.Status }{{event.StatusQueued, event.StatusReplying}, {event.StatusReplying, event.StatusSent}, {event.StatusSent, event.StatusDone}} {
			if err := bus.Mark(c, w.Claim, []event.Status{edge.from}, edge.to); err != nil {
				return scheduler.Result{}, err
			}
		}
		generated <- w.Claim.Event.ID
		return scheduler.Result{Replied: true}, nil
	}
	verify := func(c context.Context, w scheduler.Work) (scheduler.Result, error) {
		return scheduler.Result{}, bus.Mark(c, w.Claim, []event.Status{event.StatusQueued}, event.StatusDone)
	}
	runner, err := scheduler.New(bus, repo, opts, scheduler.Workers{Generate: worker, Verify: verify}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- runner.Run(runCtx) }()

	dmPos := -1
	var seen []string
	for i := 0; i < 3 && dmPos < 0; i++ {
		select {
		case id := <-generated:
			seen = append(seen, id)
			if id == dm.ID {
				dmPos = i
			}
		case err := <-finished:
			t.Fatal("调度器提前退出", err)
		case <-runCtx.Done():
			t.Fatal("等待 DM 超时")
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
	if dmPos < 0 {
		t.Fatalf("live 积压时 DM 未进入前 3 个调度：%v", seen)
	}
	state, err := repo.Snapshot(ctx, dm.ID)
	if err != nil || state.Event.Status != event.StatusDone || state.Lease.Owner != "" {
		t.Fatal("DM 未结清", err)
	}
}

func TestSchedulerRunRealPostgresWithoutModel(t *testing.T) {
	db, ctx := repositoryDB(t)
	bus := makeBus(t, db)
	repo, _ := NewSchedulerRepo(db)
	now := time.Now().Add(-time.Hour)
	first := testEvent("live:room:a", event.ChLive, "room", false, now)
	first.Text = "先到的普通消息"
	later := testEvent("live:room:z", event.ChLive, "room", false, now.Add(time.Second))
	later.Text = "后到的主人指令"
	later.FromID = "owner"
	other := testEvent("live:other:q", event.ChLive, "other", false, now.Add(2*time.Second))
	other.Text = "另一会话"
	other.FromID = "owner"
	for _, ev := range []event.Event{first, later, other} {
		if _, err := bus.Publish(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	opts := scheduler.DefaultOptions()
	opts.MaxConcurrent = 2
	opts.PollInterval = 5 * time.Millisecond
	opts.RetryDelay = retryDelayFromDBClock(t, db)
	opts.OperationTimeout = 2 * time.Second
	p := opts.Channels[event.ChLive]
	p.OwnerIDs = []string{"owner"}
	p.RatePerMinute = 6000
	p.Burst = 10
	p.Cooldown = 20 * time.Millisecond
	opts.Channels[event.ChLive] = p
	generated := make(chan string, 3)
	worker := func(c context.Context, w scheduler.Work) (scheduler.Result, error) {
		if w.Task == nil || w.Claim.Stage != event.StageGenerate {
			return scheduler.Result{}, errors.New("生成分支缺任务")
		}
		for _, edge := range []struct{ from, to event.Status }{{event.StatusQueued, event.StatusReplying}, {event.StatusReplying, event.StatusSent}, {event.StatusSent, event.StatusDone}} {
			if err := bus.Mark(c, w.Claim, []event.Status{edge.from}, edge.to); err != nil {
				return scheduler.Result{}, err
			}
		}
		generated <- w.Claim.Event.ID
		return scheduler.Result{Replied: true}, nil
	}
	verify := func(c context.Context, w scheduler.Work) (scheduler.Result, error) {
		return scheduler.Result{}, bus.Mark(c, w.Claim, []event.Status{event.StatusQueued}, event.StatusDone)
	}
	runner, err := scheduler.New(bus, repo, opts, scheduler.Workers{Generate: worker, Verify: verify}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- runner.Run(runCtx) }()
	var order []string
	for len(order) < 3 {
		select {
		case id := <-generated:
			order = append(order, id)
		case err := <-finished:
			t.Fatal("调度器提前退出", err)
		case <-runCtx.Done():
			t.Fatal("调度组合路径超时")
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
	positions := map[string]int{}
	for i, id := range order {
		positions[id] = i
	}
	if positions[first.ID] > positions[later.ID] {
		t.Fatal("主人高优先级颠倒同会话顺序")
	}
	for _, ev := range []event.Event{first, later, other} {
		state, err := repo.Snapshot(ctx, ev.ID)
		if err != nil || state.Event.Status != event.StatusDone || state.Lease.Owner != "" {
			t.Fatal("事件未结清", err)
		}
	}
	var count int64
	if err := db.Table("tasks").Count(&count).Error; err != nil || count != 3 {
		t.Fatal("调度重复/丢失Task", err)
	}
}
