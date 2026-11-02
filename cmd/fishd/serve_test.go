package main

// 本文件测 0.11 的消费循环装配：mock Brain/Sender 跑通 Claim→Generate→Sender→Result，
// 以及 Verify 走 Resume、路由缺失/任务缺失要报错、配置映射正确。
// 全部用例不调用真实模型、不连 PG。

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	senderlive "RaiseMyFish/internal/sender/live"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeBrain 记录被调用时收到的 Task 与 ContextInput。
type fakeBrain struct {
	reply    brain.Reply
	err      error
	calls    int
	gotTask  brain.Task
	gotInput brain.ContextInput
	onReply  func()
}

func (f *fakeBrain) Name() string { return "fake" }

func (f *fakeBrain) Reply(_ context.Context, task brain.Task, in brain.ContextInput) (brain.Reply, error) {
	f.calls++
	f.gotTask = task
	f.gotInput = in
	if f.onReply != nil {
		f.onReply()
	}
	if f.err != nil {
		return brain.Reply{}, f.err
	}
	return f.reply, nil
}

// fakeSender 记录投递与恢复调用。
type fakeSender struct {
	sendResult   sender.Result
	sendErr      error
	resumeResult sender.Result
	resumeErr    error

	sendCalls, resumeCalls int
	gotReply               sender.Reply
	gotClaim               event.Claim
}

func (f *fakeSender) Name() string { return "fake" }

func (f *fakeSender) Send(_ context.Context, c event.Claim, r sender.Reply) (sender.Result, error) {
	f.sendCalls++
	f.gotClaim = c
	f.gotReply = r
	if f.sendErr != nil {
		return sender.Result{}, f.sendErr
	}
	return f.sendResult, nil
}

func (f *fakeSender) Resume(_ context.Context, c event.Claim) (sender.Result, error) {
	f.resumeCalls++
	f.gotClaim = c
	if f.resumeErr != nil {
		return sender.Result{}, f.resumeErr
	}
	return f.resumeResult, nil
}

// liveClaim 造一条 live 入向事件的 Claim。
func liveClaim(id string) event.Claim {
	return event.Claim{
		Event: event.Event{
			ID: id, Channel: event.ChLive, Dir: event.DirIn,
			Session: "30308220", FromID: "1", FromName: "路人",
			Text: "主播好", At: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
			Status: event.StatusQueued,
		},
		Lease: event.Lease{Owner: "worker-1", Version: 1},
	}
}

// liveTask 造一条与 claim 对应的调度任务。
func liveTask(id string) *store.Task {
	return &store.Task{ID: "task-" + id, EventID: id, Channel: event.ChLive, Session: "30308220", Brain: "default", CreatedAt: time.Now().UTC()}
}

// TestGenerateCallsBrainThenSender Claim→Generate→Sender→Result 链路。
func TestGenerateCallsBrainThenSender(t *testing.T) {
	engine := &fakeBrain{reply: brain.Reply{Text: "哼，才不是给你回的呢"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, MsgID: "m1", Verified: true}}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, newFakeBus(), time.Second, nil, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if workers.Generate == nil || workers.Verify == nil || workers.Deliver == nil {
		t.Fatal("Generate/Deliver/Verify 三个 handler 都不能为空（stage 分派会裸奔）")
	}

	claim := liveClaim("live:30308220:1")
	task := liveTask(claim.Event.ID)
	got, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: task})
	if err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if !got.Replied {
		t.Errorf("Result.Replied 应为 true，实际 %+v", got)
	}

	if engine.calls != 1 {
		t.Fatalf("Brain 调用 %d 次，期望 1", engine.calls)
	}
	if engine.gotTask.ID != task.ID || engine.gotTask.EventID != task.EventID {
		t.Errorf("传给 Brain 的 Task 不符：%+v", engine.gotTask)
	}
	if engine.gotInput.Event.ID != claim.Event.ID {
		t.Errorf("ContextInput.Event 不符：%+v", engine.gotInput.Event)
	}
	// Memory/Shared 只带 Scope，不带数据（不伪造记忆）
	if len(engine.gotInput.Memory.Recent) != 0 || len(engine.gotInput.Memory.Records) != 0 {
		t.Errorf("Memory 应为空快照：%+v", engine.gotInput.Memory)
	}
	wantScope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	if engine.gotInput.Memory.Scope != wantScope || engine.gotInput.Shared.Scope != wantScope {
		t.Errorf("Memory/Shared 的 Scope 不符：%+v / %+v", engine.gotInput.Memory.Scope, engine.gotInput.Shared.Scope)
	}
	if engine.gotInput.Knowledge != nil {
		t.Errorf("Knowledge 应为 nil：%+v", engine.gotInput.Knowledge)
	}

	if snd.sendCalls != 1 {
		t.Fatalf("Sender 调用 %d 次，期望 1", snd.sendCalls)
	}
	if snd.gotReply.Text != engine.reply.Text {
		t.Errorf("发送正文 = %q，期望 %q", snd.gotReply.Text, engine.reply.Text)
	}
	// Scope 必须显式带上，否则真实 sender 的 Validate 会拒
	if snd.gotReply.Scope != wantScope {
		t.Errorf("发送 Scope = %+v，期望 %+v", snd.gotReply.Scope, wantScope)
	}
}

// TestGenerateDefersScheduleStage 0.4 契约要求 schedule 只做过滤/准入；
// 即使 Task 非 nil（retry 遗留）也不能进入 Mark/Brain/Sender。
func TestGenerateDefersScheduleStage(t *testing.T) {
	cases := []struct {
		name string
		task *store.Task
	}{
		{name: "无Task"},
		{name: "有Task", task: liveTask("live:1:1")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeBrain{}
			snd := &fakeSender{}
			claim := liveClaim("live:1:1")
			claim.Stage = event.StageSchedule
			bus := newFakeBus(claim.Event.ID)

			workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, nil, nil, zap.NewNop())
			if err != nil {
				t.Fatalf("装配失败：%v", err)
			}

			before := time.Now()
			got, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: tc.task})
			if err != nil {
				t.Fatalf("schedule 阶段应交回调度而不是报错：%v", err)
			}
			if got != (scheduler.Result{}) {
				t.Errorf("Result = %+v，期望零值", got)
			}
			if engine.calls != 0 || snd.sendCalls != 0 {
				t.Errorf("不应调用 Brain/Sender：brain=%d sender=%d", engine.calls, snd.sendCalls)
			}

			bus.mu.Lock()
			until, deferred := bus.defers[claim.Event.ID]
			bus.mu.Unlock()
			if !deferred {
				t.Fatal("应调用 Defer 交回调度器")
			}
			if until.Before(before.Add(time.Second)) {
				t.Errorf("Defer 期限 %v 早于 now+retryDelay（before=%v）", until, before)
			}
			if status := bus.finalStatus(claim.Event.ID); status != event.StatusRetry {
				t.Errorf("事件状态 = %s，期望 retry", status)
			}
			if tos := bus.markTos(); len(tos) != 0 {
				t.Errorf("不应经过 Mark 状态迁移：%v", tos)
			}
		})
	}
}

