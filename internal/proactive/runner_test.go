package proactive

// 本文件测重构后的 Runner：复用 Brain（断言收到 Visual）、gap、去重、15 字、失败路径。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// --- 假依赖 ---

type fakeSnapshotter struct {
	path  string
	err   error
	calls int
}

func (f *fakeSnapshotter) Snapshot(context.Context) (string, error) {
	f.calls++
	return f.path, f.err
}

type fakeVision struct {
	text  string
	err   error
	calls int
	got   string
}

func (f *fakeVision) Describe(_ context.Context, path string) (string, error) {
	f.calls++
	f.got = path
	return f.text, f.err
}

type fakeBrain struct {
	reply    brain.Reply
	err      error
	calls    int
	gotTask  brain.Task
	gotInput brain.ContextInput
}

func (f *fakeBrain) Name() string { return "fake-brain" }

func (f *fakeBrain) Reply(_ context.Context, task brain.Task, in brain.ContextInput) (brain.Reply, error) {
	f.calls++
	f.gotTask = task
	f.gotInput = in
	if f.err != nil {
		return brain.Reply{}, f.err
	}
	return f.reply, nil
}

type fakeSender struct {
	result sender.Result
	err    error
	calls  int
	got    string
}

func (f *fakeSender) Send(_ context.Context, text string) (sender.Result, error) {
	f.calls++
	f.got = text
	if f.err != nil {
		return sender.Result{}, f.err
	}
	return f.result, nil
}

type fakeMem struct {
	err   error
	got   memory.Scope
	calls int
}

func (f *fakeMem) Recall(_ context.Context, s memory.Scope) (memory.Context, error) {
	f.calls++
	f.got = s
	if f.err != nil {
		return memory.Context{}, f.err
	}
	return memory.Context{Scope: s}, nil
}

func (f *fakeMem) Shared(_ context.Context, s memory.Scope) (memory.SharedState, error) {
	if f.err != nil {
		return memory.SharedState{}, f.err
	}
	return memory.SharedState{Scope: s}, nil
}

func testPack() persona.Pack {
	return persona.Pack{
		Meta:    persona.Meta{Name: "大肥鱼", Version: "1"},
		Soul:    "你是大肥鱼",
		Rules:   "规则",
		Samples: "样本",
	}
}

type fixture struct {
	runner *Runner
	snap   *fakeSnapshotter
	vision *fakeVision
	engine *fakeBrain
	snd    *fakeSender
	clock  *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	f := &fixture{
		snap:   &fakeSnapshotter{path: t.TempDir() + "/frame.jpg"},
		vision: &fakeVision{text: "画面里有一个人坐在桌前啃面包，背景是游戏直播界面。"},
		engine: &fakeBrain{reply: brain.Reply{Text: "在吃面包呢"}},
		snd:    &fakeSender{result: sender.Result{OK: true, Verified: true}},
		clock:  &clock,
	}
	opts := Options{SendGap: 30 * time.Second, RoomID: "30308220"}
	opts.Now = func() time.Time { return *f.clock }
	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, nil, f.snd, opts)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	f.runner = runner
	return f
}

// --- 用例 ---

