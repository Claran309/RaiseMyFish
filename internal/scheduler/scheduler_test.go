package scheduler

import (
	"container/heap"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

type testTimer struct{ stopped atomic.Bool }

func (t *testTimer) Stop() bool { return !t.stopped.Swap(true) }

type timedCall struct {
	at    time.Time
	fn    func()
	timer *testTimer
}
type testClock struct {
	mu    sync.Mutex
	now   time.Time
	calls []timedCall
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) AfterFunc(d time.Duration, fn func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &testTimer{}
	c.calls = append(c.calls, timedCall{c.now.Add(d), fn, timer})
	return timer
}
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var ready []func()
	for _, call := range c.calls {
		if !call.timer.stopped.Load() && !call.at.After(c.now) {
			call.timer.stopped.Store(true)
			ready = append(ready, call.fn)
		}
	}
	c.mu.Unlock()
	for _, fn := range ready {
		fn()
	}
}

func claimFor(id, session, text string, at time.Time) event.Claim {
	return event.Claim{Event: event.Event{ID: id, Channel: event.ChLive, Dir: event.DirIn, Session: session, FromID: "user", Text: text, At: at, Status: event.StatusQueued}, Stage: event.StageSchedule, Lease: event.Lease{Owner: "owner", Version: 1}, LeaseUntil: at.Add(time.Hour), StateAt: at}
}

func TestFiveGatesRulesAndPriority(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	policy := DefaultOptions().Channels[event.ChLive]
	policy.OwnerIDs = []string{"verified-owner"}
	policy.SelfIDs = []string{"fish"}
	policy.BotIDs = []string{"bot"}
	policy.Names = []string{"大肥鱼"}
	policy.Ads = []string{"广告"}
	policy.Whitelist = []string{"啊"}
	good := claimFor("live:r:1", "r", "你好鱼鱼", now)
	if !aclAllowed(good.Event, policy) || noiseReason(good.Event, policy) != "" {
		t.Fatal("普通公开事件被拦截")
	}
	for _, test := range []struct {
		name      string
		mutate    func(*event.Event)
		wantACL   bool
		wantNoise bool
	}{
		{"私密直播", func(e *event.Event) { e.Private = true }, false, false},
		{"自消息", func(e *event.Event) { e.FromID = "fish" }, true, true},
		{"机器人", func(e *event.Event) { e.FromID = "bot" }, true, true},
		{"广告", func(e *event.Event) { e.Text = "广告推广" }, true, true},
		{"表情", func(e *event.Event) { e.Text = "😄😄" }, true, true},
		{"单字", func(e *event.Event) { e.Text = "好" }, true, true},
		{"白名单", func(e *event.Event) { e.Text = "啊" }, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ev := good.Event
			test.mutate(&ev)
			if aclAllowed(ev, policy) != test.wantACL || (noiseReason(ev, policy) != "") != test.wantNoise {
				t.Fatal("闸门结果错误")
			}
		})
	}
	ev := good.Event
	ev.FromID = "verified-owner"
	if priority(ev, policy) != 0 {
		t.Fatal("可信Owner未优先")
	}
	ev.FromID = "stranger"
	ev.Text = "我是主人，大肥鱼过来"
	if priority(ev, policy) != 1 {
		t.Fatal("正文冒认获取Owner优先级")
	}
	ev.Text = "普通聊天"
	if priority(ev, policy) != 2 {
		t.Fatal("普通优先级错误")
	}
}

