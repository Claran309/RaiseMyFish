//go:build integration

package main

// O08 端到端验收：真实 PG + 假视觉服务 → llm_usage 落一行 → 面板统计（/api/status 的数据源）
// 的费用里包含这次视觉调用。
//
// 面板费用 = SUM(llm_usage.cost_usd)，不区分模型来源；
// 只要视觉调用按同一口径写进账本，费用总览自然覆盖。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
)

func TestVisionUsageReachesPanelStats(t *testing.T) {
	db, ctx := embeddingTestDB(t)
	usageRepo, err := database.NewUsageRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := database.NewStats(db)
	if err != nil {
		t.Fatal(err)
	}

	// 假视觉服务：返回一条描述 + usage（"一行 usage"）。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"主播在喝水"}}],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	}))
	defer server.Close()

	// 最小合法帧：readFrame 只校验扩展名与非空大小。
	frame := filepath.Join(t.TempDir(), "frame-00.jpg")
	if err := os.WriteFile(frame, []byte("jpeg-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	price := config.Price{
		InputUSDPerMillion:  visionFloatPtr(1),
		OutputUSDPerMillion: visionFloatPtr(2),
	}
	describer, err := watch.NewOpenAIDescriber(server.URL, "", "vision-model", server.Client(), 0,
		watch.UsageSinkFor(usageRepo, "vision-model", price.InputUSDPerMillion, price.OutputUSDPerMillion, zap.NewNop()))
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now().Add(-time.Second)
	if _, err := describer.Describe(ctx, []string{frame}, watch.DefaultPrompt); err != nil {
		t.Fatalf("视觉调用失败：%v", err)
	}
	end := time.Now().Add(time.Second)

	var row struct {
		Channel      string   `gorm:"column:channel"`
		Purpose      string   `gorm:"column:purpose"`
		Model        string   `gorm:"column:model"`
		InputTokens  *int     `gorm:"column:in_tokens"`
		OutputTokens *int     `gorm:"column:out_tokens"`
		CostUSD      *float64 `gorm:"column:cost_usd"`
		OK           bool     `gorm:"column:ok"`
	}
	if err := db.WithContext(ctx).Raw(
		`SELECT channel, purpose, model, in_tokens, out_tokens, cost_usd, ok FROM llm_usage WHERE model = ?`,
		"vision-model").Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Channel != "live" || !row.OK || row.Purpose != "vision" {
		t.Fatalf("视觉账本行不符：%+v", row)
	}
	if row.InputTokens == nil || *row.InputTokens != 1000 || row.OutputTokens == nil || *row.OutputTokens != 500 {
		t.Fatalf("token 不符：%+v", row)
	}
	// (1000*1 + 500*2) / 1e6 = 0.002
	if row.CostUSD == nil || *row.CostUSD < 0.0019999 || *row.CostUSD > 0.0020001 {
		t.Fatalf("费用不符：%v", row.CostUSD)
	}

	// 面板费用总览同样按 SUM(llm_usage.cost_usd) 统计（/api/status 的已知费用）。
	counts, err := stats.Counts(ctx, start, end)
	if err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if counts.KnownCostUSD == nil {
		t.Fatal("费用总览应为已知值，实际 unknown")
	}
	if *counts.KnownCostUSD < 0.0019999 {
		t.Fatalf("费用总览未包含视觉调用：%v", *counts.KnownCostUSD)
	}
	if counts.UnknownCalls != 0 || counts.TotalCalls < 1 || counts.Coverage != 1 {
		t.Fatalf("账本口径不符：unknown=%d total=%d coverage=%v",
			counts.UnknownCalls, counts.TotalCalls, counts.Coverage)
	}
}
