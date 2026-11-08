package main

// 本文件测 O01：副作用工具箱建立后仍跟随面板送礼开关，而不是装配时的一次性快照。

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/reasoning"
	"go.uber.org/zap"
)

type gateGiftExecutor struct {
	calls int
	last  reasoning.GiftRequest
}

func (g *gateGiftExecutor) SendGift(_ context.Context, req reasoning.GiftRequest) (string, error) {
	g.calls++
	g.last = req
	return "礼物已送出", nil
}

type gateQuota struct {
	used     int
	released int
}

func (q *gateQuota) Reserve(_ string, num, limit int) error {
	q.used += num
	return nil
}

func (q *gateQuota) Release(_ string, num int) {
	q.released += num
	q.used -= num
}

func (q *gateQuota) Used(string) (int, error) { return q.used, nil }

// TestSideEffectDepsGiftGateHotToggle 用真实装配路径建一个箱：开→关→开全程跟随门控。
func TestSideEffectDepsGiftGateHotToggle(t *testing.T) {
	cfg := config.Config{}
	cfg.Reasoning.AllowSideEffectTools = true
	cfg.Gift.Enabled = true
	cfg.Gift.Real = true
	cfg.Gift.GiftID = "31036"
	cfg.Gift.MaxNum = 1
	cfg.Gift.DailyLimit = 2
	cfg.Gift.AllowedRooms = []string{"30308220"}

	var gate atomic.Bool
	gate.Store(true)
	g := &gateGiftExecutor{}
	q := &gateQuota{}
	box, err := reasoning.BuildSideEffectTools(
		sideEffectDeps(cfg, q, toolExecutors{Gift: g}, "30308220", gate.Load, nil, zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	// 开：真实执行（额度由 gift.NodeSender 账本核验，工具层只做门控）
	if _, err := box.Run(context.Background(), reasoning.ToolGift, `{"gift_id":"31036","num":1}`); err != nil {
		t.Fatalf("开启时送礼失败：%v", err)
	}
	if g.calls != 1 {
		t.Fatalf("开启后应执行一次：calls=%d", g.calls)
	}

	// 关：同一个箱立即拒绝，不碰执行器
	gate.Store(false)
	if _, err := box.Run(context.Background(), reasoning.ToolGift, `{"gift_id":"31036","num":1}`); err == nil || !strings.Contains(err.Error(), "关闭") {
		t.Fatalf("关闭后应立即拒绝，实际 err=%v", err)
	}
	if g.calls != 1 || q.released != 0 {
		t.Fatalf("关闭后不应执行/退额度：calls=%d released=%d", g.calls, q.released)
	}

	// 再开：同一个箱恢复执行
	gate.Store(true)
	if _, err := box.Run(context.Background(), reasoning.ToolGift, `{"gift_id":"31036","num":1}`); err != nil {
		t.Fatalf("重新开启后送礼失败：%v", err)
	}
	if g.calls != 2 {
		t.Fatalf("重新开启后应再执行一次：calls=%d", g.calls)
	}
}

// TestSideEffectDepsNilGateUsesConfig 没有动态开关时保持静态配置值。
func TestSideEffectDepsNilGateUsesConfig(t *testing.T) {
	cfg := config.Config{}
	cfg.Reasoning.AllowSideEffectTools = true
	cfg.Gift.Enabled = false
	cfg.Gift.Real = true
	cfg.Gift.GiftID = "31036"
	cfg.Gift.MaxNum = 1
	cfg.Gift.DailyLimit = 1
	cfg.Gift.AllowedRooms = []string{"30308220"}

	g := &gateGiftExecutor{}
	q := &gateQuota{}
	box, err := reasoning.BuildSideEffectTools(
		sideEffectDeps(cfg, q, toolExecutors{Gift: g}, "30308220", nil, nil, zap.NewNop()),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := box.Run(context.Background(), reasoning.ToolGift, `{"gift_id":"31036","num":1}`); err == nil {
		t.Error("静态配置关闭时应拒绝")
	}
	if g.calls != 0 {
		t.Errorf("关闭时不应执行：calls=%d", g.calls)
	}
}