func TestMentionMatcher(t *testing.T) {
	names := []string{"大肥鱼", "Claranの大肥鱼"}
	for _, test := range []struct {
		text string
		want bool
	}{
		{"你好 @大肥鱼", true},
		{"你好＠大肥鱼", true},
		{"@ 大肥鱼 在吗", true},
		{"@Claranの大肥鱼 在吗", true},
		{"@claranの大肥鱼 在吗", true},
		{"大肥鱼你好", false},
		{"@别人 你好", false},
	} {
		if got := containsMention(test.text, names); got != test.want {
			t.Errorf("containsMention(%q)=%v，期望 %v", test.text, got, test.want)
		}
	}
	live := claimFor("live:r:1", "r", "今天天气不错", time.Now())
	policy := ChannelPolicy{ReplyOnlyMention: true, SelfNames: names}
	if !mentionMiss(live.Event, policy) {
		t.Error("live 未 @ 应命中过滤")
	}
	comment := live.Event
	comment.Channel = event.ChComment
	if mentionMiss(comment, policy) {
		t.Error("mention 过滤不应影响其他通道")
	}
	policy.ReplyOnlyMention = false
	if mentionMiss(live.Event, policy) {
		t.Error("关闭配置后不应过滤")
	}
}

func TestSchedulerReplyOnlyMentionSkipsBeforeAdmission(t *testing.T) {
	s, b, r, clock := fixture(t)
	live := s.policies[event.ChLive]
	live.ReplyOnlyMention = true
	live.SelfNames = []string{"大肥鱼"}
	s.policies[event.ChComment] = &policyState{
		ChannelPolicy: ChannelPolicy{Public: true, Brain: "openai", RatePerMinute: 60, Burst: 10, DailyQuota: 100, MinRunes: 2},
		limiter:       rate.NewLimiter(rate.Limit(1), 10),
	}
	hit := claimFor("live:r:hit", "r", "@大肥鱼 在吗", clock.Now())
	miss := claimFor("live:x:miss", "x", "今天天气真不错", clock.Now())
	comment := claimFor("comment:c:1", "c", "普通评论", clock.Now())
	comment.Event.Channel = event.ChComment
	b.add(hit, miss, comment)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.marked(miss.Event.ID) != event.StatusSkipped {
		t.Fatal("未 @ 的 live 弹幕未在准入前跳过")
	}
	admitted := strings.Join(r.admitted, ",")
	if !strings.Contains(admitted, hit.Event.ID) || !strings.Contains(admitted, comment.Event.ID) {
		t.Fatalf("@命中或其他通道被误拦：%v", r.admitted)
	}
	if strings.Contains(admitted, miss.Event.ID) {
		t.Fatal("未 @ 的弹幕进入了 Admit")
	}
	drainDone(t, s)
	drainDone(t, s)
}

func TestSchedulerReplyOnlyMentionCanBeDisabled(t *testing.T) {
	s, b, r, clock := fixture(t)
	p := s.policies[event.ChLive]
	p.ReplyOnlyMention = false
	p.SelfNames = []string{"大肥鱼"}
	a := claimFor("live:a:1", "a", "今天天气不错", clock.Now())
	c := claimFor("live:c:1", "c", "还没有 @", clock.Now())
	b.add(a, c)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.admitted) != 2 {
		t.Fatalf("关闭 mention 过滤后应全部准入：%v", r.admitted)
	}
	drainDone(t, s)
	drainDone(t, s)
}

func TestPriorityHeapStable(t *testing.T) {
	now := time.Now()
	q := priorityQueue{}
	for _, entry := range []struct {
		id     string
		p      int
		offset time.Duration
	}{{"normal", 2, 0}, {"owner", 0, time.Second}, {"b", 1, 0}, {"a", 1, 0}, {"active", 3, -time.Second}} {
		c := claimFor(entry.id, entry.id, "你好", now.Add(entry.offset))
		heap.Push(&q, &queued{claim: c, priority: entry.p})
	}
	for _, want := range []string{"owner", "a", "b", "normal", "active"} {
		if got := heap.Pop(&q).(*queued).claim.Event.ID; got != want {
			t.Fatalf("%s != %s", got, want)
		}
	}
}

func TestCalendarQuotaWindowHandlesDST(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, location)
	from, until := dayWindow(now, location)
	if from.Hour() != 0 || until.Hour() != 0 || until.Sub(from) != 25*time.Hour {
		t.Fatal("夏令时回退日不能按24小时切日")
	}
}

