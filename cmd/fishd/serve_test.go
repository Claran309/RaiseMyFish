package main

// 本文件测 0.11 的消费循环装配：mock Brain/Sender 跑通 Claim→Generate→Sender→Result，
// 以及 Verify 走 Resume、路由缺失/任务缺失要报错、配置映射正确。
// 全部用例不调用真实模型、不连 PG。

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	senderlive "RaiseMyFish/internal/sender/live"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// fakeBrain 记录被调用时收到的 Task 与 ContextInput。
type fakeBrain struct {
	reply    brain.Reply
	err      error
	calls    int
	gotTask  brain.Task
	gotInput brain.ContextInput
}

func (f *fakeBrain) Name() string { return "fake" }

func (f *fakeBrain) Reply(_ context.Context, task brain.Task, in brain.ContextInput) (brain.Reply, error) {
	f.calls++
	f.gotTask = task
	f.gotInput = in
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

	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, newFakeBus(), time.Second, nil, nil, zap.NewNop())
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

// TestGenerateRejectsMissingTask 没有 Task 就报错，不伪造任务。
func TestGenerateRejectsMissingTask(t *testing.T) {
	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, newFakeBus(), time.Second, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim("live:1:1")}); err == nil {
		t.Fatal("Task 为 nil 时应报错")
	}
}

// TestGenerateRejectsUnknownChannel 通道没有路由时报错，不静默丢弃。
func TestGenerateRejectsUnknownChannel(t *testing.T) {
	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, newFakeBus(), time.Second, nil, nil, zap.NewNop())
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
	workers, _ := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, &fakeSender{}, newFakeBus(), time.Second, nil, nil, zap.NewNop())
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim("live:1:2"), Task: liveTask("live:1:2")}); !errors.Is(err, boom) {
		t.Errorf("Brain 错误未透传：%v", err)
	}

	snd := &fakeSender{sendErr: boom}
	workers2, _ := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{reply: brain.Reply{Text: "好"}}}, snd, newFakeBus(), time.Second, nil, nil, zap.NewNop())
	if _, err := workers2.Generate(context.Background(), scheduler.Work{Claim: liveClaim("live:1:3"), Task: liveTask("live:1:3")}); !errors.Is(err, boom) {
		t.Errorf("Sender 错误未透传：%v", err)
	}
}

// TestVerifyUsesResume 恢复路径只调 Resume，不重新生成。
func TestVerifyUsesResume(t *testing.T) {
	engine := &fakeBrain{}
	snd := &fakeSender{resumeResult: sender.Result{OK: true, Verified: true}}
	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, newFakeBus(), time.Second, nil, nil, zap.NewNop())
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
	if _, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, nil, newFakeBus(), time.Second, nil, nil, zap.NewNop()); err == nil {
		t.Error("Sender 为 nil 应报错")
	}
	if _, err := buildConsumeWorkers(persona.Pack{}, nil, &fakeSender{}, newFakeBus(), time.Second, nil, nil, zap.NewNop()); err == nil {
		t.Error("没有路由应报错")
	}
	if _, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, nil, time.Second, nil, nil, zap.NewNop()); err == nil {
		t.Error("Bus 为 nil 应报错")
	}
	if _, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, newFakeBus(), 0, nil, nil, zap.NewNop()); err == nil {
		t.Error("重试间隔非正应报错")
	}
	if _, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{}}, &fakeSender{}, newFakeBus(), time.Second, nil, nil, nil); err == nil {
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
	workers, err := buildConsumeWorkers(persona.Pack{}, byChannel, &fakeSender{}, newFakeBus(), time.Second, nil, nil, zap.NewNop())
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
	workers, err := buildConsumeWorkers(persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, zap.NewNop())
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