// TestGenerateRejectsUnknownChannel 通道没有路由时报错，不静默丢弃。
func TestGenerateRejectsUnknownChannel(t *testing.T) {
	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, newFakeBus(), time.Second, nil, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	claim := liveClaim("comment:1:1")
	claim.Event.Channel = event.ChComment
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask("comment:1:1")}); err == nil {
		t.Fatal("未配置的通道应报错")
	}
}

// TestGeneratePropagatesErrors Brain 与 Sender 的错误都要原样上抛。
func TestGeneratePropagatesErrors(t *testing.T) {
	boom := errors.New("炸了")

	engine := &fakeBrain{err: boom}
	workers, _ := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, &fakeSender{}, newFakeBus(), time.Second, nil, nil, nil, nil, zap.NewNop())
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim("live:1:2"), Task: liveTask("live:1:2")}); !errors.Is(err, boom) {
		t.Errorf("Brain 错误未透传：%v", err)
	}

	snd := &fakeSender{sendErr: boom}
	workers2, _ := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{reply: brain.Reply{Text: "好"}}}, snd, newFakeBus(), time.Second, nil, nil, nil, nil, zap.NewNop())
	if _, err := workers2.Generate(context.Background(), scheduler.Work{Claim: liveClaim("live:1:3"), Task: liveTask("live:1:3")}); !errors.Is(err, boom) {
		t.Errorf("Sender 错误未透传：%v", err)
	}
}

// TestVerifyUsesResume 恢复路径只调 Resume，不重新生成。
func TestVerifyUsesResume(t *testing.T) {
	engine := &fakeBrain{}
	snd := &fakeSender{resumeResult: sender.Result{OK: true, Verified: true}}
	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, newFakeBus(), time.Second, nil, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := workers.Verify(context.Background(), scheduler.Work{Claim: liveClaim("live:1:4")})
	if err != nil {
		t.Fatalf("Verify 失败：%v", err)
	}
	if !got.Replied {
		t.Errorf("Result 不符：%+v", got)
	}
	if snd.resumeCalls != 1 || snd.sendCalls != 0 {
		t.Errorf("应只调 Resume：resume=%d send=%d", snd.resumeCalls, snd.sendCalls)
	}
	if engine.calls != 0 {
		t.Errorf("Verify 不应调用 Brain，实际 %d 次", engine.calls)
	}
}

// TestBuildConsumeWorkersRejectsNilSender 依赖缺失在构造期就失败。
func TestBuildConsumeWorkersRejectsNilSender(t *testing.T) {
	if _, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, nil, newFakeBus(), time.Second, nil, nil, nil, nil, zap.NewNop()); err == nil {
		t.Error("Sender 为 nil 应报错")
	}
	if _, err := buildConsumeWorkers(persona.Pack{}, nil, &fakeSender{}, newFakeBus(), time.Second, nil, nil, nil, nil, zap.NewNop()); err == nil {
		t.Error("没有路由应报错")
	}
	if _, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, nil, time.Second, nil, nil, nil, nil, zap.NewNop()); err == nil {
		t.Error("Bus 为 nil 应报错")
	}
	if _, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, newFakeBus(), 0, nil, nil, nil, nil, zap.NewNop()); err == nil {
		t.Error("重试间隔非正应报错")
	}
	if _, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, newFakeBus(), time.Second, nil, nil, nil, nil, nil); err == nil {
		t.Error("日志器为 nil 应报错")
	}
}

// TestBuildSchedulerOptionsMapping 配置 → 调度参数的映射（含公开/私密语义）。
func TestBuildSchedulerOptionsMapping(t *testing.T) {
	c := &config.Scheduler{
		MaxConcurrent: 6, MaxPending: 60, ClaimBatch: 5,
		PollIntervalMS: 200, RetryDelayMS: 3000, OperationTimeoutMS: 8000, WorkerTimeoutMS: 20000,
		Channels: map[event.Channel]config.ChannelPolicy{
			event.ChLive: {Brain: "default", RatePerMinute: 6, Burst: 2, DailyQuota: 200, CooldownMS: 10000, MinRunes: 2, RepeatWindowMS: 60000, OwnerIDs: []string{"42"}, SelfIDs: []string{"7"}},
			event.ChDM:   {Brain: "default", RatePerMinute: 1, Burst: 1, DailyQuota: 5},
		},
	}

	o := buildSchedulerOptions(c)
	if o.MaxConcurrent != 6 || o.MaxPending != 60 || o.ClaimBatch != 5 {
		t.Errorf("并发参数不符：%+v", o)
	}
	if o.PollInterval != 200*time.Millisecond || o.RetryDelay != 3*time.Second ||
		o.OperationTimeout != 8*time.Second || o.WorkerTimeout != 20*time.Second {
		t.Errorf("时间参数不符：%+v", o)
	}

	live := o.Channels[event.ChLive]
	if !live.Public || live.Private {
		t.Errorf("live 应为公开通道：%+v", live)
	}
	if live.Cooldown != 10*time.Second || live.RepeatWindow != time.Minute {
		t.Errorf("时长换算不符：%+v", live)
	}
	if len(live.OwnerIDs) != 1 || len(live.SelfIDs) != 1 {
		t.Errorf("ID 列表未透传：%+v", live)
	}
	dm := o.Channels[event.ChDM]
	if dm.Public || !dm.Private {
		t.Errorf("dm 应为私密通道：%+v", dm)
	}
}

