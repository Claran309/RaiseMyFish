package config

import (
	"path/filepath"
	"testing"
)

// TestBenchConfigIsSelfConsistent 防止 staging 配置在重构后悄悄失效。
// 只做加载/结构/凭据规则校验，不连接数据库。
func TestBenchConfigIsSelfConsistent(t *testing.T) {
	t.Setenv("FISH_BENCH_DSN", "postgres://fish@127.0.0.1:5432/raisemyfish_bench")
	t.Setenv("FISH_MOCK_LLM_KEY", "mock")
	path := filepath.Join("..", "..", "config", "fishd.bench.toml")
	cfg, err := LoadForDaemon(path, "")
	if err != nil {
		t.Fatalf("加载 bench 配置：%v", err)
	}
	if cfg.Database.DSNEnv != "FISH_BENCH_DSN" {
		t.Fatalf("bench DSN 环境变量=%q", cfg.Database.DSNEnv)
	}
	if cfg.Sender == nil || !cfg.Sender.DryRun {
		t.Fatal("bench 配置必须 dry_run=true")
	}
	if !cfg.Metrics.Enabled || cfg.Metrics.Addr != "127.0.0.1:19091" {
		t.Fatalf("metrics=%+v", cfg.Metrics)
	}
	if err := cfg.ValidateCredentials(); err != nil {
		t.Fatalf("dry_run 凭据规则：%v", err)
	}
}
