package main

// F07：后台增强（记忆提炼/梗学习）与 ReAct 适配器的账本行。
//
// 关注点：purpose 必填且可区分、关联 ID 只存安全标识、失败尝试也落账、
// 缓存命中只透传服务端声明值，费用缺失保持 nil。

import (
	"context"
	"errors"
	"math"
	"testing"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/llmmeta"
	"go.uber.org/zap"
)

func floatPtr(v float64) *float64 { return &v }

// TestBackgroundLLMRecordsUsage 后台调用写一行带用途/关联 ID/费用/缓存命中的账。
func TestBackgroundLLMRecordsUsage(t *testing.T) {
	prov := &fakeChatProvider{resp: provider.Response{
		Text: "摘要",
		Usage: provider.Usage{
			InputTokens:       intPtr(800),
			OutputTokens:      intPtr(120),
			CachedInputTokens: intPtr(40),
		},
	}}
	repo := &fakeUsageRepo{}
	in, out := 0.5, 2.0
	adapter := &memoryProviderLLM{
		client: prov, model: "background-model", maxTokens: 64,
		purpose: llmmeta.PurposeMemoryExtract,
		inUSD:   &in, outUSD: &out,
		usage: repo, logger: zap.NewNop(),
	}
	ctx := llmmeta.WithCall(context.Background(), llmmeta.Call{
		Purpose: llmmeta.PurposeMemoryExtract, CorrelationID: "memory:batch:7",
	})
	got, err := adapter.Complete(ctx, "sys", "user")
	if err != nil || got != "摘要" {
		t.Fatalf("Complete 失败：%q / %v", got, err)
	}

	rows := repo.snapshot()
	if len(rows) != 1 {
		t.Fatalf("应写 1 行：%+v", rows)
	}
	row := rows[0]
	if row.Purpose != string(llmmeta.PurposeMemoryExtract) || row.CorrelationID != "memory:batch:7" {
		t.Fatalf("用途/关联 ID 不符：%+v", row)
	}
	if row.Channel != "" {
		t.Fatalf("后台调用不该冒充对话通道，实际 %q", row.Channel)
	}
	if row.Model != "background-model" || !row.OK {
		t.Fatalf("基础字段不符：%+v", row)
	}
	if row.InputTokens == nil || *row.InputTokens != 800 || row.OutputTokens == nil || *row.OutputTokens != 120 {
		t.Fatalf("token 不符：%+v", row)
	}
	if row.CachedInputTokens == nil || *row.CachedInputTokens != 40 {
		t.Fatalf("缓存命中未透传：%v", row.CachedInputTokens)
	}
	// (800*0.5 + 120*2) / 1e6 = 0.00064
	if row.CostUSD == nil || math.Abs(*row.CostUSD-0.00064) > 1e-9 {
		t.Fatalf("费用不符：%v", row.CostUSD)
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("账本行不合法：%v", err)
	}
}

// TestBackgroundLLMUnknownCost 没配报价时费用必须是 nil（unknown），不能编 0。
func TestBackgroundLLMUnknownCost(t *testing.T) {
	prov := &fakeChatProvider{resp: provider.Response{
		Text:  "ok",
		Usage: provider.Usage{InputTokens: intPtr(10), OutputTokens: intPtr(5)},
	}}
	repo := &fakeUsageRepo{}
	adapter := &memoryProviderLLM{
		client: prov, model: "m", maxTokens: 8,
		purpose: llmmeta.PurposeMemeLearn, usage: repo, logger: zap.NewNop(),
	}
	if _, err := adapter.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	row := repo.snapshot()[0]
	if row.CostUSD != nil {
		t.Fatalf("未配报价时 CostUSD 应为 nil，实际 %v", *row.CostUSD)
	}
	if row.Purpose != string(llmmeta.PurposeMemeLearn) {
		t.Fatalf("梗学习用途不符：%q", row.Purpose)
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("账本行不合法：%v", err)
	}
}

// TestBackgroundLLMRecordsFailedCall 失败的模型调用也要记（OK=false）。
func TestBackgroundLLMRecordsFailedCall(t *testing.T) {
	boom := errors.New("provider down")
	prov := &fakeChatProvider{err: boom}
	repo := &fakeUsageRepo{}
	adapter := &memoryProviderLLM{
		client: prov, model: "m", maxTokens: 8,
		purpose: llmmeta.PurposeMemoryExtract, usage: repo, logger: zap.NewNop(),
	}
	if _, err := adapter.Complete(context.Background(), "s", "u"); !errors.Is(err, boom) {
		t.Fatalf("错误未透传：%v", err)
	}
	rows := repo.snapshot()
	if len(rows) != 1 || rows[0].OK {
		t.Fatalf("失败调用应记 OK=false：%+v", rows)
	}
}

// TestBackgroundLLMUnsafeCorrelationDropped 正文/昵称等不安全关联 ID 不得进账本。
func TestBackgroundLLMUnsafeCorrelationDropped(t *testing.T) {
	prov := &fakeChatProvider{resp: provider.Response{Text: "ok"}}
	repo := &fakeUsageRepo{}
	adapter := &memoryProviderLLM{
		client: prov, model: "m", maxTokens: 8,
		purpose: llmmeta.PurposeMemoryExtract, usage: repo, logger: zap.NewNop(),
	}
	ctx := llmmeta.WithCall(context.Background(), llmmeta.Call{CorrelationID: "小明说：走，去钓鱼\n"})
	if _, err := adapter.Complete(ctx, "s", "u"); err != nil {
		t.Fatal(err)
	}
	if got := repo.snapshot()[0].CorrelationID; got != "" {
		t.Fatalf("不安全关联 ID 未丢弃：%q", got)
	}
}

// TestReactAdapterRecordsPurposeAndCorrelation ReAct 账本行标注 react 与事件 ID。
func TestReactAdapterRecordsPurposeAndCorrelation(t *testing.T) {
	prov := &fakeChatProvider{resp: provider.Response{
		Text:  `{"thought":"t","action":"final","final":"好"}`,
		Usage: provider.Usage{InputTokens: intPtr(30), OutputTokens: intPtr(8)},
	}}
	repo := &fakeUsageRepo{}
	llm := newTestProviderLLM(prov, repo)
	ctx := llmmeta.WithCall(context.Background(), llmmeta.Call{
		Purpose: llmmeta.PurposeReact, CorrelationID: "live:room:42",
	})
	if _, err := llm.Complete(ctx, "sys", "user"); err != nil {
		t.Fatalf("Complete 失败：%v", err)
	}
	row := repo.snapshot()[0]
	if row.Purpose != string(llmmeta.PurposeReact) || row.CorrelationID != "live:room:42" {
		t.Fatalf("用途/关联 ID 不符：%+v", row)
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("账本行不合法：%v", err)
	}
}
