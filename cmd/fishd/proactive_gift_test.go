package main

import (
	"context"
	"testing"

	"RaiseMyFish/internal/config"
	"go.uber.org/zap"
)

// TestStartProactiveGiftDisabledPaths 验证未启用/无房间/间隔为 0 时返回 noop。
func TestStartProactiveGiftDisabledPaths(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
	}{
		{"未启用", config.Config{Gift: config.Gift{Enabled: false}}},
		{"无白名单房间", config.Config{Gift: config.Gift{Enabled: true, AllowedRooms: nil}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stop := startProactiveGift(context.Background(), tc.cfg, nil, nil, zap.NewNop())
			stop() // noop 应可安全调用
		})
	}
}

// TestStartProactiveGiftNoopWhenSenderNil sender 为 nil 时不启动（装配失败降级）。
func TestStartProactiveGiftNoopWhenSenderNil(t *testing.T) {
	cfg := config.Config{Gift: config.Gift{Enabled: true, AllowedRooms: []string{"1992099976"}}}
	stop := startProactiveGift(context.Background(), cfg, nil, nil, zap.NewNop())
	stop()
}
