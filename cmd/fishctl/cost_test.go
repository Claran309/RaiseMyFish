package main

import (
	"bytes"
	"context"
	"testing"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
)

// TestCacheABUnstableDoesNotMutateOriginal 对照侧只改拷贝，稳定侧 prompt 不受影响。
func TestCacheABUnstableDoesNotMutateOriginal(t *testing.T) {
	original := []provider.Message{
		{Role: "system", Content: "固定前缀"},
		{Role: "user", Content: "问题"},
	}
	unstable := cacheABUnstable(original, "nonce", "case-1")
	if original[0].Content != "固定前缀" {
		t.Fatal("原 system 被改写")
	}
	if unstable[0].Content == original[0].Content || unstable[1].Content != "问题" {
		t.Fatalf("对照侧构造错误：%+v", unstable)
	}
	// 没有 system 时保持原样。
	noSystem := []provider.Message{{Role: "user", Content: "x"}}
	if got := cacheABUnstable(noSystem, "n", "c"); got[0].Content != "x" {
		t.Fatalf("无 system 被改写：%+v", got)
	}
}

// TestAccumulateCacheSideCostNoDoubleCount 成本按每次调用的增量累计，不重复累加。
func TestAccumulateCacheSideCostNoDoubleCount(t *testing.T) {
	in1, out1 := 1000, 10
	in2, out2 := 500, 5
	cached := 800
	price := &config.Price{InputUSDPerMillion: floatPtr(1), OutputUSDPerMillion: floatPtr(10)}
	side := cacheABSide{}
	accumulateCacheSide(&side, provider.Response{Usage: provider.Usage{InputTokens: &in1, OutputTokens: &out1, CachedInputTokens: &cached}}, price)
	accumulateCacheSide(&side, provider.Response{Usage: provider.Usage{InputTokens: &in2, OutputTokens: &out2, CachedInputTokens: &cached}}, price)
	if side.Calls != 2 || side.InputTokens != 1500 || side.OutputTokens != 15 || side.CachedTokens != 1600 {
		t.Fatalf("累计字段错误：%+v", side)
	}
	want := 1500.0/1e6*1 + 15.0/1e6*10
	if diff := side.CostUSD - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("成本重复计数：got=%v want=%v", side.CostUSD, want)
	}
	// 没有报价时保持 unknown。
	unknown := cacheABSide{}
	accumulateCacheSide(&unknown, provider.Response{Usage: provider.Usage{InputTokens: &in1}}, nil)
	if unknown.CostKnown {
		t.Fatal("无报价却标了 known")
	}
}

// TestRunCostRejectsBadArgs 参数不合法时不连模型。
func TestRunCostRejectsBadArgs(t *testing.T) {
	if err := runCost(context.Background(), "", "", []string{"cache-ab"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("缺 output 被接受")
	}
	if err := runCost(context.Background(), "", "", []string{"unknown"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("未知子命令被接受")
	}
}
