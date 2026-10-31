package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeConfigDefaultsAndLimits(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://unit-only")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Knowledge.Enabled || cfg.Knowledge.Dir != "personas/dafeiyu/knowledge" || cfg.Knowledge.BudgetPercent != 10 {
		t.Fatal("知识默认配置错误")
	}
	for _, content := range []string{"enabled=false\nbudget_percent=-1", "enabled=false\nbudget_percent=11", "enabled=true\ndir=''", "enabled=false\nbudget_percent=10.5", "enabled=false\nunknown='private-config-value'"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[knowledge]\n"+content+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, ""); err == nil || strings.Contains(err.Error(), "private-config-value") {
			t.Fatal("非法配置未拒绝或回显内容")
		}
	}
	for _, percent := range []int{0, 10} {
		k := cfg.Knowledge
		k.BudgetPercent = percent
		if err := k.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKnowledgeConfigDoesNotAccessDisabledDirectory(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://unit-only")
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "[knowledge]\nenabled=false\ndir='this-directory-must-not-be-read'\nbudget_percent=0\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, ""); err != nil {
		t.Fatal("关闭时尝试加载了目录", err)
	}
}
