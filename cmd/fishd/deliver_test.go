package main

// S06/T06：Resume 的预期业务失败只要已结清（deferred/dead）就返回正常结果，
// 不能让一条不确定的核验把调度循环带崩；结清失败才升级为错误。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// verifyClaim 造一条 verify 阶段的 live 事件 Claim。
func verifyClaim(id, session string) event.Claim {
	c := liveClaim(id)
	c.Event.Session = session
	c.Stage = event.StageVerify
	c.LeaseUntil = time.Now().Add(time.Hour)
	return c
}

// TestDeliverSettledBusinessFailureReturnsNormalResult 业务失败已结清时 handler 返回 nil。
func TestDeliverSettledBusinessFailureReturnsNormalResult(t *testing.T) {
	retryAt := time.Now().UTC().Add(time.Minute)
	cases := []struct {
		name      string
		err       error
		want      event.Status
		wantDefer bool
	}{
		{"uncertain", &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("核验未知")}, event.StatusDead, false},
		{"dead", &sender.Error{Kind: sender.KindDead, Cause: errors.New("投递已终止")}, event.StatusDead, false},
		{"retry", &sender.Error{Kind: sender.KindRetry, RetryAt: retryAt, Cause: errors.New("平台限流")}, event.StatusRetry, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "live:30308220:9001"
			bus := newFakeBus(id)
			handler := deliverHandler(resumeDeps{
				sender:     &fakeSender{resumeErr: tc.err},
				bus:        bus,
				retryDelay: time.Second,
				log:        zap.NewNop(),
			})
			res, err := handler(context.Background(), scheduler.Work{Claim: verifyClaim(id, "30308220")})
			if err != nil {
				t.Fatalf("业务失败已结清，handler 不应返回错误：%v", err)
			}
			if res.Replied {
				t.Errorf("失败结清不应标记 Replied：%+v", res)
			}
			if got := bus.finalStatus(id); got != tc.want {
				t.Errorf("最终状态 = %q，期望 %q", got, tc.want)
			}
			if _, ok := bus.defers[id]; ok != tc.wantDefer {
				t.Errorf("Defer 记录 = %v，期望 %v", ok, tc.wantDefer)
			}
			if tc.wantDefer {
				bus.mu.Lock()
				until := bus.defers[id]
				bus.mu.Unlock()
				if until.Before(retryAt) {
					t.Errorf("Defer 时间 = %v，应不早于 RetryAt %v", until, retryAt)
				}
			}
		})
	}
}

// TestDeliverSettlementFailureEscalates 结清失败必须升级为错误，不能当作已处理。
func TestDeliverSettlementFailureEscalates(t *testing.T) {
	const id = "live:30308220:9002"
	bus := newFakeBus(id)
	settleErr := errors.New("结清落库失败")
	bus.markErr = settleErr
	handler := deliverHandler(resumeDeps{
		sender:     &fakeSender{resumeErr: &sender.Error{Kind: sender.KindUncertain}},
		bus:        bus,
		retryDelay: time.Second,
		log:        zap.NewNop(),
	})

	_, err := handler(context.Background(), scheduler.Work{Claim: verifyClaim(id, "30308220")})
	if err == nil {
		t.Fatal("结清失败必须返回错误")
	}
	if !errors.Is(err, settleErr) {
		t.Fatalf("错误链未保留结清失败：%v", err)
	}
	if got := bus.finalStatus(id); got != event.StatusQueued {
		t.Fatalf("未结清事件被改写为 %q", got)
	}
}

// loopBus 是 T06 用的调度总线：结清语义与 PG 一致（释放租约、版本递增）。
type loopBus struct {
	mu      sync.Mutex
	claims  []event.Claim
	events  map[string]event.Event
	stages  map[string]event.Stage
	leases  map[string]event.Lease
	lost    map[string]bool
	notify  chan struct{}
	settled chan string
}

func newLoopBus(claims ...event.Claim) *loopBus {
	b := &loopBus{
		events: map[string]event.Event{}, stages: map[string]event.Stage{},
		leases: map[string]event.Lease{}, lost: map[string]bool{},
		notify: make(chan struct{}, 1), settled: make(chan string, len(claims)+1),
	}
	for _, c := range claims {
		b.claims = append(b.claims, c)
		b.events[c.Event.ID] = c.Event
		b.stages[c.Event.ID] = c.Stage
		b.leases[c.Event.ID] = c.Lease
	}
	return b
}

func (b *loopBus) Publish(context.Context, event.Event) (bool, error) {
	return true, nil
}

func (b *loopBus) ClaimPending(_ context.Context, n int) ([]event.Claim, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n = min(n, len(b.claims))
	out := append([]event.Claim(nil), b.claims[:n]...)
	b.claims = b.claims[n:]
	return out, nil
}

func (b *loopBus) Renew(_ context.Context, c event.Claim) (event.Claim, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lost[c.Event.ID] {
		return c, event.ErrLeaseLost
	}
	c.Event = b.events[c.Event.ID]
	c.Stage = b.stages[c.Event.ID]
	c.Lease = b.leases[c.Event.ID]
	return c, nil
}

func (b *loopBus) Recover(context.Context, int) (int, error) { return 0, nil }
func (b *loopBus) Notify() <-chan struct{}                   { return b.notify }

