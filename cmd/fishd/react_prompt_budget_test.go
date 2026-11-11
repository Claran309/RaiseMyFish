package main

// 本文件是 F04 的生产装配级验收：按审查探针的负载（100 条长近期记忆）构造
// ReAct 输入，用假 Provider 抓取每次实际请求，确认：
//   - 实际请求的保守字节估计不超过「配置预算 - 输出预留」；
//   - 当前问题与焦点对话未被裁掉，近期记忆只裁最旧的后缀；
//   - 输出预留吃光预算时直接降级，不创建 Agent、不发请求；
//   - provider 适配器会累计服务端真实 usage 供日志记录。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/reasoning"
	"go.uber.org/zap"
)

// budgetTestInput 构造与审查探针同形的输入：100 条长近期消息 + 焦点对话。
func budgetTestInput() (brain.ContextInput, event.Event, brain.Task) {
	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	ev := snapshotTestEvent("边界问题标记：刚才聊什么了")
	recent := make([]memory.Message, 0, 100)
	for i := 0; i < 100; i++ {
		recent = append(recent, memory.Message{
			EventID: fmt.Sprintf("live:30308220:old-%03d", i),
			Channel: event.ChLive, Session: scope.Session, Dir: event.DirIn,
			FromID: "1001", FromName: "观众A",
			Text: fmt.Sprintf("第%03d条：%s", i, strings.Repeat("长", 99)),
			At:   ev.At.Add(-time.Duration(100-i) * time.Minute),
		})
	}
	focus := []memory.Message{
		{EventID: "live:30308220:focus-a", Channel: event.ChLive, Session: scope.Session, Dir: event.DirIn, FromID: "1001", FromName: "观众A", Text: "焦点对话标记：我喜欢吃鱼", At: ev.At.Add(-2 * time.Minute)},
		{EventID: "live:30308220:focus-b", Channel: event.ChLive, Session: scope.Session, Dir: event.DirOut, Text: "焦点对话标记：记得你爱吃鱼", ReplyTo: "live:30308220:focus-a", At: ev.At.Add(-time.Minute)},
	}
	input := brain.ContextInput{
		Event:   ev,
		Persona: snapshotTestPack(),
		Memory:  memory.Context{Scope: scope, Recent: recent, Focus: focus},
		Shared:  memory.SharedState{Scope: scope},
	}
	task := brain.Task{ID: "task-" + ev.ID, EventID: ev.ID, Channel: ev.Channel, Session: ev.Session}
	return input, ev, task
}

