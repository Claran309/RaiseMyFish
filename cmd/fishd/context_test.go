package main

// 本文件测统一上下文快照（C01/C04/T16）：
//   - 被动生成默认注入近期记忆，不再由 Decide 关键词决定；
//   - 档案读取失败与「陌生人」分开（失败不能伪装成没有档案）；
//   - 单步 Brain、ReAct 共用同一份快照，主动弹幕由 proactive 包用自己的用例覆盖。

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeViewerRepo 记录 GetProfile 的 uid 并返回可控结果。
type fakeViewerRepo struct {
	profile store.ViewerProfile
	err     error

	gotUID string
	calls  int
}

func (f *fakeViewerRepo) UpsertProfile(context.Context, store.ViewerProfile) error { return nil }

func (f *fakeViewerRepo) GetProfile(_ context.Context, uid string) (store.ViewerProfile, error) {
	f.calls++
	f.gotUID = uid
	if f.err != nil {
		return store.ViewerProfile{}, f.err
	}
	return f.profile, nil
}

func (f *fakeViewerRepo) TopViewers(context.Context, int) ([]store.ViewerProfile, error) {
	return nil, nil
}

// snapshotTestPack 是有场景口径的人格包：ContextText 需要能取到场景正文。
func snapshotTestPack() persona.Pack {
	return persona.Pack{
		Meta:     persona.Meta{Name: "测试鱼", Version: "v1", Channels: []string{persona.ContextLive}},
		Soul:     "我是测试鱼",
		Rules:    "只说真话",
		Samples:  "嗯哼",
		Contexts: map[string]string{persona.ContextLive: "你在直播间里陪观众聊天"},
	}
}

func snapshotTestEvent(text string) event.Event {
	return event.Event{
		ID: "live:30308220:9501", Channel: event.ChLive, Dir: event.DirIn,
		Session: "30308220", FromID: "42", FromName: "路人",
		Text: text, At: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), Status: event.StatusQueued,
	}
}

func snapshotTestMemory() (*fakeMemoryReader, memory.Message) {
	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	recent := memory.Message{EventID: "e-prev", Channel: event.ChLive, Session: "30308220", Dir: event.DirIn, FromID: "42", FromName: "路人", Text: "刚才聊到吃鱼", At: time.Date(2026, 11, 6, 11, 59, 0, 0, time.UTC)}
	return &fakeMemoryReader{
		ctx:    memory.Context{Scope: scope, Recent: []memory.Message{recent}},
		shared: memory.SharedState{Scope: scope, Facts: []memory.Fact{{Key: "存粮", Value: "有", Channel: event.ChLive, Session: "30308220", SourceID: "e-prev", At: recent.At}}},
	}, recent
}

// TestBuildPassiveInputAlwaysRecallsMemory C04：近期记忆默认注入，不靠关键词。
func TestBuildPassiveInputAlwaysRecallsMemory(t *testing.T) {
	pack := snapshotTestPack()
	mem, recent := snapshotTestMemory()
	know := &fakeKnowledgeReader{}
	builder := newContextBuilder(pack, mem, know, nil, nil, nil, zap.NewNop())

	input, _ := buildPassiveInput(context.Background(), builder, snapshotTestEvent("哈哈"))

	if mem.recallCalls != 1 || mem.sharedCalls != 1 {
		t.Fatalf("简单弹幕也必须召回记忆：memory=%d/%d", mem.recallCalls, mem.sharedCalls)
	}
	if len(input.Memory.Recent) != 1 || input.Memory.Recent[0].Text != recent.Text {
		t.Fatalf("近期记忆未注入：%+v", input.Memory)
	}
	if len(input.Shared.Facts) != 1 || input.Shared.Facts[0].Key != "存粮" {
		t.Fatalf("共享事实未注入：%+v", input.Shared)
	}
	if input.Memory.Scope != (memory.Scope{Channel: event.ChLive, Session: "30308220"}) {
		t.Fatalf("记忆 Scope 不符：%+v", input.Memory.Scope)
	}
	if know.recallCalls != 0 {
		t.Fatalf("简单弹幕不应召回知识：knowledge=%d", know.recallCalls)
	}
	if input.Visual != "" {
		t.Fatalf("被动路径不应有画面描述：%q", input.Visual)
	}
}