func (b *loopBus) Mark(_ context.Context, c event.Claim, _ []event.Status, to event.Status) error {
	return b.finish(c, to)
}

func (b *loopBus) Defer(_ context.Context, c event.Claim, _ time.Time) error {
	return b.finish(c, event.StatusRetry)
}

func (b *loopBus) finish(c event.Claim, to event.Status) error {
	b.mu.Lock()
	if b.lost[c.Event.ID] {
		b.mu.Unlock()
		return event.ErrLeaseLost
	}
	e := b.events[c.Event.ID]
	e.Status = to
	b.events[c.Event.ID] = e
	b.lost[c.Event.ID] = true
	lease := b.leases[c.Event.ID]
	lease.Owner, lease.Version = "", lease.Version+1
	b.leases[c.Event.ID] = lease
	b.mu.Unlock()

	select {
	case b.settled <- c.Event.ID:
	default:
	}
	select {
	case b.notify <- struct{}{}:
	default:
	}
	return nil
}

func (b *loopBus) statusOf(id string) event.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.events[id].Status
}

// loopRepo 让调度器在结清后能读到与总线一致的事件快照。
type loopRepo struct {
	bus       *loopBus
	snapshots chan string
}

func (r *loopRepo) Admit(context.Context, event.Claim, store.Task, store.ScheduleQuota) (store.ScheduleAdmission, error) {
	return store.ScheduleAdmission{}, nil
}

func (r *loopRepo) Earlier(context.Context, event.Claim) (*time.Time, error) { return nil, nil }

func (r *loopRepo) Task(context.Context, string) (store.Task, error) {
	return store.Task{}, store.ErrNotFound
}

func (r *loopRepo) Snapshot(_ context.Context, id string) (event.Claim, error) {
	r.bus.mu.Lock()
	c := event.Claim{Event: r.bus.events[id], Stage: r.bus.stages[id], Lease: r.bus.leases[id]}
	r.bus.mu.Unlock()
	select {
	case r.snapshots <- id:
	default:
	}
	return c, nil
}

// scriptedSender 按事件返回预设的 Resume 结果。
type scriptedSender struct {
	mu      sync.Mutex
	results map[string]sender.Result
	errs    map[string]error
	resumes map[string]int
}

func (s *scriptedSender) Name() string { return "scripted" }

func (s *scriptedSender) Send(context.Context, event.Claim, sender.Reply) (sender.Result, error) {
	return sender.Result{}, errors.New("T06 不应调用 Send")
}

func (s *scriptedSender) Resume(_ context.Context, c event.Claim) (sender.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumes[c.Event.ID]++
	return s.results[c.Event.ID], s.errs[c.Event.ID]
}

func (s *scriptedSender) resumeCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resumes[id]
}

// TestVerifyUncertainSettledKeepsSchedulerRunning T06：一条 verify uncertain 结清后，
// 另一会话仍成功执行，调度循环不退出。
func TestVerifyUncertainSettledKeepsSchedulerRunning(t *testing.T) {
	const (
		badID  = "live:30308220:9101"
		goodID = "live:30308221:9102"
	)
	bus := newLoopBus(verifyClaim(badID, "30308220"), verifyClaim(goodID, "30308221"))
	repo := &loopRepo{bus: bus, snapshots: make(chan string, 4)}
	snd := &scriptedSender{
		results: map[string]sender.Result{goodID: {OK: true, MsgID: "rpid-1", Verified: true}},
		errs:    map[string]error{badID: &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("核验未知")}},
		resumes: map[string]int{},
	}
	handler := deliverHandler(resumeDeps{sender: snd, bus: bus, retryDelay: time.Second, log: zap.NewNop()})

	opts := scheduler.DefaultOptions()
	opts.PollInterval = 10 * time.Millisecond
	opts.RetryDelay = 10 * time.Millisecond
	opts.OperationTimeout = time.Second
	opts.WorkerTimeout = time.Second
	sched, err := scheduler.New(bus, repo, opts, scheduler.Workers{Generate: handler, Deliver: handler, Verify: handler}, zap.NewNop())
	if err != nil {
		t.Fatalf("装配调度器失败：%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- sched.Run(ctx) }()

	// 等两个事件都走完 complete() 的快照核验；旧的「结清后仍返回 error」
	// 会在坏事件 complete 时直接终止 Run，这里会先收到 runErr。
	pending := map[string]bool{badID: true, goodID: true}
	timeout := time.After(3 * time.Second)
	for len(pending) > 0 {
		select {
		case id := <-repo.snapshots:
			delete(pending, id)
		case err := <-runErr:
			t.Fatalf("业务失败结清后调度循环不应退出：%v", err)
		case <-timeout:
			t.Fatalf("等待事件结清超时，剩余=%v", pending)
		}
	}

	if got := bus.statusOf(badID); got != event.StatusDead {
		t.Errorf("uncertain 事件最终状态 = %q，期望 dead", got)
	}
	if got := bus.statusOf(goodID); got != event.StatusDone {
		t.Errorf("其他会话最终状态 = %q，期望 done", got)
	}
	if n := snd.resumeCount(goodID); n != 1 {
		t.Errorf("其他会话 Resume 次数 = %d，期望 1", n)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("取消后 Run 应正常退出：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 Run 未退出")
	}
}
