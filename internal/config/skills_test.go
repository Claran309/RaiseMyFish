package config

import (
	"testing"
)

func TestSkillsDefaultsAndValidation(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Skills.Enabled || cfg.Skills.Dir != "skills" || cfg.Skills.CheckSeconds != 30 || cfg.Skills.MaxTimeoutSec != 30 || cfg.Skills.MaxOutputBytes != 8000 {
		t.Fatalf("技能默认值不符：%+v", cfg.Skills)
	}
	body := "[skills]\nenabled=true\ndir='skills'\nallow_scripts=true\ncheck_seconds=30\nmax_timeout_seconds=30\nmax_output_bytes=8000\nallowed=['white-rice','dice']\n"
	cfg, err = loadWatchConfig(t, body)
	if err != nil {
		t.Fatalf("合法技能配置被拒绝：%v", err)
	}
	if !cfg.Skills.Enabled || !cfg.Skills.AllowScripts || len(cfg.Skills.Allowed) != 2 {
		t.Fatalf("技能配置未生效：%+v", cfg.Skills)
	}
	for _, bad := range []string{
		"[skills]\nenabled=true\ndir=''\n",
		"[skills]\nenabled=true\ncheck_seconds=0\n",
		"[skills]\nenabled=true\nmax_timeout_seconds=0\n",
		"[skills]\nenabled=true\nmax_output_bytes=10\n",
		"[skills]\nenabled=true\nallowed=['Bad Name']\n",
	} {
		if _, err := loadWatchConfig(t, bad); err == nil {
			t.Fatalf("无效技能配置未被拒绝：%q", bad)
		}
	}
}