// TestRunOnceReusesBrainWithVisual 核心用例：画面描述进 ContextInput.Visual，走 Brain 管线。
func TestRunOnceReusesBrainWithVisual(t *testing.T) {
	f := newFixture(t)

	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if !got.Sent || got.Text != "在吃面包呢" {
		t.Fatalf("结果不符：%+v", got)
	}

	if f.engine.calls != 1 {
		t.Fatalf("必须复用 Brain 生成（调用 %d 次）", f.engine.calls)
	}
	if f.engine.gotInput.Visual != f.vision.text {
		t.Errorf("Visual 未注入：%q", f.engine.gotInput.Visual)
	}
	if strings.TrimSpace(f.engine.gotInput.Persona.Soul) == "" {
		t.Error("人格包未传入")
	}
	// 合成事件：Brain 要求 ID/Text/Session 齐全
	ev := f.engine.gotInput.Event
	if !strings.HasPrefix(ev.ID, "proactive:30308220:") {
		t.Errorf("合成事件 ID 不符：%q", ev.ID)
	}
	if ev.Channel != event.ChLive || ev.Dir != event.DirIn || ev.Session != "30308220" {
		t.Errorf("合成事件字段不符：%+v", ev)
	}
	if strings.TrimSpace(ev.Text) != SyntheticEventText {
		t.Errorf("合成事件正文应为占位文本，实际 %q", ev.Text)
	}
	if f.engine.gotTask.EventID != ev.ID || f.engine.gotTask.Channel != ev.Channel {
		t.Errorf("Task 与事件不一致：%+v / %+v", f.engine.gotTask, ev)
	}
	// 记忆 Scope 必须与事件一致
	if f.engine.gotInput.Memory.Scope != (memory.Scope{Channel: event.ChLive, Session: "30308220"}) {
		t.Errorf("记忆 Scope 不符：%+v", f.engine.gotInput.Memory.Scope)
	}

	if f.vision.got != f.snap.path {
		t.Errorf("识图用的不是截帧：%q vs %q", f.vision.got, f.snap.path)
	}
	if f.snd.got != "在吃面包呢" {
		t.Errorf("发送正文 = %q", f.snd.got)
	}
}

// TestRunOnceTruncatesTo15Runes 发送前截到 15 字。
func TestRunOnceTruncatesTo15Runes(t *testing.T) {
	f := newFixture(t)
	f.engine.reply = brain.Reply{Text: "这条弹幕特别特别特别特别特别长超过十五个字了"}

	if _, err := f.runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if n := len([]rune(f.snd.got)); n > MaxDanmakuRunes {
		t.Errorf("发送正文 %d 字，超过 %d", n, MaxDanmakuRunes)
	}
}

// TestRunOnceGapSkips 间隔内直接跳过，不截帧、不识图、不调 Brain。
func TestRunOnceGapSkips(t *testing.T) {
	f := newFixture(t)
	if _, err := f.runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("首轮失败：%v", err)
	}
	*f.clock = f.clock.Add(10 * time.Second)

	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if got.Sent || got.Reason != ReasonGap {
		t.Fatalf("间隔内应跳过：%+v", got)
	}
	if f.snap.calls != 1 || f.vision.calls != 1 || f.engine.calls != 1 {
		t.Errorf("跳过时不应再调用下游：snap=%d vision=%d brain=%d", f.snap.calls, f.vision.calls, f.engine.calls)
	}
}

// TestRunOnceDedupSimilar 相似度 ≥0.75 不重复发。
func TestRunOnceDedupSimilar(t *testing.T) {
	f := newFixture(t)
	if _, err := f.runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("首轮失败：%v", err)
	}
	*f.clock = f.clock.Add(time.Minute)

	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if got.Reason != ReasonSimilar {
		t.Fatalf("相同内容应被去重：%+v", got)
	}
	if f.snd.calls != 1 {
		t.Errorf("只应发送一次，实际 %d", f.snd.calls)
	}
}

// TestRunOnceVisionFailure 识图失败 → 本轮报错，不发送。
func TestRunOnceVisionFailure(t *testing.T) {
	f := newFixture(t)
	f.vision.err = errors.New("vision down")

	if _, err := f.runner.RunOnce(context.Background()); err == nil {
		t.Fatal("识图失败应报错")
	}
	if f.engine.calls != 0 || f.snd.calls != 0 {
		t.Errorf("识图失败不应继续：brain=%d send=%d", f.engine.calls, f.snd.calls)
	}
}

// TestRunOnceEmptyVisual 画面描述为空 → 报错（不拿空上下文骗 Brain）。
func TestRunOnceEmptyVisual(t *testing.T) {
	f := newFixture(t)
	f.vision.text = "   "

	if _, err := f.runner.RunOnce(context.Background()); err == nil {
		t.Fatal("空描述应报错")
	}
	if f.snd.calls != 0 {
		t.Error("空描述不应发送")
	}
}

// TestRunOnceBrainFailure Brain 失败 → 报错、不发送。
func TestRunOnceBrainFailure(t *testing.T) {
	f := newFixture(t)
	f.engine.err = errors.New("brain down")

	if _, err := f.runner.RunOnce(context.Background()); err == nil {
		t.Fatal("Brain 失败应报错")
	}
	if f.snd.calls != 0 {
		t.Error("Brain 失败不应发送")
	}
}

