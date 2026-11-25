package main

import (
	"path/filepath"
	"testing"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
)

// staging 回放的是 30 天历史样本，调度器不能按生产 60 秒阈值把它们全部丢掉。
func TestBenchSchedulerKeepsHistoricalEvents(t *testing.T) {
	t.Setenv("FISH_BENCH_DSN", "postgres://postgres@127.0.0.1:55632/raisemyfish_bench?sslmode=disable")
	t.Setenv("FISH_MOCK_LLM_KEY", "mock")
	cfg, err := config.LoadForDaemon(filepath.Join("..", "..", "config", "fishd.bench.toml"), "")
	if err != nil {
		t.Fatal(err)
	}
	opts := buildSchedulerOptions(cfg.Scheduler)
	if opts.MaxEventAge != 0 {
		t.Fatalf("全局 stale 阈值=%s，期望 0", opts.MaxEventAge)
	}
	if live := opts.Channels[event.ChLive]; live.MaxEventAge != 0 {
		t.Fatalf("live stale 阈值=%s，期望 0", live.MaxEventAge)
	}
}
