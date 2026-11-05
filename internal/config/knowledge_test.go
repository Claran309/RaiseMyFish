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
	if cfg.Knowledge.MemeLearn || cfg.Knowledge.MemeLearnMaxPerDay != 0 || cfg.Knowledge.MemeLearnHotLimit != 15 {
		t.Fatal("梗白学习默认配置错误")
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
	for _, k := range []Knowledge{
		{Dir: "personas", Enabled: false, MemeLearn: true, MemeLearnMaxPerDay: 10, MemeLearnHotLimit: 15},
		{Dir: "personas", Enabled: true, MemeLearn: true, MemeLearnMaxPerDay: -1, MemeLearnHotLimit: 15},
		{Dir: "personas", Enabled: true, MemeLearn: true, MemeLearnMaxPerDay: 101, MemeLearnHotLimit: 15},
		{Dir: "personas", Enabled: true, MemeLearn: true, MemeLearnMaxPerDay: 0, MemeLearnHotLimit: 0},
		{Dir: "personas", Enabled: true, MemeLearn: true, MemeLearnMaxPerDay: 0, MemeLearnHotLimit: 51},
	} {
		if err := k.Validate(); err == nil {
			t.Fatalf("梗白学习非法配置未拒绝：%+v", k)
		}
	}
	if err := (Knowledge{Dir: "personas", Enabled: true, MemeLearn: true, MemeLearnMaxPerDay: 0, MemeLearnHotLimit: 15}).Validate(); err != nil {
		t.Fatal(err)
	}

	// 新配置项可被解析：hot_limit 覆盖默认值，每日上限 0 表示不限。
	path := filepath.Join(t.TempDir(), "knowledge.toml")
	body := "[knowledge]\nenabled=false\ndir='personas'\nmeme_learn=false\nmeme_learn_hot_limit=20\nmeme_learn_max_per_day=0\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Knowledge.MemeLearnHotLimit != 20 || loaded.Knowledge.MemeLearnMaxPerDay != 0 {
		t.Fatalf("梗白学习配置解析不符：%+v", loaded.Knowledge)
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