type fakeBus struct {
	event.Bus
	mu       sync.Mutex
	claims   []event.Claim
	states   map[string]event.Event
	stages   map[string]event.Stage
	marks    map[string]event.Status
	deferred map[string]time.Time
	renewed  map[string]int
	lost     map[string]bool
	leases   map[string]event.Lease
	notify   chan struct{}
}

func newFakeBus() *fakeBus {
	return &fakeBus{states: map[string]event.Event{}, stages: map[string]event.Stage{}, marks: map[string]event.Status{}, deferred: map[string]time.Time{}, renewed: map[string]int{}, lost: map[string]bool{}, leases: map[string]event.Lease{}, notify: make(chan struct{}, 1)}
}
func (b *fakeBus) add(claims ...event.Claim) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range claims {
		b.claims = append(b.claims, c)
		b.states[c.Event.ID] = c.Event
		b.stages[c.Event.ID] = c.Stage
		b.leases[c.Event.ID] = c.Lease
		b.lost[c.Event.ID] = false
	}
}
func (b *fakeBus) ClaimPending(_ context.Context, n int) ([]event.Claim, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n = min(n, len(b.claims))
	out := append([]event.Claim(nil), b.claims[:n]...)
	b.claims = b.claims[n:]
	return out, nil
}
func (b *fakeBus) Recover(context.Context, int) (int, error) { return 0, nil }
func (b *fakeBus) Notify() <-chan struct{}                   { return b.notify }
func (b *fakeBus) Renew(_ context.Context, c event.Claim) (event.Claim, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lost[c.Event.ID] {
		return c, event.ErrLeaseLost
	}
	b.renewed[c.Event.ID]++
	c.Stage = b.stages[c.Event.ID]
	c.Event = b.states[c.Event.ID]
	return c, nil
}
func (b *fakeBus) Mark(_ context.Context, c event.Claim, _ []event.Status, to event.Status) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lost[c.Event.ID] {
		return event.ErrLeaseLost
	}
	b.marks[c.Event.ID] = to
	e := b.states[c.Event.ID]
	e.Status = to
	b.states[c.Event.ID] = e
	b.lost[c.Event.ID] = true
	l := b.leases[c.Event.ID]
	l.Owner = ""
	l.Version++
	b.leases[c.Event.ID] = l
	return nil
}
func (b *fakeBus) Defer(_ context.Context, c event.Claim, until time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lost[c.Event.ID] {
		return event.ErrLeaseLost
	}
	b.deferred[c.Event.ID] = until
	e := b.states[c.Event.ID]
	e.Status = event.StatusRetry
	b.states[c.Event.ID] = e
	b.lost[c.Event.ID] = true
	l := b.leases[c.Event.ID]
	l.Owner = ""
	l.Version++
	b.leases[c.Event.ID] = l
	return nil
}

type fakeRepo struct {
	bus         *fakeBus
	tasks       map[string]store.Task
	admitted    []string
	limited     bool
	err         error
	earlier     map[string]time.Time
	taskRead    func()
	earlierRead func()
}

func (r *fakeRepo) Snapshot(_ context.Context, id string) (event.Claim, error) {
	r.bus.mu.Lock()
	defer r.bus.mu.Unlock()
	return event.Claim{Event: r.bus.states[id], Stage: r.bus.stages[id], Lease: r.bus.leases[id]}, r.err
}
func (b *fakeBus) marked(id string) event.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.marks[id]
}
func (r *fakeRepo) Task(_ context.Context, id string) (store.Task, error) {
	if r.taskRead != nil {
		r.taskRead()
	}
	t, ok := r.tasks[id]
	if !ok {
		return t, store.ErrNotFound
	}
	return t, r.err
}
func (r *fakeRepo) Earlier(_ context.Context, c event.Claim) (*time.Time, error) {
	if r.earlierRead != nil {
		r.earlierRead()
	}
	if until, ok := r.earlier[c.Event.ID]; ok {
		return &until, r.err
	}
	return nil, r.err
}
func (r *fakeRepo) Admit(_ context.Context, c event.Claim, t store.Task, q store.ScheduleQuota) (store.ScheduleAdmission, error) {
	if r.err != nil {
		return store.ScheduleAdmission{}, r.err
	}
	if r.limited {
		return store.ScheduleAdmission{WaitUntil: &q.Until}, nil
	}
	r.tasks[c.Event.ID] = t
	r.admitted = append(r.admitted, c.Event.ID)
	r.bus.mu.Lock()
	r.bus.stages[c.Event.ID] = event.StageGenerate
	r.bus.mu.Unlock()
	return store.ScheduleAdmission{Inserted: true}, nil
}