// TestViewerProfileLookupSeparatesNotFoundAndFailure C04：
// 陌生人（ErrNotFound）不注入；真实故障注入显式状态并记日志，不能伪装成陌生人。
func TestViewerProfileLookupSeparatesNotFoundAndFailure(t *testing.T) {
	profile := store.ViewerProfile{
		UID: "42", Name: "老熟人昵称", Hits: 12, Replies: 3,
		LastSeen:    time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		RecentTexts: []string{"摸摸"},
	}
	cases := []struct {
		name      string
		err       error
		wantText  string
		wantWarn  bool
		wantCheck func(t *testing.T, got string)
	}{
		{
			name: "有档案",
			wantCheck: func(t *testing.T, got string) {
				if !strings.Contains(got, "【对方档案 · 老熟人】") || !strings.Contains(got, "说过 12 次") || !strings.Contains(got, "摸摸") {
					t.Fatalf("档案片段不符：%q", got)
				}
			},
		},
		{
			name:     "陌生人",
			err:      fmt.Errorf("查档案：%w", store.ErrNotFound),
			wantText: "",
		},
		{
			name:     "读取失败",
			err:      errors.New("db down"),
			wantText: viewerUnavailableText,
			wantWarn: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			viewers := &fakeViewerRepo{profile: profile, err: tc.err}
			builder := newContextBuilder(snapshotTestPack(), nil, nil, viewers, nil, nil, zap.New(core))

			got := builder.viewerText(context.Background(), snapshotTestEvent("在吗"))

			if viewers.calls != 1 || viewers.gotUID != "42" {
				t.Fatalf("GetProfile 调用 = %d，uid=%q", viewers.calls, viewers.gotUID)
			}
			if tc.wantCheck != nil {
				tc.wantCheck(t, got)
			} else if got != tc.wantText {
				t.Fatalf("档案片段 = %q，期望 %q", got, tc.wantText)
			}
			warns := logs.FilterMessage("观众档案读取失败，本轮不注入档案").Len()
			if tc.wantWarn && warns != 1 {
				t.Fatalf("读取失败应记 1 条 Warn，实际 %d", warns)
			}
			if !tc.wantWarn && warns != 0 {
				t.Fatalf("陌生人/成功不应记故障日志，实际 %d", warns)
			}
		})
	}
}

