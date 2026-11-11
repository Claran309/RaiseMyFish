package config

// 本文件测 [reasoning] 段：默认值、关闭开关、预算校验。

import (
	"strings"
	"testing"
)

// TestReasoningDefaults 缺段/缺字段时用默认值（默认开启、默认 ReAct，3 步 / 8 秒）。
func TestReasoningDefaults(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[log]\nlevel='info'\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !cfg.Reasoning.Enabled {
		t.Error("默认应开启 ReAct")
	}
	if !cfg.Reasoning.DefaultReact {
		t.Error("默认应所有弹幕走 ReAct")
	}
	if cfg.Reasoning.MaxSteps != 3 || cfg.Reasoning.TimeoutMS != 8000 {
		t.Errorf("默认预算不符：%+v", cfg.Reasoning)
	}
	if cfg.Reasoning.AllowDanmakuTool {
		t.Error("send_danmaku 默认应关闭，显式开启才装配")
	}
	for name, got := range map[string]ReasoningChannel{"评论": cfg.Reasoning.Comment, "私信": cfg.Reasoning.DM} {
		if got.MaxSteps != 5 || got.TimeoutMS != 45000 {
			t.Errorf("%s默认预算不符：%+v", name, got)
		}
	}
}

// TestReasoningChannelBudgets 评论/私信可配 5-10 步 / 30-60 秒，弹幕预算不受影响。
func TestReasoningChannelBudgets(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[reasoning]\nmax_steps = 3\ntimeout_ms = 8000\n[reasoning.comment]\nmax_steps = 8\ntimeout_ms = 60000\n[reasoning.dm]\nmax_steps = 10\ntimeout_ms = 30000\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Reasoning.MaxSteps != 3 || cfg.Reasoning.TimeoutMS != 8000 {
		t.Errorf("弹幕预算被改动：%+v", cfg.Reasoning)
	}
	if cfg.Reasoning.Comment.MaxSteps != 8 || cfg.Reasoning.Comment.TimeoutMS != 60000 {
		t.Errorf("评论预算不符：%+v", cfg.Reasoning.Comment)
	}
	if cfg.Reasoning.DM.MaxSteps != 10 || cfg.Reasoning.DM.TimeoutMS != 30000 {
		t.Errorf("私信预算不符：%+v", cfg.Reasoning.DM)
	}
}

// TestReasoningRejectsBadChannelBudget 评论/私信预算越界在加载阶段被拒。
func TestReasoningRejectsBadChannelBudget(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"评论步数过小", "[reasoning.comment]\nmax_steps = 4\n", "评论 ReAct 步数"},
		{"评论步数过大", "[reasoning.comment]\nmax_steps = 11\n", "评论 ReAct 步数"},
		{"评论超时过小", "[reasoning.comment]\ntimeout_ms = 20000\n", "评论 ReAct 超时"},
		{"评论超时过大", "[reasoning.comment]\ntimeout_ms = 61000\n", "评论 ReAct 超时"},
		{"私信步数过小", "[reasoning.dm]\nmax_steps = 1\n", "私信 ReAct 步数"},
		{"私信超时过小", "[reasoning.dm]\ntimeout_ms = 1000\n", "私信 ReAct 超时"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWithScheduler(t, tc.body)
			if err == nil {
				t.Fatal("非法预算未被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息 %q 不含 %q", err.Error(), tc.want)
			}
		})
	}
}

// TestReasoningDanmakuToolSwitch 显式 allow_danmaku_tool=true 才开启弹幕工具。
func TestReasoningDanmakuToolSwitch(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[reasoning]\nallow_danmaku_tool = true\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !cfg.Reasoning.AllowDanmakuTool {
		t.Error("显式开启 send_danmaku 未生效")
	}
}

// TestReasoningDefaultReactCanBeDisabled 显式 default_react=false 回到旧两段式。
func TestReasoningDefaultReactCanBeDisabled(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[reasoning]\ndefault_react = false\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.Reasoning.DefaultReact {
		t.Error("显式关闭默认 ReAct 未生效")
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