func fixture(t *testing.T) (*Scheduler, *fakeBus, *fakeRepo, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	b := newFakeBus()
	r := &fakeRepo{bus: b, tasks: map[string]store.Task{}}
	opts := DefaultOptions()
	opts.Clock = clock
	opts.Channels[event.ChLive] = ChannelPolicy{Public: true, Brain: "openai", RatePerMinute: 60, Burst: 10, DailyQuota: 100, Cooldown: time.Minute, MinRunes: 2, RepeatWindow: time.Minute}
	handler := func(ctx context.Context, w Work) (Result, error) {
		err := b.Mark(ctx, w.Claim, []event.Status{event.StatusQueued}, event.StatusDone)
		return Result{Replied: true}, err
	}
	s, err := New(b, r, opts, Workers{Generate: handler, Verify: handler}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return s, b, r, clock
}

func TestSchedulerAdmissionGates(t *testing.T) {
	s, b, r, clock := fixture(t)
	now := clock.Now()
	s.workers.Generate = func(ctx context.Context, w Work) (Result, error) {
		return Result{}, b.Mark(ctx, w.Claim, nil, event.StatusDone)
	}
	a := claimFor("live:r:a", "r", "你好鱼鱼", now)
	bad := claimFor("live:x:bad", "x", "😄😄", now)
	b.add(a, a, bad)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.admitted) != 1 || b.marked(bad.Event.ID) != event.StatusSkipped {
		t.Fatal("重复/噪音闸未生效")
	}
	drainDone(t, s)
	duplicate := claimFor("live:r:b", "r", "你好鱼鱼", now.Add(time.Second))
	b.add(duplicate)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.marked(duplicate.Event.ID) != event.StatusSkipped {
		t.Fatal("同用户短时重复未跳过")
	}
}

func drainDone(t *testing.T, s *Scheduler) {
	t.Helper()
	select {
	case done := <-s.done:
		if err := s.complete(context.Background(), done); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker未退出")
	}
}