// TestResolveRoutesRejectsMissingRoute 配置了不存在的路由要报错。
func TestResolveRoutesRejectsMissingRoute(t *testing.T) {
	policies := map[event.Channel]scheduler.ChannelPolicy{event.ChLive: {Brain: "missing"}}
	if _, err := resolveRoutes(policies, map[string]brain.Brain{"default": &fakeBrain{}}); err == nil {
		t.Fatal("缺失路由应报错")
	}

	ok, err := resolveRoutes(policies, map[string]brain.Brain{"missing": &fakeBrain{}})
	if err != nil {
		t.Fatalf("路由存在时不应报错：%v", err)
	}
	if ok[event.ChLive] == nil {
		t.Error("未解析出 Brain 实例")
	}
}

// TestConsumeEnabledSwitch 三个开关缺一不可。
func TestConsumeEnabledSwitch(t *testing.T) {
	cfg := config.Config{Scheduler: &config.Scheduler{}}
	cfg.Channels.Live.Enabled = true
	routes := map[string]brain.Brain{"default": &fakeBrain{}}

	if !consumeEnabled(cfg, routes) {
		t.Error("三个条件都满足时应启用")
	}
	cfg.Channels.Live.Enabled = false
	if consumeEnabled(cfg, routes) {
		t.Error("live 关闭时不应启用")
	}
	cfg.Channels.Live.Enabled = true
	if consumeEnabled(config.Config{}, routes) {
		t.Error("缺少 [scheduler] 时不应启用")
	}
	if consumeEnabled(cfg, nil) {
		t.Error("没有路由时不应启用")
	}
}

// fakeBus 模拟事件总线的结清语义：校验状态边、记录每次 Mark/Defer。
//
// 刻意复用 store.ValidateTransition：生产的 MarkClaim 就是先过这层校验。
// 假总线若不做同样校验，测试会「绿得没有意义」——状态边写错了也发现不了。
type fakeBus struct {
	mu      sync.Mutex
	status  map[string]event.Status
	marks   []markCall
	defers  map[string]time.Time
	markErr error
}

// markCall 记录一次状态结清。
type markCall struct {
	From []event.Status
	To   event.Status
}

// newFakeBus 造一个把指定事件置于 queued 的假总线。
func newFakeBus(ids ...string) *fakeBus {
	b := &fakeBus{status: map[string]event.Status{}, defers: map[string]time.Time{}}
	for _, id := range ids {
		b.status[id] = event.StatusQueued
	}
	return b
}

func (b *fakeBus) Publish(context.Context, event.Event) (bool, error)       { return true, nil }
func (b *fakeBus) ClaimPending(context.Context, int) ([]event.Claim, error) { return nil, nil }
func (b *fakeBus) Renew(context.Context, event.Claim) (event.Claim, error)  { return event.Claim{}, nil }
func (b *fakeBus) Recover(context.Context, int) (int, error)                { return 0, nil }
func (b *fakeBus) Notify() <-chan struct{}                                  { return make(chan struct{}) }

func (b *fakeBus) Mark(_ context.Context, c event.Claim, from []event.Status, to event.Status) error {
	// 先过状态机校验，和 MarkClaim 的检查顺序一致
	if err := store.ValidateTransition(from, to); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.markErr != nil {
		return b.markErr
	}
	current, ok := b.status[c.Event.ID]
	if !ok {
		current = event.StatusQueued
	}
	if !slices.Contains(from, current) {
		return store.ErrConflict
	}
	b.status[c.Event.ID] = to
	b.marks = append(b.marks, markCall{From: append([]event.Status(nil), from...), To: to})
	return nil
}

func (b *fakeBus) Defer(_ context.Context, c event.Claim, until time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status[c.Event.ID] = event.StatusRetry
	b.defers[c.Event.ID] = until
	return nil
}

// finalStatus 返回事件的最终状态。
func (b *fakeBus) finalStatus(id string) event.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status[id]
}

// markTos 返回每次 Mark 的目标状态序列。
func (b *fakeBus) markTos() []event.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]event.Status, 0, len(b.marks))
	for _, m := range b.marks {
		out = append(out, m.To)
	}
	return out
}

type fakeSchedulerRepo struct{}

func (fakeSchedulerRepo) Admit(context.Context, event.Claim, store.Task, store.ScheduleQuota) (store.ScheduleAdmission, error) {
	return store.ScheduleAdmission{}, store.ErrNotFound
}

func (fakeSchedulerRepo) Earlier(context.Context, event.Claim) (*time.Time, error) { return nil, nil }

func (fakeSchedulerRepo) Task(context.Context, string) (store.Task, error) {
	return store.Task{}, store.ErrNotFound
}

func (fakeSchedulerRepo) Snapshot(context.Context, string) (event.Claim, error) {
	return event.Claim{}, store.ErrNotFound
}

// TestSchedulerAcceptsAssembledWorkers 用 mock bus/repo 验证调度器接受这套装配。
func TestSchedulerAcceptsAssembledWorkers(t *testing.T) {
	c := &config.Scheduler{
		MaxConcurrent: 4, MaxPending: 64, ClaimBatch: 8,
		PollIntervalMS: 500, RetryDelayMS: 5000, OperationTimeoutMS: 10000, WorkerTimeoutMS: 30000,
		Channels: map[event.Channel]config.ChannelPolicy{
			event.ChLive: {Brain: "default", RatePerMinute: 6, Burst: 2, DailyQuota: 200, CooldownMS: 10000, MinRunes: 2},
		},
	}
	options := buildSchedulerOptions(c)
	byChannel, err := resolveRoutes(options.Channels, map[string]brain.Brain{"default": &fakeBrain{}})
	if err != nil {
		t.Fatalf("路由解析失败：%v", err)
	}
	workers, err := buildConsumeWorkers(persona.Pack{}, byChannel, &fakeSender{}, newFakeBus(), time.Second, nil, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("worker 装配失败：%v", err)
	}

	sched, err := scheduler.New(newFakeBus(), fakeSchedulerRepo{}, options, workers, zap.NewNop())
	if err != nil {
		t.Fatalf("调度器拒绝了这套装配：%v", err)
	}
	if sched == nil {
		t.Fatal("调度器为 nil")
	}
}

