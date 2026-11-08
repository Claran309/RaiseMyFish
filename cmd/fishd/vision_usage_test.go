package main

// 本文件测 O08 的 fishd 侧装配：视觉记账 sink 的报价挑选与端到端写账。

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
)

func visionIntPtr(n int) *int           { return &n }
func visionFloatPtr(v float64) *float64 { return &v }

// TestVisionSinkRecordsCostWithPrice 有 token + 有报价 → 写一行带费用的账（live 通道）。
func TestVisionSinkRecordsCostWithPrice(t *testing.T) {
	repo := &fakeUsageRepo{}
	provider := config.Provider{Prices: map[string]config.Price{
		"vision-model": {InputUSDPerMillion: visionFloatPtr(1), OutputUSDPerMillion: visionFloatPtr(2)},
	}}
	sink := visionSink(repo, "vision-model", provider, zap.NewNop())
	if sink == nil {
		t.Fatal("有账本时应返回 sink")
	}

	sink(context.Background(), watch.VisionUsage{
		InputTokens:  visionIntPtr(100),
		OutputTokens: visionIntPtr(20),
	}, true, 150*time.Millisecond)

	rows := repo.snapshot()
	if len(rows) != 1 {
		t.Fatalf("应写 1 行：%+v", rows)
	}
	row := rows[0]
	if row.Channel != event.ChLive || row.Model != "vision-model" || !row.OK {
		t.Fatalf("基础字段不符：%+v", row)
	}
	// (100*1 + 20*2) / 1e6 = 0.00014
	if row.CostUSD == nil || *row.CostUSD < 0.0001399 || *row.CostUSD > 0.0001401 {
		t.Fatalf("费用不符：%v", row.CostUSD)
	}
}

// TestVisionSinkUnknownPrice 模型没配报价 → CostUSD=nil（unknown），不是 0。
func TestVisionSinkUnknownPrice(t *testing.T) {
	repo := &fakeUsageRepo{}
	provider := config.Provider{Prices: map[string]config.Price{}} // 没有该模型的报价
	sink := visionSink(repo, "vision-model", provider, zap.NewNop())

	sink(context.Background(), watch.VisionUsage{
		InputTokens:  visionIntPtr(100),
		OutputTokens: visionIntPtr(20),
	}, true, time.Millisecond)

	rows := repo.snapshot()
	if len(rows) != 1 {
		t.Fatalf("应写 1 行：%+v", rows)
	}
	if rows[0].CostUSD != nil {
		t.Fatalf("未知费用应记 nil，实际 %v", *rows[0].CostUSD)
	}
}

// TestVisionSinkNilRepo 没有账本时不返回 sink（调用方无需判空）。
func TestVisionSinkNilRepo(t *testing.T) {
	if sink := visionSink(nil, "m", config.Provider{}, zap.NewNop()); sink != nil {
		t.Fatal("nil 账本应返回 nil sink")
	}
}

// TestModelPriceMissing 未配置报价时返回零值（两个指针都是 nil）。
func TestModelPriceMissing(t *testing.T) {
	if price := modelPrice(config.Provider{}, "m"); price.InputUSDPerMillion != nil || price.OutputUSDPerMillion != nil {
		t.Fatalf("未配置报价不应编造价格：%+v", price)
	}
	provider := config.Provider{Prices: map[string]config.Price{
		"m": {InputUSDPerMillion: visionFloatPtr(3), OutputUSDPerMillion: visionFloatPtr(4)},
	}}
	price := modelPrice(provider, "m")
	if price.InputUSDPerMillion == nil || *price.InputUSDPerMillion != 3 {
		t.Fatalf("报价未取到：%+v", price)
	}
}