func TestSchedulerBusyCooldownRateAndQuota(t *testing.T) {
	t.Run("会话忙", func(t *testing.T) {
		s, b, r, clock := fixture(t)
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		s.workers.Generate = func(ctx context.Context, w Work) (Result, error) {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return Result{}, b.Mark(ctx, w.Claim, nil, event.StatusDone)
		}
		a := claimFor("live:r:a", "r", "第一句话", clock.Now())
		c := claimFor("live:r:b", "r", "第二句话", clock.Now().Add(time.Second))
		b.add(a, c)
		if err := s.cycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		<-entered
		if len(r.admitted) != 1 || b.deferred[c.Event.ID].IsZero() {
			t.Fatal("同会话并发进入worker")
		}
		close(release)
		drainDone(t, s)
	})
	t.Run("冷却AfterFunc", func(t *testing.T) {
		s, b, r, clock := fixture(t)
		b.add(claimFor("live:r:a", "r", "第一句话", clock.Now()))
		if err := s.cycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		drainDone(t, s)
		b.add(claimFor("live:r:b", "r", "第二句话", clock.Now()))
		if err := s.cycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(r.admitted) != 1 || b.deferred["live:r:b"].IsZero() {
			t.Fatal("冷却未生效")
		}
		if len(clock.calls) == 0 {
			t.Fatal("没有AfterFunc")
		}
		clock.advance(time.Minute)
		select {
		case <-s.wake:
		default:
			t.Fatal("冷却到期未唤醒")
		}
		s.stopTimers()
	})
	t.Run("令牌桶", func(t *testing.T) {
		s, b, r, clock := fixture(t)
		s.policies[event.ChLive].limiter.SetBurst(1)
		b.add(claimFor("live:a:1", "a", "第一句话", clock.Now()), claimFor("live:b:1", "b", "第二句话", clock.Now()))
		if err := s.cycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(r.admitted) != 1 || len(b.deferred) != 1 {
			t.Fatal("令牌边界错误")
		}
		drainDone(t, s)
	})
	t.Run("每日配额", func(t *testing.T) {
		s, b, r, clock := fixture(t)
		r.limited = true
		b.add(claimFor("live:r:q", "r", "你好鱼鱼", clock.Now()))
		if err := s.cycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, until := dayWindow(clock.Now(), time.UTC)
		if !b.deferred["live:r:q"].Equal(until) || len(r.admitted) != 0 {
			t.Fatal("配额未延期到次日")
		}
	})
}

func TestRecoveredStagesSkipFirstNoiseAndQuota(t *testing.T) {
	s, b, r, clock := fixture(t)
	r.limited = true
	generated := make(chan struct{}, 1)
	verified := make(chan struct{}, 1)
	s.workers.Generate = func(ctx context.Context, w Work) (Result, error) {
		if w.Task == nil {
			t.Error("恢复缺Task")
		}
		generated <- struct{}{}
		return Result{}, b.Mark(ctx, w.Claim, nil, event.StatusDone)
	}
	s.workers.Verify = func(ctx context.Context, w Work) (Result, error) {
		if w.Task != nil {
			t.Error("核验伪造Task")
		}
		verified <- struct{}{}
		return Result{}, b.Mark(ctx, w.Claim, nil, event.StatusDone)
	}
	g := claimFor("live:g:1", "g", "😄", clock.Now())
	g.Stage = event.StageGenerate
	v := claimFor("live:v:1", "v", "😄", clock.Now())
	v.Stage = event.StageVerify
	r.tasks[g.Event.ID] = store.Task{ID: "old", EventID: g.Event.ID, Channel: g.Event.Channel, Session: g.Event.Session, Brain: "openai", Priority: 2, CreatedAt: clock.Now()}
	b.add(g, v)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainDone(t, s)
	drainDone(t, s)
	if len(generated) != 1 || len(verified) != 1 || len(r.admitted) != 0 {
		t.Fatal("Stage分流错误")
	}
}

func TestRunCancellationStopsWorkerAndTimers(t *testing.T) {
	s, b, _, clock := fixture(t)
	s.clock = systemClock{}
	s.options.PollInterval = 10 * time.Millisecond
	c := claimFor("live:r:cancel", "r", "你好鱼鱼", clock.Now())
	c.LeaseUntil = time.Now().Add(time.Hour)
	b.add(c)
	entered := make(chan struct{})
	s.workers.Generate = func(ctx context.Context, _ Work) (Result, error) {
		close(entered)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- s.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("worker未启动")
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消未退出")
	}
}