// senderTestConfig 造一份采集与发送脚本**故意不同**的配置，用来验证发送器没串到采集配置。
func senderTestConfig() config.Config {
	cfg := config.Config{
		Sender: &config.Sender{
			NodePath:      "/opt/node-sender",
			ScriptPath:    "tools/bili-whale/danmaku.mjs",
			SelfID:        "123456",
			HTTPTimeoutMS: 12000,
		},
	}
	cfg.Channels.Live.Enabled = true
	cfg.Collector.Live.NodePath = "node-collector"
	cfg.Collector.Live.ScriptPath = "tools/bili-whale/danmaku-await.mjs"
	cfg.Collector.Live.SelfID = "999999"
	cfg.Collector.Live.Cookie = "cookie-value"
	return cfg
}

// TestResolveSenderWiringUsesSenderSection 发送参数来自 [sender]，不是采集配置。
func TestResolveSenderWiringUsesSenderSection(t *testing.T) {
	cfg := senderTestConfig()

	wiring, err := resolveSenderWiring(cfg, 10*time.Second)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	if wiring.Node.ScriptPath != "tools/bili-whale/danmaku.mjs" {
		t.Errorf("发送脚本 = %q，期望 [sender] 里的 danmaku.mjs（不能用采集的 danmaku-await）", wiring.Node.ScriptPath)
	}
	if wiring.Node.NodePath != "/opt/node-sender" {
		t.Errorf("node 路径 = %q，期望 /opt/node-sender", wiring.Node.NodePath)
	}
	if wiring.Node.Timeout != 12*time.Second {
		t.Errorf("超时 = %v，期望 12s", wiring.Node.Timeout)
	}
	// 凭据复用采集侧（同一账号）
	if wiring.Node.Cookie != "cookie-value" {
		t.Errorf("cookie 未复用采集侧：%q", wiring.Node.Cookie)
	}
	// self_id 用 [sender] 的，不是采集的
	if wiring.Service.SelfID != "123456" {
		t.Errorf("SelfID = %q，期望 123456", wiring.Service.SelfID)
	}
	if !wiring.Service.Enabled {
		t.Error("live 开启时发送器应启用")
	}
	if wiring.Service.MinInterval != 10*time.Second {
		t.Errorf("MinInterval = %v，期望 10s", wiring.Service.MinInterval)
	}

	// 传进去的参数必须真的能构造出传输层
	if _, err := senderlive.NewNodeTransport(wiring.Node); err != nil {
		t.Fatalf("NodeTransport 拒绝了这组参数：%v", err)
	}
}

// TestResolveSenderWiringSelfIDFallback self_id 留空时回落到采集侧同一账号。
func TestResolveSenderWiringSelfIDFallback(t *testing.T) {
	cfg := senderTestConfig()
	cfg.Sender.SelfID = ""

	wiring, err := resolveSenderWiring(cfg, time.Second)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if wiring.Service.SelfID != "999999" {
		t.Errorf("SelfID = %q，期望回落到采集侧 999999", wiring.Service.SelfID)
	}
}

// TestResolveSenderWiringRequiresSenderSection 没有 [sender] 段要明确报错。
func TestResolveSenderWiringRequiresSenderSection(t *testing.T) {
	cfg := senderTestConfig()
	cfg.Sender = nil
	if _, err := resolveSenderWiring(cfg, time.Second); err == nil {
		t.Fatal("缺少 [sender] 段应报错")
	}
}

// newSettleWorkers 装配一套带假总线的 worker。
func newSettleWorkers(t *testing.T, engine brain.Brain, snd sender.Sender, bus event.Bus) scheduler.Workers {
	t.Helper()
	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	return workers
}

// TestGenerateSettlesToDoneOnSuccess 发送并核验成功 → 事件必须结清为 done。
//
// 顺序：queued→replying（进入执行中）→ replying→sent（投递完成）→ sent→done（核验通过）。
// 少任何一步，scheduler 的 ownSettlement 都会判「未结清」并把消费循环带崩。
func TestGenerateSettlesToDoneOnSuccess(t *testing.T) {
	const id = "live:30308220:9001"
	bus := newFakeBus(id)
	snd := &fakeSender{sendResult: sender.Result{OK: true, MsgID: "m1", Verified: true}}
	workers := newSettleWorkers(t, &fakeBrain{reply: brain.Reply{Text: "哼"}}, snd, bus)

	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	want := []event.Status{event.StatusReplying, event.StatusSent, event.StatusDone}
	if got := bus.markTos(); !slices.Equal(got, want) {
		t.Errorf("结清序列 = %v，期望 %v", got, want)
	}
	if got := bus.finalStatus(id); got != event.StatusDone {
		t.Errorf("最终状态 = %q，期望 done", got)
	}
}

// TestGenerateSettlesUnverifiedToRetry 投递成功但未核验 → 交回 retry，等 verify 阶段接手。
func TestGenerateSettlesUnverifiedToRetry(t *testing.T) {
	const id = "live:30308220:9002"
	bus := newFakeBus(id)
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: false}}
	workers := newSettleWorkers(t, &fakeBrain{reply: brain.Reply{Text: "哼"}}, snd, bus)

	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if got := bus.finalStatus(id); got != event.StatusRetry {
		t.Errorf("最终状态 = %q，期望 retry（未核验要留给 verify）", got)
	}
	if _, ok := bus.defers[id]; !ok {
		t.Error("未核验时应通过 Defer 交回重试")
	}
}

