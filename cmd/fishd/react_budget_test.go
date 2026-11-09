package main

// 本文件测 B2/N03 的三条修复：
//   - 知识预召回命中后不再注册 knowledge_search（避免重复召回）；
//   - ReAct 工具查证后超时，回落单步仍拿到证据；
//   - ReAct 整轮预算为最终正文预留一段时间。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// finalLLM 只回一条 final，用来观察 Agent 实际看到的工具清单。
type finalLLM struct {
	mu      sync.Mutex
	systems []string
}

func (l *finalLLM) Complete(_ context.Context, system, _ string) (string, error) {
	l.mu.Lock()
	l.systems = append(l.systems, system)
	l.mu.Unlock()
	return `{"thought":"够了","action":"final","final":"好"}`, nil
}

func (l *finalLLM) lastSystem() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.systems) == 0 {
		return ""
	}
	return l.systems[len(l.systems)-1]
}

// TestReasonerFactorySkipsKnowledgeToolWhenPreloaded 预召回命中 → 工具清单里没有 knowledge_search；
// 未预召回 → 工具保留，模型仍可主动查。
func TestReasonerFactorySkipsKnowledgeToolWhenPreloaded(t *testing.T) {
	var mu sync.Mutex
	var systems []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) > 0 {
			mu.Lock()
			systems = append(systems, req.Messages[0].Content)
			mu.Unlock()
		}
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, `{"thought":"够了","action":"final","final":"好"}`)
	}))
	defer server.Close()

	cfg := config.Config{
		LLM: config.LLM{
			Enabled: true, MaxPromptTokens: 8192, MaxReplyTokens: 128,
			AttemptTimeoutMS: 1000, TotalTimeoutMS: 5000, BackoffMS: 1, MaxConcurrent: 1, Temperature: .7,
			Providers: map[string]config.Provider{"fake": {Type: "openai", BaseURL: server.URL, APIKey: "unit-only", Models: []string{"model"}}},
			Routing:   map[string]string{"default": "fake:model"},
		},
		Reasoning: config.Reasoning{Enabled: true, MaxSteps: 2, TimeoutMS: 2000},
	}
	know := &fakeKnowledgeReader{ctx: knowledge.Context{
		Scope:    knowledge.Scope{Channel: event.ChLive, Session: "30308220"},
		Revision: strings.Repeat("a", 64),
		Items:    []knowledge.Item{{ID: "k1", Source: "test:public", Text: "拉拉是梗", Scenes: []string{"live"}, Priority: 50}},
	}}
	factory, err := buildReasonerFactory(cfg, nil, know, nil, nil, nil, nil, toolExecutors{}, nil, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if factory == nil {
		t.Fatal("ReAct 未启用")
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	run := func(preloaded bool) string {
		t.Helper()
		agent, err := factory(scope, "", preloaded)
		if err != nil {
			t.Fatalf("创建 agent 失败：%v", err)
		}
		if _, err := agent.Run(context.Background(), reasoning.Input{System: "人设", Question: "那个梗是什么意思"}); err != nil {
			t.Fatalf("执行失败：%v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		return systems[len(systems)-1]
	}

	preloaded := run(true)
	if strings.Contains(preloaded, reasoning.ToolKnowledgeSearch) {
		t.Fatalf("知识已预召回，工具清单仍注册了 %s：\n%s", reasoning.ToolKnowledgeSearch, preloaded)
	}
	notPreloaded := run(false)
	if !strings.Contains(notPreloaded, reasoning.ToolKnowledgeSearch) {
		t.Fatalf("零命中/未预召回时应保留 %s 工具：\n%s", reasoning.ToolKnowledgeSearch, notPreloaded)
	}
}

// timeoutAfterToolLLM 第一次回工具步，之后卡到 ctx 超时，模拟「查证后超时」。
type timeoutAfterToolLLM struct {
	calls int
}

func (l *timeoutAfterToolLLM) Complete(ctx context.Context, _, _ string) (string, error) {
	l.calls++
	if l.calls == 1 {
		return `{"thought":"查历史","action":"history","args":"5"}`, nil
	}
	<-ctx.Done()
	return "", ctx.Err()
}

// TestReactTimeoutFallbackKeepsEvidence B2/N03 验收：
// 工具查证后超时 → ReAct 正文为空 → 回落单步的 Brain 输入仍带 tool_evidence。
func TestReactTimeoutFallbackKeepsEvidence(t *testing.T) {
	const id = "live:30308220:9501"
	ev := snapshotTestEvent("刚才聊什么了")
	bus := newFakeBus(id)
	engine := &fakeBrain{reply: brain.Reply{Text: "单步兜底"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	mem, _ := snapshotTestMemory()

	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: reasoning.ToolHistory, Desc: "测试工具",
		Run: func(context.Context, string) (string, error) { return "刚才在聊吃鱼", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := &timeoutAfterToolLLM{}
	runtime := &reactRuntime{
		factory: func(memory.Scope, string, bool) (*reasoning.Agent, error) {
			return reasoning.New(llm, box, reasoning.Options{MaxSteps: 3, Timeout: 40 * time.Millisecond}, zap.NewNop())
		},
		defaultReact: true,
	}
	workers, err := buildConsumeWorkers(scheduler.ChannelPolicy{}, snapshotTestPack(), map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, mem, nil, nil, "", nil, nil, runtime, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: event.Claim{Event: ev}, Task: liveTask(ev.ID)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	if llm.calls != 2 {
		t.Fatalf("ReAct 模型调用 %d 次，期望 2（工具 + 超时那次）", llm.calls)
	}
	if engine.calls != 1 || snd.gotReply.Text != "单步兜底" {
		t.Fatalf("未回落单步：brain=%d text=%q", engine.calls, snd.gotReply.Text)
	}
	if len(engine.gotInput.Evidence) != 1 {
		t.Fatalf("回落输入缺少 ReAct 证据：%+v", engine.gotInput.Evidence)
	}
	if got := engine.gotInput.Evidence[0]; got.Tool != reasoning.ToolHistory || got.Args != "5" || !strings.Contains(got.Result, "吃鱼") {
		t.Fatalf("回落证据不符：%+v", got)
	}
}

// TestGeneratePassesKnowledgePreloadedToFactory 预召回命中时工厂收到 true，零命中收到 false。
func TestGeneratePassesKnowledgePreloadedToFactory(t *testing.T) {
	newRuntime := func(got *bool) (*reactRuntime, *finalLLM) {
		llm := &finalLLM{}
		box, err := reasoning.NewToolbox(reasoning.Tool{
			Name: reasoning.ToolHistory, Desc: "测试工具",
			Run: func(context.Context, string) (string, error) { return "弹幕一", nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		factory := func(_ memory.Scope, _ string, preloaded bool) (*reasoning.Agent, error) {
			*got = preloaded
			return reasoning.New(llm, box, reasoning.Options{MaxSteps: 2, Timeout: time.Second}, zap.NewNop())
		}
		return &reactRuntime{factory: factory, defaultReact: true, budget: brain.Budget{MaxPromptTokens: 8192, KnowledgeBudgetPercent: 10}}, llm
	}

	cases := []struct {
		name string
		know knowledge.Context
		want bool
	}{
		{
			name: "命中预召回",
			know: knowledge.Context{
				Scope:    knowledge.Scope{Channel: event.ChLive, Session: "30308220"},
				Revision: strings.Repeat("a", 64),
				Items:    []knowledge.Item{{ID: "k1", Source: "test:public", Text: "拉拉是梗", Scenes: []string{"live"}, Priority: 50}},
			},
			want: true,
		},
		{name: "零命中", know: knowledge.Context{Scope: knowledge.Scope{Channel: event.ChLive, Session: "30308220"}, Revision: strings.Repeat("a", 64)}, want: false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got bool
			runtime, _ := newRuntime(&got)
			reader := &fakeKnowledgeReader{ctx: tc.know}
			id := fmt.Sprintf("live:30308220:96%02d", i)
			ev := snapshotTestEvent("那个梗是什么意思")
			ev.ID = id
			bus := newFakeBus(id)
			engine := &fakeBrain{reply: brain.Reply{Text: "不该被调用"}}
			snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
			mem, _ := snapshotTestMemory()
			workers, err := buildConsumeWorkers(scheduler.ChannelPolicy{}, snapshotTestPack(), map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, mem, reader, nil, "", nil, nil, runtime, nil, zap.NewNop())
			if err != nil {
				t.Fatalf("装配失败：%v", err)
			}
			if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: event.Claim{Event: ev}, Task: liveTask(ev.ID)}); err != nil {
				t.Fatalf("Generate 失败：%v", err)
			}
			if reader.recallCalls != 1 {
				t.Fatalf("知识预召回次数 = %d，期望 1", reader.recallCalls)
			}
			if got != tc.want {
				t.Fatalf("工厂收到 knowledgePreloaded=%v，期望 %v", got, tc.want)
			}
			if engine.calls != 0 {
				t.Fatalf("ReAct 成功时不应回落单步，实际 %d", engine.calls)
			}
		})
	}
}

// deadlineMemory 记录召回时看到的 ctx deadline，用来验证准备段预算真的接上了。
type deadlineMemory struct {
	mu         sync.Mutex
	recallDL   time.Time
	sharedDL   time.Time
	recallSeen bool
	sharedSeen bool
}

func (m *deadlineMemory) Recall(ctx context.Context, s memory.Scope) (memory.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallDL, m.recallSeen = ctx.Deadline()
	return memory.Context{Scope: s}, nil
}

func (m *deadlineMemory) Shared(ctx context.Context, s memory.Scope) (memory.SharedState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sharedDL, m.sharedSeen = ctx.Deadline()
	return memory.SharedState{Scope: s}, nil
}

func (m *deadlineMemory) deadlines() (recall, shared time.Time, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recallDL, m.sharedDL, m.recallSeen && m.sharedSeen
}

// deadlineBrain 记录回落单步时看到的 ctx deadline。
type deadlineBrain struct {
	mu    sync.Mutex
	dl    time.Time
	seen  bool
	calls int
}

func (b *deadlineBrain) Name() string { return "deadline" }

func (b *deadlineBrain) Reply(ctx context.Context, _ brain.Task, _ brain.ContextInput) (brain.Reply, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	b.dl, b.seen = ctx.Deadline()
	return brain.Reply{Text: "单步兜底"}, nil
}

func (b *deadlineBrain) deadline() (time.Time, bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dl, b.seen, b.calls
}

// TestGeneratePrepBudgetAndFallbackUseParentDeadline 整轮预算接线：
// 读取/召回跑在「父 deadline 减 finalReserve」的 prepCtx 上；
// ReAct 超时回落后，单步 Brain 拿回父 ctx，保留最终正文时间。
func TestGeneratePrepBudgetAndFallbackUseParentDeadline(t *testing.T) {
	const id = "live:30308220:9701"
	ev := snapshotTestEvent("刚才聊什么了")
	bus := newFakeBus(id)
	engine := &deadlineBrain{}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	mem := &deadlineMemory{}

	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: reasoning.ToolHistory, Desc: "测试工具",
		Run: func(context.Context, string) (string, error) { return "刚才在聊吃鱼", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := &timeoutAfterToolLLM{}
	const reserve = 400 * time.Millisecond
	runtime := &reactRuntime{
		factory: func(memory.Scope, string, bool) (*reasoning.Agent, error) {
			return reasoning.New(llm, box, reasoning.Options{MaxSteps: 3, Timeout: 30 * time.Millisecond}, zap.NewNop())
		},
		defaultReact: true,
		finalReserve: reserve,
	}
	workers, err := buildConsumeWorkers(scheduler.ChannelPolicy{}, snapshotTestPack(), map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, mem, nil, nil, "", nil, nil, runtime, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	parentDeadline := time.Now().Add(2 * time.Second)
	parent, cancel := context.WithDeadline(context.Background(), parentDeadline)
	defer cancel()
	if _, err := workers.Generate(parent, scheduler.Work{Claim: event.Claim{Event: ev}, Task: liveTask(ev.ID)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}

	recallDL, sharedDL, ok := mem.deadlines()
	if !ok {
		t.Fatal("未观察到记忆召回，预算接线测试无效")
	}
	wantPrep := parentDeadline.Add(-reserve)
	if !recallDL.Equal(wantPrep) || !sharedDL.Equal(wantPrep) {
		t.Fatalf("记忆召回未跑在准备段预算上：recall=%v shared=%v 期望=%v", recallDL, sharedDL, wantPrep)
	}

	brainDL, seen, calls := engine.deadline()
	if !seen || calls != 1 {
		t.Fatalf("未回落单步：seen=%v calls=%d", seen, calls)
	}
	if !brainDL.Equal(parentDeadline) {
		t.Fatalf("回落单步未拿回父 ctx：deadline=%v 期望=%v", brainDL, parentDeadline)
	}
	if !brainDL.After(recallDL) {
		t.Fatalf("最终正文段未预留：brain=%v recall=%v", brainDL, recallDL)
	}
}

// TestPrepareDeadlineReservesFinalSegment 整轮预算：准备段在父 deadline 前停，
// 留出的时间不少于配置的最终正文预留；预留过大时不超过剩余时间一半。
func TestPrepareDeadlineReservesFinalSegment(t *testing.T) {
	parentDeadline := time.Now().Add(time.Hour)
	parent, cancel := context.WithDeadline(context.Background(), parentDeadline)
	defer cancel()

	prep, stop := prepareDeadline(parent, 10*time.Minute)
	defer stop()
	got, ok := prep.Deadline()
	if !ok {
		t.Fatal("准备 ctx 应带 deadline")
	}
	if diff := got.Sub(parentDeadline); diff > -10*time.Minute+time.Second || diff < -10*time.Minute-time.Second {
		t.Fatalf("准备段 deadline 偏移 %v，期望约 -10m", diff)
	}

	// 预留超过剩余一半：按一半封顶，准备阶段仍有时间。
	capped, stop2 := prepareDeadline(parent, 2*time.Hour)
	defer stop2()
	gotCapped, ok := capped.Deadline()
	if !ok {
		t.Fatal("封顶后准备 ctx 仍应带 deadline")
	}
	if diff := gotCapped.Sub(parentDeadline); diff > -30*time.Minute+time.Second || diff < -30*time.Minute-time.Second {
		t.Fatalf("超半预留未按一半封顶：偏移 %v", diff)
	}

	for _, tc := range []struct {
		name    string
		ctx     context.Context
		reserve time.Duration
	}{
		{"无deadline", context.Background(), time.Second},
		{"零预留", parent, 0},
	} {
		if next, stop := prepareDeadline(tc.ctx, tc.reserve); next != tc.ctx {
			stop()
			t.Fatalf("%s 应原样返回父 ctx", tc.name)
		} else {
			stop()
		}
	}
}