func TestConstructorAndWorkerUnsettled(t *testing.T) {
	s, b, r, clock := fixture(t)
	if _, err := New(nil, r, DefaultOptions(), s.workers, zap.NewNop()); err == nil {
		t.Fatal("接受空Bus")
	}
	s.workers.Generate = func(context.Context, Work) (Result, error) { return Result{}, nil }
	b.add(claimFor("live:r:unsettled", "r", "你好鱼鱼", clock.Now()))
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case done := <-s.done:
		if err := s.complete(context.Background(), done); !errors.Is(err, ErrUnsettled) {
			t.Fatal("worker未结清也被当成功", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker未结束")
	}
}

func TestSettlementBeforeRenewStillAppliesCooldown(t *testing.T) {
	s, b, _, clock := fixture(t)
	settled := make(chan struct{})
	release := make(chan struct{})
	s.workers.Generate = func(ctx context.Context, w Work) (Result, error) {
		err := b.Mark(ctx, w.Claim, nil, event.StatusDone)
		close(settled)
		<-release
		return Result{Replied: true}, err
	}
	b.add(claimFor("live:r:settle", "r", "已经完成投递", clock.Now()))
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-settled
	clock.advance(20 * time.Minute)
	if err := s.cycle(context.Background()); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	drainDone(t, s)
	if cd := s.cooldowns[sessionKey{event.ChLive, "r"}]; !cd.until.After(clock.Now()) {
		t.Fatal("正常结清被当成丢租，漏设回复冷却")
	}
	s.stopTimers()
}

func TestExpiredCooldownIsRemoved(t *testing.T) {
	s, b, _, clock := fixture(t)
	b.add(claimFor("live:r:cleanup", "r", "你好鱼鱼", clock.Now()))
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainDone(t, s)
	clock.advance(time.Minute)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.cooldowns) != 0 {
		t.Fatal("已到期冷却仍保留Timer/会话引用")
	}
}

func TestRetryReclaimedBeforeCallbackRetainsConservativeCooldown(t *testing.T) {
	s, b, _, clock := fixture(t)
	settled := make(chan struct{})
	release := make(chan struct{})
	s.workers.Generate = func(ctx context.Context, w Work) (Result, error) {
		b.mu.Lock()
		b.stages[w.Claim.Event.ID] = event.StageVerify
		b.mu.Unlock()
		err := b.Defer(ctx, w.Claim, clock.Now().Add(time.Millisecond))
		close(settled)
		<-release
		return Result{Replied: true}, err
	}
	c := claimFor("live:r:deferred-receipt", "r", "已经投递，核验延期", clock.Now())
	b.add(c)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-settled
	newClaim := c
	newClaim.Stage = event.StageVerify
	newClaim.Lease.Version = 3
	newClaim.Lease.Owner = "new-owner"
	b.add(newClaim)
	if err := s.cycle(context.Background()); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	drainDone(t, s)
	if !s.cooldowns[session(c.Event)].until.After(clock.Now()) {
		t.Fatal("回执晚于retry重领，正投递事实不应漏设保守冷却")
	}
	s.stopTimers()
}

func TestWaitingAndActiveClaimsRenewAndCapacityIsBounded(t *testing.T) {
	s, b, _, clock := fixture(t)
	s.options.MaxConcurrent = 1
	s.options.MaxPending = 2
	entered := make(chan struct{})
	release := make(chan struct{})
	s.workers.Generate = func(ctx context.Context, w Work) (Result, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return Result{}, b.Mark(ctx, w.Claim, nil, event.StatusDone)
	}
	for _, pair := range []struct{ id, session string }{{"live:a:1", "a"}, {"live:b:1", "b"}, {"live:c:1", "c"}} {
		b.add(claimFor(pair.id, pair.session, "你好鱼鱼", clock.Now()))
	}
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-entered
	if len(s.active) != 1 || len(s.pending) != 1 {
		t.Fatal("未保持容量和全局并发边界")
	}
	clock.advance(20 * time.Minute)
	if err := s.cycle(context.Background()); err != nil {
		close(release)
		t.Fatal(err)
	}
	b.mu.Lock()
	waitingRenewed := b.renewed["live:b:1"]
	activeRenewed := b.renewed["live:a:1"]
	unclaimed := len(b.claims)
	b.mu.Unlock()
	if waitingRenewed < 1 || activeRenewed < 3 || unclaimed != 1 {
		close(release)
		t.Fatal("等待/活动未续租，或超额领取")
	}
	close(release)
	drainDone(t, s)
}