// newSnapshotWorkers 用同一套依赖装配被动生成 worker。
func newSnapshotWorkers(t *testing.T, engine brain.Brain, mem memory.Reader, viewers store.ViewerRepo, soulPath string, state func(time.Time) string, react *reactRuntime) scheduler.Workers {
	t.Helper()
	workers, err := buildConsumeWorkers(
		scheduler.ChannelPolicy{}, snapshotTestPack(),
		map[event.Channel]brain.Brain{event.ChLive: engine},
		&fakeSender{sendResult: sender.Result{OK: true, Verified: true}},
		newFakeBus("live:30308220:9501"), time.Second,
		mem, nil, viewers, soulPath, nil, state, react, nil, zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	return workers
}

func writeSnapshotSoul(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "soul-memories.md")
	err := memory.WriteSoulAtomic(context.Background(), path, []memory.SoulMemory{{
		Title: "喜欢被摸头", Summary: "摸头会开心", Importance: 0.9,
		ValidFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatalf("写灵魂记忆失败：%v", err)
	}
	return path
}

// TestGeneratePassesUnifiedSnapshotToBrain 被动单步：档案/双方对话/状态/长期设定都进 Brain。
func TestGeneratePassesUnifiedSnapshotToBrain(t *testing.T) {
	engine := &fakeBrain{reply: brain.Reply{Text: "哼"}}
	mem, recent := snapshotTestMemory()
	viewers := &fakeViewerRepo{profile: store.ViewerProfile{
		UID: "42", Name: "老熟人昵称", Hits: 12, Replies: 3,
		LastSeen:    time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		RecentTexts: []string{"摸摸"},
	}}
	const stateText = "你正在看房间 30308220 的直播"
	soulPath := writeSnapshotSoul(t)

	workers := newSnapshotWorkers(t, engine, mem, viewers, soulPath, func(time.Time) string { return stateText }, nil)
	ev := snapshotTestEvent("哈哈")
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: event.Claim{Event: ev}, Task: liveTask(ev.ID)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	if engine.calls != 1 {
		t.Fatalf("Brain 调用 = %d，期望 1", engine.calls)
	}
	if len(engine.gotInput.Memory.Recent) != 1 || engine.gotInput.Memory.Recent[0].Text != recent.Text {
		t.Errorf("近期对话未注入：%+v", engine.gotInput.Memory)
	}
	if !strings.Contains(engine.gotInput.Viewer, "【对方档案 · 老熟人】") || !strings.Contains(engine.gotInput.Viewer, "摸摸") {
		t.Errorf("档案未注入：%q", engine.gotInput.Viewer)
	}
	if engine.gotInput.State != stateText {
		t.Errorf("状态 = %q，期望 %q", engine.gotInput.State, stateText)
	}
	if len(engine.gotInput.Soul) == 0 || !strings.Contains(strings.Join(engine.gotInput.Soul, "\n"), "喜欢被摸头") {
		t.Errorf("长期设定未注入：%+v", engine.gotInput.Soul)
	}
}

// capturingReactLLM 记录 ReAct 实际收到的 system/user。
type capturingReactLLM struct {
	system string
	user   string
	calls  int
}

func (c *capturingReactLLM) Complete(_ context.Context, system, user string) (string, error) {
	c.calls++
	c.system = system
	c.user = user
	return `{"thought":"够了","action":"final","final":"记得，刚才在聊吃鱼"}`, nil
}

func newCapturingReactRuntime(t *testing.T, llm *capturingReactLLM) *reactRuntime {
	t.Helper()
	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: reasoning.ToolHistory, Desc: "测试工具",
		Run: func(context.Context, string) (string, error) { return "弹幕一", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	factory := func(memory.Scope, string) (*reasoning.Agent, error) {
		return reasoning.New(llm, box, reasoning.Options{MaxSteps: 2, Timeout: 2 * time.Second}, zap.NewNop())
	}
	return &reactRuntime{factory: factory, defaultReact: true}
}

// TestGenerateReactUsesSameContextSnapshot C01/T16：
// ReAct 收到与单步 Brain 同源的统一快照（档案/双方对话/状态/长期设定/场景资料）。
func TestGenerateReactUsesSameContextSnapshot(t *testing.T) {
	engine := &fakeBrain{reply: brain.Reply{Text: "不该被调用"}}
	mem, recent := snapshotTestMemory()
	viewers := &fakeViewerRepo{profile: store.ViewerProfile{
		UID: "42", Name: "老熟人昵称", Hits: 12, Replies: 3,
		LastSeen:    time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		RecentTexts: []string{"摸摸"},
	}}
	const stateText = "你正在看房间 30308220 的直播"
	soulPath := writeSnapshotSoul(t)
	llm := &capturingReactLLM{}
	runtime := newCapturingReactRuntime(t, llm)

	workers := newSnapshotWorkers(t, engine, mem, viewers, soulPath, func(time.Time) string { return stateText }, runtime)
	ev := snapshotTestEvent("哈哈")
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: event.Claim{Event: ev}, Task: liveTask(ev.ID)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	if llm.calls != 1 {
		t.Fatalf("ReAct 模型调用 = %d，期望 1", llm.calls)
	}
	if engine.calls != 0 {
		t.Fatalf("走 ReAct 时不应调单步 Brain，实际 %d", engine.calls)
	}
	if !strings.HasPrefix(llm.system, snapshotTestPack().SystemPrompt()) || !strings.Contains(llm.system, "你在回答一条直播弹幕") {
		t.Errorf("ReAct system 不是「人格 prompt + 协议」")
	}
	for _, want := range []string{
		recent.Text,
		"【对方档案 · 老熟人】",
		"摸摸",
		"[当前状态] " + stateText,
		"喜欢被摸头",
		"你在直播间里陪观众聊天",
	} {
		if !strings.Contains(llm.user, want) {
			t.Errorf("ReAct 上下文缺少 %q：\n%s", want, llm.user)
		}
	}

	// 与单步 Brain 的 ContextText 完全同源：user 段以同一份快照开头。
	builder := newContextBuilder(snapshotTestPack(), mem, nil, viewers, newSoulCache(soulPath), func(time.Time) string { return stateText }, zap.NewNop())
	wantText, err := brain.ContextText(context.Background(), builder.BuildContext(context.Background(), ev, "", false))
	if err != nil {
		t.Fatalf("渲染统一快照失败：%v", err)
	}
	if !strings.HasPrefix(llm.user, strings.TrimSpace(wantText)) {
		t.Errorf("ReAct 的 Context 与统一快照不同源：\nwant prefix:\n%s\ngot:\n%s", wantText, llm.user)
	}
}