// TestRunOnceSendNotAccepted 平台未接受 → Reason=send_not_accepted，不算 Sent。
func TestRunOnceSendNotAccepted(t *testing.T) {
	f := newFixture(t)
	f.snd.result = sender.Result{OK: false, Error: "content"}

	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("未被接受不算错误：%v", err)
	}
	if got.Sent || got.Reason != "send_not_accepted" {
		t.Fatalf("结果不符：%+v", got)
	}
}

// TestRunOnceMemoryRecallDegrades 记忆召回失败降级为空快照，仍能发送。
func TestRunOnceMemoryRecallDegrades(t *testing.T) {
	f := newFixture(t)
	mem := &fakeMem{err: errors.New("db down")}
	clock := f.clock
	runner, err := New(f.snap, f.vision, f.engine, testPack(), mem, nil, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Now: func() time.Time { return *clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("记忆失败不应阻塞：%v", err)
	}
	if !got.Sent {
		t.Errorf("应照常发送：%+v", got)
	}
	if mem.got != (memory.Scope{Channel: event.ChLive, Session: "30308220"}) {
		t.Errorf("记忆 Scope 不符：%+v", mem.got)
	}
	if len(f.engine.gotInput.Memory.Recent) != 0 {
		t.Errorf("降级后记忆应为空：%+v", f.engine.gotInput.Memory)
	}
}

// TestNewRejectsMissingDeps 依赖/参数缺失在构造期失败。
func TestNewRejectsMissingDeps(t *testing.T) {
	snap := &fakeSnapshotter{path: "x"}
	vision := &fakeVision{}
	engine := &fakeBrain{}
	snd := &fakeSender{}
	opts := Options{RoomID: "1"}

	if _, err := New(nil, vision, engine, testPack(), nil, nil, snd, opts); err == nil {
		t.Error("缺截帧应报错")
	}
	if _, err := New(snap, nil, engine, testPack(), nil, nil, snd, opts); err == nil {
		t.Error("缺识图应报错")
	}
	if _, err := New(snap, vision, nil, testPack(), nil, nil, snd, opts); err == nil {
		t.Error("缺 Brain 应报错")
	}
	if _, err := New(snap, vision, engine, persona.Pack{}, nil, nil, snd, opts); err == nil {
		t.Error("缺人格应报错")
	}
	if _, err := New(snap, vision, engine, testPack(), nil, nil, nil, opts); err == nil {
		t.Error("缺发送应报错")
	}
	if _, err := New(snap, vision, engine, testPack(), nil, nil, snd, Options{}); err == nil {
		t.Error("缺房间号应报错")
	}
	if _, err := New(snap, vision, engine, testPack(), nil, nil, snd, Options{RoomID: "1", SendGap: -time.Second}); err == nil {
		t.Error("负 gap 应报错")
	}
}

// ---------- ReAct 分流 ----------

// stubLLM 是 ReAct 用的假模型：固定回放一条 final，或固定报错。
type stubLLM struct {
	reply string
	err   error
	calls int
}

func (s *stubLLM) Complete(_ context.Context, _, _ string) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

// reactFixture 造一个「真 Agent + 假模型」的工厂，并记录工厂被调次数。
func reactFixture(t *testing.T, llm *stubLLM) (ReactFactory, *int) {
	t.Helper()
	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: reasoning.ToolHistory, Desc: "测试工具",
		Run: func(context.Context, string) (string, error) { return "弹幕一", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	factory := func(_ memory.Scope) (*reasoning.Agent, error) {
		factoryCalls++
		agent, err := reasoning.New(llm, box, reasoning.Options{MaxSteps: 3, Timeout: 2 * time.Second}, zap.NewNop())
		return agent, err
	}
	return factory, &factoryCalls
}

// TestRunOnceUsesReactWhenVisualHintsFact 画面里出现「梗」→ 走 ReAct，单步 Brain 不被调用。
func TestRunOnceUsesReactWhenVisualHintsFact(t *testing.T) {
	f := newFixture(t)
	f.vision.text = "画面里的人正在讲解一个弹幕梗，笑得很开心。"
	llm := &stubLLM{reply: `{"thought":"懂了","action":"final","final":"这梗笑死"}`}
	factory, factoryCalls := reactFixture(t, llm)

	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, factory, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if *factoryCalls != 1 {
		t.Errorf("ReAct 工厂调用 %d 次，期望 1", *factoryCalls)
	}
	if llm.calls == 0 {
		t.Error("ReAct 模型未被调用")
	}
	if f.engine.calls != 0 {
		t.Errorf("走 ReAct 时不应再调单步 Brain，实际 %d 次", f.engine.calls)
	}
	if !got.React || !got.Sent || got.Text != "这梗笑死" {
		t.Errorf("结果不符：%+v", got)
	}
	if f.snd.got != "这梗笑死" {
		t.Errorf("发送正文 = %q", f.snd.got)
	}
}

// TestRunOnceSkipsReactWithoutHint 画面没有事实线索 → 单步，ReAct 工厂不被调用。
func TestRunOnceSkipsReactWithoutHint(t *testing.T) {
	f := newFixture(t)
	f.vision.text = "画面里一个人安静地坐着喝茶。"
	llm := &stubLLM{reply: `{"thought":"t","action":"final","final":"不该用"}`}
	factory, factoryCalls := reactFixture(t, llm)

	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, factory, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if *factoryCalls != 0 || llm.calls != 0 {
		t.Errorf("简单场景不应进 ReAct：factory=%d llm=%d", *factoryCalls, llm.calls)
	}
	if f.engine.calls != 1 {
		t.Errorf("应走单步 1 次，实际 %d", f.engine.calls)
	}
	if got.React || !got.Sent {
		t.Errorf("结果不符：%+v", got)
	}
}

// TestRunOnceReactFactoryErrorFallsBack 工厂报错 → 回落单步，弹幕照发。
func TestRunOnceReactFactoryErrorFallsBack(t *testing.T) {
	f := newFixture(t)
	f.vision.text = "画面里的人在讲之前说过的那个梗。"
	boom := errors.New("工具装配失败")
	factory := ReactFactory(func(memory.Scope) (*reasoning.Agent, error) { return nil, boom })

	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, factory, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("ReAct 失败不应阻塞：%v", err)
	}
	if f.engine.calls != 1 {
		t.Errorf("应回落单步 1 次，实际 %d", f.engine.calls)
	}
	if got.React || !got.Sent || f.snd.got != "在吃面包呢" {
		t.Errorf("结果不符：%+v / %q", got, f.snd.got)
	}
}

// TestRunOnceReactLLMErrorFallsBack ReAct 模型报错 → 降级后仍为空 → 回落单步。
func TestRunOnceReactLLMErrorFallsBack(t *testing.T) {
	f := newFixture(t)
	f.vision.text = "画面里的人在提刚才那句话。"
	llm := &stubLLM{err: errors.New("model down")}
	factory, factoryCalls := reactFixture(t, llm)

	runner, err := New(f.snap, f.vision, f.engine, testPack(), nil, factory, f.snd, Options{
		SendGap: 30 * time.Second, RoomID: "30308220",
		Now: func() time.Time { return *f.clock },
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	got, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if *factoryCalls != 1 {
		t.Errorf("应尝试过 ReAct：%d", *factoryCalls)
	}
	if f.engine.calls != 1 {
		t.Errorf("模型失败后应回落单步，实际 brain=%d", f.engine.calls)
	}
	if got.React {
		t.Error("回落单步时 React 标记应为 false")
	}
}

// TestRunOnceNilReactAlwaysSingleStep 没装 ReactFactory 时，即使画面有线索也走单步。
func TestRunOnceNilReactAlwaysSingleStep(t *testing.T) {
	f := newFixture(t)
	f.vision.text = "画面里的人在讲一个梗。"

	got, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if got.React {
		t.Error("未装配 ReAct 时不应标记 React")
	}
	if f.engine.calls != 1 {
		t.Errorf("应走单步 1 次，实际 %d", f.engine.calls)
	}
}