// TestRunReactEveryRequestWithinPromptBudget F04 验收：
// 实际首步/带观察的后续请求都必须 ≤ max_prompt_tokens - 输出预留。
func TestRunReactEveryRequestWithinPromptBudget(t *testing.T) {
	const (
		maxPrompt     = 16384
		outputReserve = 128
	)
	input, ev, task := budgetTestInput()
	scope := memory.Scope{Channel: ev.Channel, Session: ev.Session}
	prov := &captureProvider{}
	prov.reply = func(r provider.Request) string {
		if strings.Contains(r.Messages[1].Content, "已经查到") {
			return `{"thought":"够了","action":"final","final":"按上下文回答"}`
		}
		return `{"thought":"查历史","action":"history","args":"5"}`
	}
	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: reasoning.ToolHistory, Desc: "查最近弹幕",
		Run: func(context.Context, string) (string, error) { return "刚才在聊吃鱼", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &reactRuntime{
		factory: func(memory.Scope, string, bool, event.Channel) (*reasoning.Agent, error) {
			return reasoning.New(captureLLM(prov), box, reasoning.Options{MaxSteps: 3, Timeout: 2 * time.Second}, zap.NewNop())
		},
		defaultReact:  true,
		budget:        brain.Budget{MaxPromptTokens: maxPrompt, KnowledgeBudgetPercent: 10},
		outputReserve: outputReserve,
	}

	res, err := runReact(context.Background(), runtime, scope, ev, task, input, zap.NewNop())
	if err != nil {
		t.Fatalf("runReact 失败：%v", err)
	}
	if res.Text != "按上下文回答" || res.Degraded {
		t.Fatalf("应正常收尾：text=%q degraded=%v reason=%q", res.Text, res.Degraded, res.Reason)
	}
	requests := prov.snapshot()
	if len(requests) != 3 {
		t.Fatalf("模型请求数 = %d，期望 3（首步工具 + 带观察收尾 + 反思）", len(requests))
	}
	limit := maxPrompt - outputReserve
	for i, req := range requests {
		if size := provider.EstimateMessages(req.Messages); size > limit {
			t.Fatalf("第 %d 次实际请求超预算：%d > %d", i+1, size, limit)
		}
		user := req.Messages[1].Content
		if !strings.Contains(user, "边界问题标记") {
			t.Fatalf("第 %d 次请求丢失当前问题：\n%s", i+1, user)
		}
		if !strings.Contains(user, "焦点对话标记") {
			t.Fatalf("第 %d 次请求丢失本轮焦点：\n%s", i+1, user)
		}
	}
	first := requests[0].Messages[1].Content
	if !strings.Contains(first, "第099条") {
		t.Fatalf("近期记忆应保留最新后缀而不是全部丢弃：\n%s", first)
	}
	if strings.Contains(first, "第000条") {
		t.Fatalf("超预算时最旧的近期记忆应先被裁掉：\n%s", first)
	}
	if last := requests[2].Messages[1].Content; !strings.Contains(last, "已经查到") || !strings.Contains(last, "吃鱼") {
		t.Fatalf("反思请求丢失工具结果：\n%s", last)
	}
	if res.PromptBytes != provider.EstimateMessages(requests[len(requests)-1].Messages) {
		t.Fatalf("PromptBytes=%d 与实际最后一次请求估计不符", res.PromptBytes)
	}
	if res.PromptBytes > limit {
		t.Fatalf("结果记录的 PromptBytes 超预算：%d > %d", res.PromptBytes, limit)
	}
}

// TestRunReactOutputReserveExhaustedDegrades 输出预留吃光预算时：
// 不创建 Agent、不发模型请求，返回 budget 降级交给单步回落。
func TestRunReactOutputReserveExhaustedDegrades(t *testing.T) {
	input, ev, task := budgetTestInput()
	input.Memory.Recent = nil
	scope := memory.Scope{Channel: ev.Channel, Session: ev.Session}
	prov := &captureProvider{}
	factoryCalls := 0
	runtime := &reactRuntime{
		factory: func(memory.Scope, string, bool, event.Channel) (*reasoning.Agent, error) {
			factoryCalls++
			return nil, nil
		},
		defaultReact:  true,
		budget:        brain.Budget{MaxPromptTokens: 1000, KnowledgeBudgetPercent: 10},
		outputReserve: 1000,
	}

	res, err := runReact(context.Background(), runtime, scope, ev, task, input, zap.NewNop())
	if err != nil {
		t.Fatalf("预算不足不应上抛错误：%v", err)
	}
	if !res.Degraded || res.Reason != reasoning.DegradeBudget || res.Text != "" {
		t.Fatalf("应为 budget 降级且无正文：%+v", res)
	}
	if factoryCalls != 0 {
		t.Fatalf("预算为 0 时不应创建 Agent：%d 次", factoryCalls)
	}
	if len(prov.snapshot()) != 0 {
		t.Fatalf("预算为 0 时不应发模型请求：%d 次", len(prov.snapshot()))
	}
}

// usageProvider 返回固定服务端 usage，用于验证适配器累计。
type usageProvider struct {
	calls int
	in    int
	out   int
}

func (p *usageProvider) Name() string { return "usage" }

func (p *usageProvider) Chat(context.Context, provider.Request) (provider.Response, error) {
	p.calls++
	in, out := p.in, p.out
	return provider.Response{Text: "好", Usage: provider.Usage{InputTokens: &in, OutputTokens: &out}}, nil
}

// TestProviderLLMAccumulatesUsage 记录估计器口径之外的真实 usage：
// 每次调用都累计，服务端没给的字段由 Known 标记，不编造 0。
func TestProviderLLMAccumulatesUsage(t *testing.T) {
	prov := &usageProvider{in: 120, out: 30}
	llm := &providerLLM{
		blueprint: &reasoningModel{client: prov, model: "usage-model", maxTokens: 16, usage: &fakeUsageRepo{}},
		channel:   event.ChLive,
		log:       zap.NewNop(),
	}
	for i := 0; i < 2; i++ {
		if _, err := llm.Complete(context.Background(), "系统", "用户"); err != nil {
			t.Fatalf("第 %d 次调用失败：%v", i+1, err)
		}
	}
	usage := llm.Usage()
	if usage.Calls != 2 || usage.InputTokens != 240 || usage.OutputTokens != 60 || !usage.InputKnown || !usage.OutputKnown {
		t.Fatalf("usage 累计不符：%+v", usage)
	}
}
