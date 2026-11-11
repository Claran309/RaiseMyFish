//go:build integration

package main

// A6/N11：评论/DM 崩溃恢复必须按阶段直接 Resume，不重跑 Brain/ReAct。
//
// 用真实 PG + 真实 Bus/TaskRepo/PlannedRepo/SchedulerRepo + 真实 Scheduler
// 与 buildConsumeWorkers 组装完整链路，在 ready / armed / unknown 三个崩溃点
// 中断进程，断言恢复期间模型调用与副作用工具调用次数都不增加：
//   - ready：计划已落、未 POST，恢复按计划原文重投一次；
//   - armed：许可已发出、POST 在途，恢复只核验并按未知停止；
//   - unknown：结果已记录为未知，恢复同样只核验不重发。

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	busimpl "RaiseMyFish/internal/event/bus"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	sendercomment "RaiseMyFish/internal/sender/comment"
	"RaiseMyFish/internal/sender/durable"
	"RaiseMyFish/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const recoveryAccount = "10001"

// recoveryTestDB 建独立 schema、迁移并返回连接，避免与其他集成测试共享表。
func recoveryTestDB(t *testing.T) (*gorm.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("FISH_TEST_DSN")
	if dsn == "" {
		env, err := godotenv.Read(filepath.Join("..", "..", ".env"))
		if err != nil {
			t.Fatal("集成测试需要 FISH_TEST_DSN 或本地 .env")
		}
		dsn = env["FISH_DB_DSN"]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	admin, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("fish_a6_%x", suffix)
	quoted := pgx.Identifier{schema}.Sanitize()
	if err := admin.WithContext(ctx).Exec("CREATE SCHEMA " + quoted).Error; err != nil {
		t.Fatal("创建测试schema失败")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		if err := admin.WithContext(cleanup).Exec("DROP SCHEMA " + quoted + " CASCADE").Error; err != nil {
			t.Error("清理测试schema失败")
		}
		if err := database.Close(admin); err != nil {
			t.Error(err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("DSN格式错误")
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema + ",public"
	}
	db, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return db, ctx
}

// recoveryPost 是平台发送替身：只数真实 POST，不参与 Resume。
type recoveryPost struct {
	sends atomic.Int32
}

func (p *recoveryPost) Name() string { return "recovery-post" }

func (p *recoveryPost) Send(_ context.Context, _ event.Claim, _ sender.Reply) (sender.Result, error) {
	p.sends.Add(1)
	return sender.Result{OK: true, MsgID: "receipt", Verified: true}, nil
}

func (p *recoveryPost) Resume(context.Context, event.Claim) (sender.Result, error) {
	return sender.Result{}, errors.New("不应调用底层 Resume")
}

// recoveryBrain 记录单步 Brain 的调用次数；恢复路径必须为 0。
type recoveryBrain struct{ calls atomic.Int32 }

func (*recoveryBrain) Name() string { return "recovery-brain" }

func (b *recoveryBrain) Reply(context.Context, brain.Task, brain.ContextInput) (brain.Reply, error) {
	b.calls.Add(1)
	return brain.Reply{Text: "模型重新生成"}, nil
}

// recoveryLLM 在 ReAct 提示模型调用时执行 follow 副作用并记录次数。
type recoveryLLM struct{ calls atomic.Int32 }

func (l *recoveryLLM) Complete(context.Context, string, string) (string, error) {
	l.calls.Add(1)
	return `{"thought":"关注一下","action":"follow","args":"{\"mid\":\"123\",\"action\":\"follow\"}"}`, nil
}

// recoveryReact 组装带副作用工具的 ReAct 运行时，并分别计数工厂/模型/工具调用。
type recoveryReact struct {
	factories atomic.Int32
	llm       *recoveryLLM
	tools     atomic.Int32
	box       *reasoning.Toolbox
}

func newRecoveryReact(t *testing.T) *recoveryReact {
	t.Helper()
	r := &recoveryReact{llm: &recoveryLLM{}}
	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: reasoning.ToolFollow,
		Desc: "测试副作用工具",
		Run: func(context.Context, string) (string, error) {
			r.tools.Add(1)
			return "已关注", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.box = box
	return r
}

func (r *recoveryReact) runtime() *reactRuntime {
	factory := func(memory.Scope, string, bool, event.Channel) (*reasoning.Agent, error) {
		r.factories.Add(1)
		return reasoning.New(r.llm, r.box, reasoning.Options{MaxSteps: 2, Timeout: time.Second}, zap.NewNop())
	}
	return &reactRuntime{factory: factory, defaultReact: true}
}

// recoveryPack 给评论/DM 两类场景都配好正文：人格包缺场景会让 Prepare
// 直接拒绝，ReAct 根本没机会创建工厂，副作用计数就成了死代码。
func recoveryPack() persona.Pack {
	pack := snapshotTestPack()
	pack.Contexts[persona.ContextComment] = "你在评论区里回复观众"
	pack.Contexts[persona.ContextDM] = "你在私信里和观众聊天"
	return pack
}

// recoveryClaim 造一条已进入 Generate 的事件并领取（status 由调用方推进）。
func recoveryClaim(t *testing.T, db *gorm.DB, ctx context.Context, bus *busimpl.Bus, id string, ch event.Channel, session string) event.Claim {
	t.Helper()
	ev := event.Event{
		ID: id, Channel: ch, Dir: event.DirIn, Session: session, FromID: "20002",
		Text: "崩溃恢复测试", At: time.Now().UTC().Truncate(time.Microsecond), Status: event.StatusPending,
	}
	if ch == event.ChDM {
		ev.Private = true
	}
	if _, err := bus.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}
	claims, err := bus.ClaimPending(ctx, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("领取事件失败：%d %v", len(claims), err)
	}
	c := claims[0]
	tasks, err := database.NewTaskRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.CreateClaimed(ctx, c, store.Task{
		ID: "task:" + id, EventID: id, Channel: ch, Session: session, Brain: "test", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Mark(ctx, c, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
		t.Fatal(err)
	}
	return c
}

func recoveryRow(t *testing.T, db *gorm.DB, id string) (event.Status, event.Stage, *string) {
	t.Helper()
	var row struct {
		Status event.Status
		Stage  event.Stage
		Owner  *string `gorm:"column:lease_owner"`
	}
	if err := db.Table("events").Select("status,stage,lease_owner").Where("id=?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.Status, row.Stage, row.Owner
}

// TestRecoveryResumesWithoutRegenerating 用真实调度器验证三个崩溃点都不再进入生成。
func TestRecoveryResumesWithoutRegenerating(t *testing.T) {
	cases := []struct {
		name     string
		ch       event.Channel
		id       string
		session  string
		crashAt  string
		wantPost int32
		want     event.Status
		attempt  string
	}{
		{"comment_ready", event.ChComment, "comment:555:901", "555", "ready", 1, event.StatusDone, sender.StateAccepted},
		{"dm_armed", event.ChDM, "dm:10001:20002:901", "10001:20002", "armed", 0, event.StatusDead, sender.StateArmed},
		{"comment_unknown", event.ChComment, "comment:777:901", "777", "unknown", 0, event.StatusDead, sender.StateUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, ctx := recoveryTestDB(t)
			eventRepo, err := database.NewEventRepo(db)
			if err != nil {
				t.Fatal(err)
			}
			bus, err := busimpl.New(eventRepo, 0)
			if err != nil {
				t.Fatal(err)
			}
			c := recoveryClaim(t, db, ctx, bus, tc.id, tc.ch, tc.session)

			repo, err := database.NewPlannedRepo(db)
			if err != nil {
				t.Fatal(err)
			}
			key := store.PlanKey{Channel: tc.ch, AccountID: recoveryAccount, Destination: tc.session}
			a, err := repo.PreparePlan(ctx, c, key, "计划原文")
			if err != nil {
				t.Fatal(err)
			}
			switch tc.crashAt {
			case "ready":
			case "armed", "unknown":
				if _, err := repo.ArmPlan(ctx, c, key, a.ID); err != nil {
					t.Fatal(err)
				}
				if tc.crashAt == "unknown" {
					if _, err := repo.RecordPlan(ctx, c, key, a.ID, store.PlanOutcome{State: sender.StateUnknown, ErrorKind: "unknown_manual"}); err != nil {
						t.Fatal(err)
					}
				}
			default:
				t.Fatalf("未知崩溃点 %s", tc.crashAt)
			}

			// 模拟进程崩溃：租约到期，调度器下次循环才能接管。
			if err := db.Exec("UPDATE events SET lease_until=clock_timestamp()-interval '1 second' WHERE id=?", tc.id).Error; err != nil {
				t.Fatal(err)
			}

			post := &recoveryPost{}
			snd, err := durable.New(repo, post, durable.Options{
				Channel: tc.ch, AccountID: recoveryAccount, Validate: sendercomment.Normalize, MaxAttempts: 3,
			}, zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			engine := &recoveryBrain{}
			react := newRecoveryReact(t)
			workers, err := buildConsumeWorkers(
				scheduler.ChannelPolicy{}, recoveryPack(),
				map[event.Channel]brain.Brain{tc.ch: engine},
				snd, bus, time.Second,
				nil, nil, nil, "", nil, nil,
				react.runtime(), nil, zap.NewNop(),
			)
			if err != nil {
				t.Fatal(err)
			}

			options := scheduler.DefaultOptions()
			options.MaxConcurrent = 2
			options.MaxPending = 4
			options.ClaimBatch = 4
			options.PollInterval = 20 * time.Millisecond
			options.RetryDelay = 50 * time.Millisecond
			options.OperationTimeout = 3 * time.Second
			options.WorkerTimeout = 5 * time.Second
			options.Location = time.UTC
			options.Channels = map[event.Channel]scheduler.ChannelPolicy{
				tc.ch: {Public: tc.ch == event.ChComment, Private: tc.ch == event.ChDM, Brain: "test", RatePerMinute: 600, Burst: 10, DailyQuota: 100},
			}
			schedRepo, err := database.NewSchedulerRepo(db)
			if err != nil {
				t.Fatal(err)
			}
			sched, err := scheduler.New(bus, schedRepo, options, workers, zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			runCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- sched.Run(runCtx) }()

			deadline := time.Now().Add(10 * time.Second)
			var status event.Status
			var stage event.Stage
			for {
				status, stage, _ = recoveryRow(t, db, tc.id)
				if status == event.StatusDone || status == event.StatusDead {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("恢复未结清：status=%s stage=%s", status, stage)
				}
				time.Sleep(10 * time.Millisecond)
			}
			stop()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("调度器退出：%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("调度器未退出")
			}

			if status != tc.want || stage != event.StageVerify {
				t.Fatalf("结清状态=%s/%s want %s/verify", status, stage, tc.want)
			}
			// 关键断言：恢复没有重新生成，也没有执行副作用工具。
			if engine.calls.Load() != 0 || react.factories.Load() != 0 || react.llm.calls.Load() != 0 || react.tools.Load() != 0 {
				t.Fatalf("恢复重跑了生成：brain=%d factory=%d llm=%d tool=%d",
					engine.calls.Load(), react.factories.Load(), react.llm.calls.Load(), react.tools.Load())
			}
			if got := post.sends.Load(); got != tc.wantPost {
				t.Fatalf("恢复 POST 次数=%d want %d", got, tc.wantPost)
			}
			var attempt struct {
				State  string
				Number int
			}
			if err := db.Table("delivery_attempts").Select("state,number").Where("event_id=?", tc.id).Take(&attempt).Error; err != nil {
				t.Fatal(err)
			}
			if attempt.Number != 1 || attempt.State != tc.attempt {
				t.Fatalf("尝试=%+v want number=1 state=%s", attempt, tc.attempt)
			}
		})
	}
}
