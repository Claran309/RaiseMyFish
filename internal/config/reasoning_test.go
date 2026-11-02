package config

// 本文件测 [reasoning] 段：默认值、关闭开关、预算校验。

import (
	"strings"
	"testing"
)

// TestReasoningDefaults 缺段/缺字段时用默认值（默认开启，3 步 / 8 秒）。
func TestReasoningDefaults(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[log]\nlevel='info'\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !cfg.Reasoning.Enabled {
		t.Error("默认应开启 ReAct")
	}
	if cfg.Reasoning.MaxSteps != 3 || cfg.Reasoning.TimeoutMS != 8000 {
		t.Errorf("默认预算不符：%+v", cfg.Reasoning)
	}
}

// TestReasoningCanBeDisabled 显式 enabled=false 时关闭。
func TestReasoningCanBeDisabled(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[reasoning]\nenabled = false\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Reasoning.Enabled {
		t.Error("显式关闭未生效")
	}
	// 关闭时预算仍要补齐，便于日志展示
	if cfg.Reasoning.MaxSteps != 3 || cfg.Reasoning.TimeoutMS != 8000 {
		t.Errorf("关闭时预算未补默认：%+v", cfg.Reasoning)
	}
}

// TestReasoningRejectsBadBudget 非法预算在加载阶段被拒。
func TestReasoningRejectsBadBudget(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"步数为0", "[reasoning]\nmax_steps = 0\ntimeout_ms = 8000\n", ""}, // 0 会被补默认，不报错
		{"步数过大", "[reasoning]\nmax_steps = 11\n", "步数"},
		{"超时过小", "[reasoning]\ntimeout_ms = 500\n", "超时"},
		{"超时过大", "[reasoning]\ntimeout_ms = 30000\n", "超时"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWithScheduler(t, tc.body)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("零值应被补成默认而不是报错：%v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("非法预算未被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息 %q 不含 %q", err.Error(), tc.want)
			}
		})
	}
}
