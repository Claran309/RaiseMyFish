package config

// 本文件测每日总结配置的默认值、范围校验与 LLM 依赖。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummaryDefaults(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Summary
	if s.Enabled || s.Hour != 0 || s.Minute != 10 || s.MaxEvents != 300 || s.MaxTokens != 900 {
		t.Fatalf("每日总结默认值错误：%+v", s)
	}
}

func TestSummaryLoadAndValidate(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(p, []byte("[summary]\nhour=23\nminute=55\nmax_events=500\nmax_tokens=1200\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Summary.Enabled || cfg.Summary.Hour != 23 || cfg.Summary.Minute != 55 || cfg.Summary.MaxEvents != 500 || cfg.Summary.MaxTokens != 1200 {
		t.Fatalf("每日总结配置未生效：%+v", cfg.Summary)
	}

	for _, tc := range []Summary{
		{Hour: 24, Minute: 0, MaxEvents: 300, MaxTokens: 900},
		{Hour: -1, Minute: 0, MaxEvents: 300, MaxTokens: 900},
		{Hour: 0, Minute: 60, MaxEvents: 300, MaxTokens: 900},
		{Hour: 0, Minute: 0, MaxEvents: 10, MaxTokens: 900},
		{Hour: 0, Minute: 0, MaxEvents: 300, MaxTokens: 100},
	} {
		if err := tc.Validate(); err == nil {
			t.Errorf("越界配置应被拒绝：%+v", tc)
		}
	}
}

// TestSummaryRequiresLLM 开启每日总结但未启用 LLM 时加载失败。
func TestSummaryRequiresLLM(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(p, []byte("[summary]\nenabled=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p, "")
	if err == nil || !strings.Contains(err.Error(), "每日总结") {
		t.Fatalf("应要求先启用 LLM：%v", err)
	}
}
