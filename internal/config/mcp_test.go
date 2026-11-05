package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMCPDefaultDisabled 缺省关闭且没有白名单。
func TestMCPDefaultDisabled(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(p, []byte("[log]\nlevel='info'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if cfg.MCP.Enabled || len(cfg.MCP.AllowedTools) != 0 {
		t.Fatalf("MCP 默认应为关闭且无白名单：%+v", cfg.MCP)
	}
}

// TestLoadMCPConfig 新字段能从 TOML 加载。
func TestLoadMCPConfig(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private@localhost/fish")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	body := "[mcp]\nenabled=true\nallowed_tools=['memory_search','now']\n"
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !cfg.MCP.Enabled || len(cfg.MCP.AllowedTools) != 2 || cfg.MCP.AllowedTools[0] != "memory_search" {
		t.Fatalf("MCP 配置未正确加载：%+v", cfg.MCP)
	}
}

// TestMCPValidate 工具名格式与重复项在加载期拒绝。
func TestMCPValidate(t *testing.T) {
	good := []MCP{
		{},
		{AllowedTools: []string{"memory_search", "viewer_memory", "now", " post_dynamic "}},
	}
	for _, m := range good {
		if err := m.Validate(); err != nil {
			t.Errorf("配置 %+v 应合法：%v", m, err)
		}
	}
	bad := []MCP{
		{AllowedTools: []string{""}},
		{AllowedTools: []string{"Memory_Search"}},
		{AllowedTools: []string{"memory-search"}},
		{AllowedTools: []string{"1now"}},
		{AllowedTools: []string{"now", "now"}},
		{AllowedTools: []string{"now\n"}},
	}
	for _, m := range bad {
		if err := m.Validate(); err == nil {
			t.Errorf("配置 %+v 应被拒绝", m)
		}
	}
}
