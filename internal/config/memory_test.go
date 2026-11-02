package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadMemoryConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path, "")
}

// TestMemoryDefaults 记忆提炼默认开启、每 300 秒扫描、默认不提炼私密事件。
func TestMemoryDefaults(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Memory.Enabled || cfg.Memory.IntervalSeconds != 300 || cfg.Memory.AllowPrivate {
		t.Fatalf("记忆默认值不符：%+v", cfg.Memory)
	}
}

// TestMemoryOverrides 显式配置覆盖默认值。
func TestMemoryOverrides(t *testing.T) {
	cfg, err := loadMemoryConfig(t, "[memory]\nenabled=true\ninterval_seconds=60\nallow_private=true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Memory.Enabled || cfg.Memory.IntervalSeconds != 60 || !cfg.Memory.AllowPrivate {
		t.Fatalf("记忆配置未生效：%+v", cfg.Memory)
	}
}

// TestMemoryDisabledSkipsIntervalValidation 关闭后不启动 worker，也不再校验扫描间隔。
func TestMemoryDisabledSkipsIntervalValidation(t *testing.T) {
	cfg, err := loadMemoryConfig(t, "[memory]\nenabled=false\ninterval_seconds=0\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.Enabled || cfg.Memory.IntervalSeconds != 0 {
		t.Fatalf("关闭配置不符：%+v", cfg.Memory)
	}
}

// TestMemoryIntervalRange 启用时扫描间隔必须在允许范围内。
func TestMemoryIntervalRange(t *testing.T) {
	for _, interval := range []string{"9", "86401", "-1"} {
		t.Run(interval, func(t *testing.T) {
			_, err := loadMemoryConfig(t, "[memory]\nenabled=true\ninterval_seconds="+interval+"\n")
			if err == nil || !strings.Contains(err.Error(), "记忆提炼间隔") {
				t.Fatalf("interval=%s 未按范围拒绝：%v", interval, err)
			}
		})
	}
}
