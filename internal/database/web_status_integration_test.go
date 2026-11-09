//go:build integration

package database

// 本文件用真实 PG + 面板 API 验证费用四态：
// 空账本 / 真实零 / 全未知 / 混合；确认 NULL 不会被 COALESCE 成 0。
//
// 只测仓储函数不够：N18 的缺陷横跨 SQL 聚合、DTO 与 JSON 映射，
// 必须经 /api/status 的完整链路断言字段值。

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"RaiseMyFish/internal/web"
	"go.uber.org/zap"
)

// webStatusCost 是 /api/status 费用字段的 JSON 投影。
type webStatusCost struct {
	KnownCost    *float64 `json:"known_cost"`
	UnknownCalls int64    `json:"unknown_calls"`
	TotalCalls   int64    `json:"total_calls"`
	Coverage     float64  `json:"coverage"`
}

// TestWebStatusCostFourScenarios 用真实 PG 造四组账本，经 API 断言费用表达。
func TestWebStatusCostFourScenarios(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(web.Options{Addr: "127.0.0.1:8090", Repo: repo, Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}

	reset := func(t *testing.T) {
		t.Helper()
		if err := db.WithContext(ctx).Exec(`TRUNCATE llm_usage RESTART IDENTITY`).Error; err != nil {
			t.Fatalf("清空账本失败：%v", err)
		}
	}
	insert := func(t *testing.T, cost *float64) {
		t.Helper()
		if err := db.WithContext(ctx).Exec(
			`INSERT INTO llm_usage(channel,model,in_tokens,out_tokens,cost_usd,ms,ok) VALUES(?,?,?,?,?,?,?)`,
			"live", "test", 10, 20, cost, 5, true).Error; err != nil {
			t.Fatalf("插入用量失败：%v", err)
		}
	}
	// status 拉原始 JSON 与费用投影；同时守住旧 cost_usd 字段不再出现。
	status := func(t *testing.T) (map[string]any, webStatusCost) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req.Host = "127.0.0.1:8090"
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，响应 %s", rec.Code, rec.Body.String())
		}
		var raw map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("响应不是 JSON：%v", err)
		}
		if _, ok := raw["cost_usd"]; ok {
			t.Fatal("旧 cost_usd 字段会掩盖未知费用，不应再返回")
		}
		var cost webStatusCost
		if err := json.Unmarshal(rec.Body.Bytes(), &cost); err != nil {
			t.Fatalf("解析费用字段失败：%v", err)
		}
		return raw, cost
	}

	t.Run("空账本", func(t *testing.T) {
		reset(t)
		raw, cost := status(t)
		if v, ok := raw["known_cost"]; !ok || v != nil {
			t.Fatalf("空账本 known_cost 应为 JSON null，实际 %v（存在=%v）", v, ok)
		}
		if cost.UnknownCalls != 0 || cost.TotalCalls != 0 || cost.Coverage != 0 {
			t.Fatalf("空账本计数不符：%+v", cost)
		}
	})

	t.Run("真实零", func(t *testing.T) {
		reset(t)
		zero := 0.0
		insert(t, &zero)
		insert(t, &zero)
		raw, cost := status(t)
		v, ok := raw["known_cost"]
		if !ok || v == nil {
			t.Fatalf("真实零 known_cost 应为 0，实际 %v（存在=%v）", v, ok)
		}
		if value, ok := v.(float64); !ok || value != 0 {
			t.Fatalf("真实零 known_cost = %v，期望 0", v)
		}
		if cost.UnknownCalls != 0 || cost.TotalCalls != 2 || cost.Coverage != 1 {
			t.Fatalf("真实零计数不符：%+v", cost)
		}
	})

	t.Run("全未知", func(t *testing.T) {
		reset(t)
		insert(t, nil)
		insert(t, nil)
		raw, cost := status(t)
		if v, ok := raw["known_cost"]; !ok || v != nil {
			t.Fatalf("全未知 known_cost 应为 JSON null，实际 %v（存在=%v）", v, ok)
		}
		if cost.UnknownCalls != 2 || cost.TotalCalls != 2 || cost.Coverage != 0 {
			t.Fatalf("全未知计数不符：%+v", cost)
		}
	})

	t.Run("混合", func(t *testing.T) {
		reset(t)
		known := 0.01
		zero := 0.0
		insert(t, &known)
		insert(t, &zero) // 零价也是「已知」，计入覆盖率
		insert(t, nil)
		raw, cost := status(t)
		v, ok := raw["known_cost"]
		if !ok || v == nil {
			t.Fatalf("混合 known_cost 应为已知部分之和，实际 %v（存在=%v）", v, ok)
		}
		if value, ok := v.(float64); !ok || math.Abs(value-0.01) > 1e-9 {
			t.Fatalf("混合 known_cost = %v，期望 0.01", v)
		}
		if cost.UnknownCalls != 1 || cost.TotalCalls != 3 {
			t.Fatalf("混合计数不符：%+v", cost)
		}
		if math.Abs(cost.Coverage-2.0/3.0) > 1e-9 {
			t.Fatalf("混合覆盖率 = %v，期望 2/3", cost.Coverage)
		}
	})
}