func TestLostLeaseKeepsSessionLockedUntilCallbackReturns(t *testing.T) {
	s, b, r, clock := fixture(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	s.workers.Generate = func(ctx context.Context, _ Work) (Result, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return Result{}, ctx.Err()
	}
	c := claimFor("live:r:active", "r", "第一句话", clock.Now())
	b.add(c)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-entered
	b.mu.Lock()
	b.lost[c.Event.ID] = true
	b.mu.Unlock()
	clock.advance(20 * time.Minute)
	next := claimFor("live:r:next", "r", "第二句话", clock.Now())
	b.add(next)
	if err := s.cycle(context.Background()); err != nil {
		close(release)
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("丢租没有取消回调")
	}
	if len(r.admitted) != 1 || b.deferred[next.Event.ID].IsZero() {
		close(release)
		t.Fatal("回调未结束就释放会话锁")
	}
	close(release)
	drainDone(t, s)
	if len(s.sessions) != 0 {
		t.Fatal("回调返回后未释放锁")
	}
}

func TestAdmissionRefundAndPriorityGate(t *testing.T) {
	s, b, r, clock := fixture(t)
	s.policies[event.ChLive].limiter.SetBurst(1)
	r.limited = true
	b.add(claimFor("live:a:denied", "a", "配额不足", clock.Now()))
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.limited = false
	b.add(claimFor("live:b:accepted", "b", "配额恢复", clock.Now()))
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.admitted) != 1 {
		t.Fatal("失败准入未退还令牌")
	}
	drainDone(t, s)
	s.stopTimers()
	s, b, r, clock = fixture(t)
	s.options.MaxConcurrent = 1
	s.policies[event.ChLive].OwnerIDs = []string{"owner"}
	ordinary := claimFor("live:a:normal", "a", "普通消息", clock.Now())
	owner := claimFor("live:b:owner", "b", "主人指令", clock.Now().Add(time.Second))
	owner.Event.FromID = "owner"
	b.add(ordinary, owner)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.admitted) != 1 || r.admitted[0] != owner.Event.ID {
		t.Fatal("未按优先级准入")
	}
	drainDone(t, s)
	s.stopTimers()
}

func TestACLAndEarlierGateInDispatch(t *testing.T) {
	s, b, r, clock := fixture(t)
	bad := claimFor("live:r:private", "r", "私密数据", clock.Now())
	bad.Event.Private = true
	b.add(bad)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.admitted) != 0 || b.marked(bad.Event.ID) != event.StatusSkipped {
		t.Fatal("ACL错误事件进入Task")
	}
	blocked := claimFor("live:x:later", "x", "后来的事件", clock.Now())
	r.earlier = map[string]time.Time{blocked.Event.ID: clock.Now().Add(time.Hour)}
	b.add(blocked)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !b.deferred[blocked.Event.ID].Equal(clock.Now().Add(time.Hour)) {
		t.Fatal("顺序闸未保留较早事件的延期")
	}
	missing := claimFor("live:y:missing", "y", "恢复消息", clock.Now())
	missing.Stage = event.StageGenerate
	b.add(missing)
	if err := s.cycle(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("缺失恢复Task被静默重建")
	}
}

func TestCallbackErrorPanicAndRunReentry(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			s, b, _, clock := fixture(t)
			sentinel := errors.New("private-error-canary")
			s.workers.Generate = func(context.Context, Work) (Result, error) {
				if mode == "panic" {
					panic("private-panic-canary")
				}
				return Result{}, sentinel
			}
			b.add(claimFor("live:r:"+mode, "r", "你好鱼鱼", clock.Now()))
			if err := s.cycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			select {
			case done := <-s.done:
				err := s.complete(context.Background(), done)
				if err == nil || strings.Contains(err.Error(), "canary") {
					t.Fatal("错误丢失或泄露正文")
				}
				if mode == "error" && !errors.Is(err, sentinel) {
					t.Fatal("丢失原因链")
				}
			case <-time.After(time.Second):
				t.Fatal("worker未退出")
			}
		})
	}
	s, _, _, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(ctx); !errors.Is(err, ErrStarted) {
		t.Fatal("Run可以重复启动")
	}
}