// TestGenerateSettlesBySenderKind 发送失败要按 sender.Kind 分流结清。
func TestGenerateSettlesBySenderKind(t *testing.T) {
	cases := []struct {
		name string
		kind sender.Kind
		want event.Status
	}{
		{"retry 类", sender.KindRetry, event.StatusRetry},
		{"dead 类", sender.KindDead, event.StatusDead},
		{"auth 类", sender.KindAuth, event.StatusDead},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "live:30308220:9003"
			bus := newFakeBus(id)
			snd := &fakeSender{sendErr: &sender.Error{Kind: tc.kind, Settled: false}}
			workers := newSettleWorkers(t, &fakeBrain{reply: brain.Reply{Text: "哼"}}, snd, bus)

			if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err == nil {
				t.Fatal("发送失败时应返回错误")
			}
			if got := bus.finalStatus(id); got != tc.want {
				t.Errorf("最终状态 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestGenerateSettlesBrainFailure 生成失败也不能把事件留在 replying。
func TestGenerateSettlesBrainFailure(t *testing.T) {
	const id = "live:30308220:9004"
	boom := errors.New("模型炸了")
	bus := newFakeBus(id)
	workers := newSettleWorkers(t, &fakeBrain{err: boom}, &fakeSender{}, bus)

	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); !errors.Is(err, boom) {
		t.Fatalf("Brain 错误未上抛：%v", err)
	}
	if got := bus.finalStatus(id); got != event.StatusDead {
		t.Errorf("最终状态 = %q，期望 dead（不能留 replying）", got)
	}
}

// TestVerifySettlesToDone 恢复路径核验通过 → done。
func TestVerifySettlesToDone(t *testing.T) {
	const id = "live:30308220:9005"
	bus := newFakeBus(id)
	snd := &fakeSender{resumeResult: sender.Result{OK: true, Verified: true}}
	workers := newSettleWorkers(t, &fakeBrain{}, snd, bus)

	claim := liveClaim(id)
	claim.Stage = event.StageVerify
	if _, err := workers.Verify(context.Background(), scheduler.Work{Claim: claim}); err != nil {
		t.Fatalf("Verify 失败：%v", err)
	}
	if got := bus.finalStatus(id); got != event.StatusDone {
		t.Errorf("最终状态 = %q，期望 done", got)
	}
}

// TestVerifySettlesFailureByKind 恢复失败同样按 Kind 结清，不留未结清状态。
func TestVerifySettlesFailureByKind(t *testing.T) {
	const id = "live:30308220:9006"
	boom := &sender.Error{Kind: sender.KindDead, Settled: false}
	bus := newFakeBus(id)
	workers := newSettleWorkers(t, &fakeBrain{}, &fakeSender{resumeErr: boom}, bus)

	claim := liveClaim(id)
	claim.Stage = event.StageVerify
	if _, err := workers.Verify(context.Background(), scheduler.Work{Claim: claim}); err == nil {
		t.Fatal("Resume 失败时应返回错误")
	}
	if got := bus.finalStatus(id); got != event.StatusDead {
		t.Errorf("最终状态 = %q，期望 dead", got)
	}
}

// TestQueuedToSentEdgeIsIllegal 记录一条事实：契约里没有 queued→sent 这条边。
//
// 所以「一次 Mark 直接把 queued 事件标成 sent」是做不到的，必须先进 replying。
func TestQueuedToSentEdgeIsIllegal(t *testing.T) {
	if err := store.ValidateTransition([]event.Status{event.StatusQueued}, event.StatusSent); err == nil {
		t.Fatal("queued→sent 竟然合法？状态机变了，需要重新核对结清流程")
	}
	// 规范里若把两个 from 一起传，同样会被拒（逐条校验）
	if err := store.ValidateTransition([]event.Status{event.StatusReplying, event.StatusQueued}, event.StatusSent); err == nil {
		t.Fatal("含 queued 的 from 集合仍应被拒")
	}
}

// TestDeliverStageIsHandled deliver 阶段领取的事件必须有 handler，且要能结清。
//
// 为什么单列一条：scheduler.launch 在 stage=deliver 时直接调 workers.Deliver，
// 为 nil 就是一次裸奔调用（panic），事件也永远结不清。
func TestDeliverStageIsHandled(t *testing.T) {
	const id = "live:30308220:9101"
	bus := newFakeBus(id)
	snd := &fakeSender{resumeResult: sender.Result{OK: true, Verified: true}}
	workers := newSettleWorkers(t, &fakeBrain{}, snd, bus)

	if workers.Deliver == nil {
		t.Fatal("Deliver handler 不能为 nil：deliver 阶段的事件会裸奔 panic")
	}

	claim := liveClaim(id)
	claim.Stage = event.StageDeliver
	if _, err := workers.Deliver(context.Background(), scheduler.Work{Claim: claim}); err != nil {
		t.Fatalf("Deliver 失败：%v", err)
	}
	if snd.resumeCalls != 1 {
		t.Errorf("deliver 阶段应走 Resume，实际调用 %d 次", snd.resumeCalls)
	}
	if snd.sendCalls != 0 {
		t.Errorf("deliver 阶段不应重新投递，实际 Send %d 次", snd.sendCalls)
	}
	if got := bus.finalStatus(id); got != event.StatusDone {
		t.Errorf("最终状态 = %q，期望 done", got)
	}
}

// TestDeliverStageSettlesFailure deliver 阶段失败同样要结清，不能留中间态。
func TestDeliverStageSettlesFailure(t *testing.T) {
	const id = "live:30308220:9102"
	bus := newFakeBus(id)
	workers := newSettleWorkers(t, &fakeBrain{}, &fakeSender{resumeErr: &sender.Error{Kind: sender.KindDead, Settled: false}}, bus)

	claim := liveClaim(id)
	claim.Stage = event.StageDeliver
	if _, err := workers.Deliver(context.Background(), scheduler.Work{Claim: claim}); err == nil {
		t.Fatal("Resume 失败时应返回错误")
	}
	if got := bus.finalStatus(id); got != event.StatusDead {
		t.Errorf("最终状态 = %q，期望 dead", got)
	}
}

// TestDeliverStageRetriesOnRetryable deliver 可重试失败 → retry（Defer 交回）。
func TestDeliverStageRetriesOnRetryable(t *testing.T) {
	const id = "live:30308220:9103"
	bus := newFakeBus(id)
	workers := newSettleWorkers(t, &fakeBrain{}, &fakeSender{resumeErr: &sender.Error{Kind: sender.KindRetry, Settled: false}}, bus)

	claim := liveClaim(id)
	claim.Stage = event.StageDeliver
	if _, err := workers.Deliver(context.Background(), scheduler.Work{Claim: claim}); err == nil {
		t.Fatal("Resume 失败时应返回错误")
	}
	if got := bus.finalStatus(id); got != event.StatusRetry {
		t.Errorf("最终状态 = %q，期望 retry", got)
	}
}

// fakeMemoryReader 记录被查询的 Scope 并返回可控结果。
type fakeMemoryReader struct {
	recallErr error
	sharedErr error
	ctx       memory.Context
	shared    memory.SharedState

	gotScope    memory.Scope
	recallCalls int
	sharedCalls int
}

func (f *fakeMemoryReader) Recall(_ context.Context, s memory.Scope) (memory.Context, error) {
	f.recallCalls++
	f.gotScope = s
	if f.recallErr != nil {
		return memory.Context{}, f.recallErr
	}
	return f.ctx, nil
}

func (f *fakeMemoryReader) Shared(_ context.Context, s memory.Scope) (memory.SharedState, error) {
	f.sharedCalls++
	f.gotScope = s
	if f.sharedErr != nil {
		return memory.SharedState{}, f.sharedErr
	}
	return f.shared, nil
}

// fakeKnowledgeReader 记录被查询的 Query 并返回可控结果。
type fakeKnowledgeReader struct {
	recallErr error
	ctx       knowledge.Context

	gotQuery    knowledge.Query
	recallCalls int
}

func (f *fakeKnowledgeReader) Recall(_ context.Context, q knowledge.Query) (knowledge.Context, error) {
	f.recallCalls++
	f.gotQuery = q
	if f.recallErr != nil {
		return knowledge.Context{}, f.recallErr
	}
	return f.ctx, nil
}

// TestGenerateRecallsMemoryWithCorrectScope 记忆召回按事件 Scope 查询，结果填进 ContextInput。
func TestGenerateRecallsMemoryWithCorrectScope(t *testing.T) {
	const id = "live:30308220:9201"
	bus := newFakeBus(id)
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	mem := &fakeMemoryReader{
		ctx:    memory.Context{Scope: memory.Scope{Channel: event.ChLive, Session: "30308220"}, Recent: []memory.Message{{EventID: "e1", Text: "早"}}},
		shared: memory.SharedState{Scope: memory.Scope{Channel: event.ChLive, Session: "30308220"}, Facts: []memory.Fact{{Key: "存粮", Value: "有"}}},
	}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, mem, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	claim := liveClaim(id)
	claim.Event.Text = "你还记得刚才说的吗"
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	wantScope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	if mem.recallCalls != 1 || mem.sharedCalls != 1 {
		t.Fatalf("Recall/Shared 调用次数 = %d/%d，期望 1/1", mem.recallCalls, mem.sharedCalls)
	}
	if mem.gotScope != wantScope {
		t.Errorf("召回 Scope = %+v，期望 %+v", mem.gotScope, wantScope)
	}
	if len(engine.gotInput.Memory.Recent) != 1 || engine.gotInput.Memory.Recent[0].Text != "早" {
		t.Errorf("Memory 未填回：%+v", engine.gotInput.Memory)
	}
	if len(engine.gotInput.Shared.Facts) != 1 || engine.gotInput.Shared.Facts[0].Key != "存粮" {
		t.Errorf("Shared 未填回：%+v", engine.gotInput.Shared)
	}
}

// TestGenerateMemoryRecallFailureDegrades 召回失败降级为空快照，且不阻塞回复。
func TestGenerateMemoryRecallFailureDegrades(t *testing.T) {
	const id = "live:30308220:9202"
	bus := newFakeBus(id)
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	mem := &fakeMemoryReader{recallErr: errors.New("db down"), sharedErr: errors.New("db down")}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, mem, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	claim := liveClaim(id)
	claim.Event.Text = "之前说过什么"
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask(id)}); err != nil {
		t.Fatalf("召回失败不应阻塞回复：%v", err)
	}

	// 降级为空快照，Scope 仍正确（Brain 需要它校验）
	if len(engine.gotInput.Memory.Recent) != 0 || len(engine.gotInput.Memory.Records) != 0 {
		t.Errorf("召回失败后 Memory 应为空：%+v", engine.gotInput.Memory)
	}
	if len(engine.gotInput.Shared.Facts) != 0 {
		t.Errorf("召回失败后 Shared 应为空：%+v", engine.gotInput.Shared)
	}
	if engine.gotInput.Memory.Scope.Channel != event.ChLive {
		t.Errorf("降级后 Scope 不应丢失：%+v", engine.gotInput.Memory.Scope)
	}
	if engine.calls != 1 {
		t.Errorf("Brain 应被调用 1 次，实际 %d", engine.calls)
	}
}

// TestGenerateKnowledgeEnabledFillsContext 知识 Reader 存在时填 ContextInput.Knowledge。
func TestGenerateKnowledgeEnabledFillsContext(t *testing.T) {
	const id = "live:30308220:9203"
	bus := newFakeBus(id)
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	know := &fakeKnowledgeReader{
		ctx: knowledge.Context{Scope: knowledge.Scope{Channel: event.ChLive, Session: "30308220"}, Revision: "rev1", Items: []knowledge.Item{{ID: "k1", Text: "资料"}}},
	}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, know, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	claim := liveClaim(id)
	claim.Event.Text = "这个梗什么意思"
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	if know.recallCalls != 1 {
		t.Fatalf("知识召回调用 = %d，期望 1", know.recallCalls)
	}
	if know.gotQuery.Text != "这个梗什么意思" {
		t.Errorf("知识查询正文 = %q，期望事件正文", know.gotQuery.Text)
	}
	if know.gotQuery.Scope.Channel != event.ChLive || know.gotQuery.Scope.Session != "30308220" {
		t.Errorf("知识查询 Scope = %+v", know.gotQuery.Scope)
	}
	if engine.gotInput.Knowledge == nil {
		t.Fatal("Knowledge 未填回")
	}
	if len(engine.gotInput.Knowledge.Items) != 1 || engine.gotInput.Knowledge.Items[0].ID != "k1" {
		t.Errorf("Knowledge 内容不符：%+v", engine.gotInput.Knowledge)
	}
}

// TestGenerateSkipsRecallForSimpleEvent 简单弹幕不查记忆/知识，Brain 仍拿到带 Scope 的空快照。
func TestGenerateSkipsRecallForSimpleEvent(t *testing.T) {
	const id = "live:30308220:9205"
	bus := newFakeBus(id)
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	mem := &fakeMemoryReader{
		ctx:    memory.Context{Scope: memory.Scope{Channel: event.ChLive, Session: "30308220"}, Recent: []memory.Message{{EventID: "e1", Text: "不该被召回"}}},
		shared: memory.SharedState{Scope: memory.Scope{Channel: event.ChLive, Session: "30308220"}, Facts: []memory.Fact{{Key: "不该", Value: "召回"}}},
	}
	know := &fakeKnowledgeReader{
		ctx: knowledge.Context{Scope: knowledge.Scope{Channel: event.ChLive, Session: "30308220"}, Items: []knowledge.Item{{ID: "k1", Text: "不该被召回"}}},
	}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, mem, know, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	if mem.recallCalls != 0 || mem.sharedCalls != 0 || know.recallCalls != 0 {
		t.Fatalf("简单弹幕不应召回：memory=%d/%d knowledge=%d", mem.recallCalls, mem.sharedCalls, know.recallCalls)
	}
	if engine.calls != 1 {
		t.Fatalf("Brain 调用 = %d，期望 1", engine.calls)
	}
	wantScope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	if engine.gotInput.Memory.Scope != wantScope || engine.gotInput.Shared.Scope != wantScope {
		t.Errorf("空快照 Scope 不符：%+v / %+v", engine.gotInput.Memory.Scope, engine.gotInput.Shared.Scope)
	}
	if len(engine.gotInput.Memory.Recent) != 0 || len(engine.gotInput.Shared.Facts) != 0 {
		t.Errorf("不需要召回时不应填入数据：%+v / %+v", engine.gotInput.Memory, engine.gotInput.Shared)
	}
	if engine.gotInput.Knowledge != nil {
		t.Errorf("不需要知识时 Knowledge 应为 nil：%+v", engine.gotInput.Knowledge)
	}
}

// TestGenerateRecallsBothForMixedQuestion 同时命中记忆与知识时，两路召回都要执行。
func TestGenerateRecallsBothForMixedQuestion(t *testing.T) {
	const id = "live:30308220:9206"
	bus := newFakeBus(id)
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	mem := &fakeMemoryReader{
		ctx:    memory.Context{Scope: memory.Scope{Channel: event.ChLive, Session: "30308220"}, Recent: []memory.Message{{EventID: "e1", Text: "早"}}},
		shared: memory.SharedState{Scope: memory.Scope{Channel: event.ChLive, Session: "30308220"}, Facts: []memory.Fact{{Key: "存粮", Value: "有"}}},
	}
	know := &fakeKnowledgeReader{
		ctx: knowledge.Context{Scope: knowledge.Scope{Channel: event.ChLive, Session: "30308220"}, Items: []knowledge.Item{{ID: "k1", Text: "资料"}}},
	}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, mem, know, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	claim := liveClaim(id)
	claim.Event.Text = "你还记得那个梗吗"
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	if mem.recallCalls != 1 || mem.sharedCalls != 1 || know.recallCalls != 1 {
		t.Fatalf("双命中召回次数不符：memory=%d/%d knowledge=%d", mem.recallCalls, mem.sharedCalls, know.recallCalls)
	}
	if engine.gotInput.Knowledge == nil || len(engine.gotInput.Knowledge.Items) != 1 {
		t.Errorf("Knowledge 未填回：%+v", engine.gotInput.Knowledge)
	}
	if len(engine.gotInput.Memory.Recent) != 1 || len(engine.gotInput.Shared.Facts) != 1 {
		t.Errorf("Memory/Shared 未填回：%+v / %+v", engine.gotInput.Memory, engine.gotInput.Shared)
	}
}

// TestGenerateLogsReasoningDecision reasoning 日志要带 action 与两个判断位。
func TestGenerateLogsReasoningDecision(t *testing.T) {
	const id = "live:30308220:9207"
	core, logs := observer.New(zap.InfoLevel)
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, newFakeBus(id), time.Second, nil, nil, nil, nil, zap.New(core))
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	claim := liveClaim(id)
	claim.Event.Text = "你还记得那个梗吗"
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	entries := logs.FilterField(zap.String("action", "reasoning")).All()
	if len(entries) != 1 {
		t.Fatalf("reasoning 日志条数 = %d，期望 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["need_memory"] != true || fields["need_knowledge"] != true {
		t.Errorf("日志判断位不符：%+v", fields)
	}
	reason, _ := fields["reason"].(string)
	if !strings.Contains(reason, "memory:") || !strings.Contains(reason, "knowledge:") {
		t.Errorf("日志 reason 不完整：%q", reason)
	}
}

// TestGenerateKnowledgeDisabledKeepsNil 知识 Reader 为 nil（未启用）→ ContextInput.Knowledge 保持 nil。
func TestGenerateKnowledgeDisabledKeepsNil(t *testing.T) {
	const id = "live:30308220:9204"
	bus := newFakeBus(id)
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if engine.gotInput.Knowledge != nil {
		t.Errorf("知识未启用时 Knowledge 应为 nil：%+v", engine.gotInput.Knowledge)
	}
}

// TestGeneratePresenceLifecycle 生成期间标 replying、投递成功后标 sending，返回前清掉 replying。
func TestGeneratePresenceLifecycle(t *testing.T) {
	const id = "live:30308220:9301"
	bus := newFakeBus(id)
	table := presence.NewTable()
	var during []presence.State
	engine := &fakeBrain{
		reply:   brain.Reply{Text: "哼"},
		onReply: func() { during = table.Active(time.Now()) },
	}
	snd := &fakeSender{sendResult: sender.Result{OK: true, MsgID: "m1", Verified: true}}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, table, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	if len(during) != 1 || during[0].Kind != presence.KindReplying || during[0].Channel != string(event.ChLive) {
		t.Fatalf("Brain 调用期间 presence = %+v，期望 replying/live", during)
	}

	active := table.Active(time.Now())
	if len(active) != 1 || active[0].Kind != presence.KindSending || active[0].Channel != string(event.ChLive) {
		t.Fatalf("Generate 返回后 presence = %+v，期望只剩 sending/live", active)
	}
	if active[0].TTL != time.Minute {
		t.Errorf("sending TTL = %v，期望 1m", active[0].TTL)
	}
	if got := table.Summary(time.Now()); got != "在发弹幕" {
		t.Errorf("Summary = %q，期望「在发弹幕」", got)
	}
}

// TestGeneratePresenceClearsReplyingOnBrainFailure 生成失败也要清掉 replying，不留悬挂状态。
func TestGeneratePresenceClearsReplyingOnBrainFailure(t *testing.T) {
	const id = "live:30308220:9302"
	bus := newFakeBus(id)
	table := presence.NewTable()
	var during []presence.State
	engine := &fakeBrain{
		err:     errors.New("模型炸了"),
		onReply: func() { during = table.Active(time.Now()) },
	}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, &fakeSender{}, bus, time.Second, nil, nil, table, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err == nil {
		t.Fatal("Brain 失败时应返回错误")
	}

	if len(during) != 1 || during[0].Kind != presence.KindReplying {
		t.Fatalf("Brain 调用期间 presence = %+v，期望 replying", during)
	}
	if got := table.Active(time.Now()); len(got) != 0 {
		t.Errorf("Generate 返回后 presence = %+v，期望空表", got)
	}
}

// TestBrainStatePresencePriority Presence 的实时房间优先于配置，且不把配置主播名串到别的房间。
func TestBrainStatePresencePriority(t *testing.T) {
	watch := config.Watch{CurrentRoom: "111", StreamerName: "甲"}
	got := brainState([]presence.State{{Kind: presence.KindWatching, Detail: "222"}}, watch)
	if !strings.Contains(got, "222") || strings.Contains(got, "111") || strings.Contains(got, "甲") {
		t.Fatalf("Presence 未优先或串台：%q", got)
	}
}

// TestBrainStateMatchesConfiguredStreamer 房间与配置一致时带上主播名。
func TestBrainStateMatchesConfiguredStreamer(t *testing.T) {
	watch := config.Watch{CurrentRoom: "1992099976", StreamerName: "娇娇"}
	active := []presence.State{{Kind: presence.KindWatching, Detail: "1992099976"}}
	want := "你正在看娇娇（房间 1992099976）的直播，你在她弹幕区发弹幕"
	if got := brainState(active, watch); got != want {
		t.Fatalf("状态摘要 = %q，期望 %q", got, want)
	}
}

// TestBrainStateConfigFallback 无 watching 时用配置兜底；无配置返回空。
func TestBrainStateConfigFallback(t *testing.T) {
	watch := config.Watch{CurrentRoom: "1992099976", StreamerName: "娇娇"}
	if got, want := brainState(nil, watch), "你正在看娇娇（房间 1992099976）的直播"; got != want {
		t.Fatalf("配置兜底 = %q，期望 %q", got, want)
	}
	if got := brainState(nil, config.Watch{}); got != "" {
		t.Fatalf("无配置兜底 = %q，期望空", got)
	}
	// replying/sending 不是观看状态，不能生成「在看」摘要。
	active := []presence.State{{Kind: presence.KindReplying, Detail: "1992099976"}}
	if got := brainState(active, config.Watch{}); got != "" {
		t.Fatalf("非 watching 状态生成了摘要：%q", got)
	}
	// 异常房间号不能经 Presence 进入 prompt，退回配置兜底。
	active = []presence.State{{Kind: presence.KindWatching, Detail: "bad<room>"}}
	if got, want := brainState(active, watch), "你正在看娇娇（房间 1992099976）的直播"; got != want {
		t.Fatalf("异常房间号未退回配置兜底：%q，期望 %q", got, want)
	}
}

// TestGenerateInjectsPresenceState Generate 把 Presence 的 watching 注入 Brain。
func TestGenerateInjectsPresenceState(t *testing.T) {
	const id = "live:30308220:9401"
	bus := newFakeBus(id)
	table := presence.NewTable()
	table.Set(presence.State{Kind: presence.KindWatching, Channel: string(event.ChLive), Detail: "1992099976", TTL: time.Hour})
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	watch := config.Watch{CurrentRoom: "1992099976", StreamerName: "娇娇"}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, table, brainStateSource(table, watch), zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if !strings.Contains(engine.gotInput.State, "娇娇") || !strings.Contains(engine.gotInput.State, "1992099976") {
		t.Fatalf("ContextInput.State = %q，期望含主播名与房间", engine.gotInput.State)
	}
	if !strings.Contains(engine.gotInput.State, "弹幕区") {
		t.Fatalf("ContextInput.State = %q，期望说明在弹幕区", engine.gotInput.State)
	}
}

// TestGenerateStateConfigFallback 无 Presence 时 Generate 仍能用配置兜底。
func TestGenerateStateConfigFallback(t *testing.T) {
	const id = "live:30308220:9402"
	bus := newFakeBus(id)
	table := presence.NewTable()
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	watch := config.Watch{CurrentRoom: "1992099976", StreamerName: "娇娇"}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, table, brainStateSource(table, watch), zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if want := "你正在看娇娇（房间 1992099976）的直播"; engine.gotInput.State != want {
		t.Fatalf("ContextInput.State = %q，期望 %q", engine.gotInput.State, want)
	}
}

// TestGenerateStateEmptyWithoutPresenceOrConfig 无状态来源时 State 保持空，不伪造身份。
func TestGenerateStateEmptyWithoutPresenceOrConfig(t *testing.T) {
	const id = "live:30308220:9403"
	bus := newFakeBus(id)
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, nil, brainStateSource(nil, config.Watch{}), zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if engine.gotInput.State != "" {
		t.Fatalf("无状态来源时 State = %q，期望空", engine.gotInput.State)
	}
}

// TestGeneratePresenceSkipsSendingWhenNotOK 发送未被接受（OK=false）不记 sending。
func TestGeneratePresenceSkipsSendingWhenNotOK(t *testing.T) {
	const id = "live:30308220:9303"
	bus := newFakeBus(id)
	table := presence.NewTable()
	snd := &fakeSender{sendResult: sender.Result{OK: false, Verified: false}}

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{reply: brain.Reply{Text: "哼"}}}, snd, bus, time.Second, nil, nil, table, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if got := table.Active(time.Now()); len(got) != 0 {
		t.Errorf("OK=false 时不应记 sending：%+v", got)
	}
}