func TestRecoveredDispatchFencesAfterRepositoryReads(t *testing.T) {
	for _, stage := range []event.Stage{event.StageGenerate, event.StageVerify} {
		t.Run(string(stage), func(t *testing.T) {
			s, b, r, clock := fixture(t)
			c := claimFor("live:r:late-expiry", "r", "恢复任务", clock.Now())
			c.Stage = stage
			r.tasks[c.Event.ID] = store.Task{ID: "task", EventID: c.Event.ID, Channel: c.Event.Channel, Session: c.Event.Session, Brain: "openai", Priority: 2, CreatedAt: clock.Now()}
			lose := func() { b.mu.Lock(); b.lost[c.Event.ID] = true; b.mu.Unlock() }
			if stage == event.StageGenerate {
				reads := 0
				r.taskRead = func() {
					reads++
					if reads == 2 {
						lose()
					}
				}
			} else {
				r.earlierRead = lose
			}
			handler := func(context.Context, Work) (Result, error) { return Result{}, event.ErrLeaseLost }
			s.workers.Generate = handler
			s.workers.Verify = handler
			b.add(c)
			if err := s.cycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(s.active) > 0 {
				drainDone(t, s)
				t.Fatal("仓储查询期间丢租，仍启动了下游回调")
			}
		})
	}
}

func TestExternalReclaimBeforeCompletionIsNotUnsettled(t *testing.T) {
	s, b, _, clock := fixture(t)
	settled := make(chan struct{})
	release := make(chan struct{})
	s.workers.Generate = func(ctx context.Context, w Work) (Result, error) {
		err := b.Defer(ctx, w.Claim, clock.Now().Add(time.Millisecond))
		close(settled)
		<-release
		return Result{Replied: true}, err
	}
	c := claimFor("live:r:external-owner", "r", "投递后核验待续", clock.Now())
	b.add(c)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-settled
	// 模拟另一实例已经领取；不让当前实例经过add/renew预先标lost。
	b.mu.Lock()
	b.leases[c.Event.ID] = event.Lease{Owner: "external-owner", Version: 3}
	state := b.states[c.Event.ID]
	state.Status = event.StatusQueued
	b.states[c.Event.ID] = state
	b.mu.Unlock()
	close(release)
	select {
	case done := <-s.done:
		if err := s.complete(context.Background(), done); err != nil {
			t.Fatal("外代的queued误报为本代未结清", err)
		}
	case <-time.After(time.Second):
		t.Fatal("回调未结束")
	}
	if !s.cooldowns[session(c.Event)].until.After(clock.Now()) {
		t.Fatal("晚到的正投递回执遗漏保守冷却")
	}
	s.stopTimers()
}

func TestDeliverRecoveryNeverRegenerates(t *testing.T) {
	s, b, r, clock := fixture(t)
	generated := 0
	delivered := make(chan struct{}, 1)
	s.workers.Generate = func(context.Context, Work) (Result, error) { generated++; return Result{}, nil }
	s.workers.Deliver = func(ctx context.Context, w Work) (Result, error) {
		delivered <- struct{}{}
		return Result{}, b.Mark(ctx, w.Claim, nil, event.StatusDone)
	}
	c := claimFor("live:r:delivery", "r", "😄", clock.Now())
	c.Stage = event.StageDeliver
	r.tasks[c.Event.ID] = store.Task{ID: "old-task", EventID: c.Event.ID, Channel: c.Event.Channel, Session: c.Event.Session, Brain: "openai", Priority: 2, CreatedAt: clock.Now()}
	r.limited = true
	b.add(c)
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	drainDone(t, s)
	if len(delivered) != 1 || generated != 0 || len(r.admitted) != 0 {
		t.Fatal("deliver恢复走了生成或重复准入")
	}
}
