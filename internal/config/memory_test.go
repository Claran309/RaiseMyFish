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
	if cfg.Memory.Frozen {
		t.Fatalf("记忆默认不应冻结：%+v", cfg.Memory)
	}
}

// TestSoulPathDerivedFromPersonaDir 未填 soul_path 时跟随人格包，换 persona 自动换路径。
func TestSoulPathDerivedFromPersonaDir(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.SoulPath != "personas/dafeiyu/knowledge/soul-memories.md" {
		t.Errorf("默认 soul_path = %q", cfg.Memory.SoulPath)
	}

	cfg, err = loadMemoryConfig(t, "[llm]\npersona_dir=\"personas/other\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.SoulPath != "personas/other/knowledge/soul-memories.md" {
		t.Errorf("persona 派生 soul_path = %q", cfg.Memory.SoulPath)
	}
}

// TestSoulPathExplicitWins 显式配置优先于人格包派生。
func TestSoulPathExplicitWins(t *testing.T) {
	cfg, err := loadMemoryConfig(t, "[llm]\npersona_dir=\"personas/other\"\n[memory]\nsoul_path=\"custom/soul.md\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.SoulPath != "custom/soul.md" {
		t.Errorf("显式 soul_path 被覆盖：%q", cfg.Memory.SoulPath)
	}
}

// TestMemoryOverrides 显式配置覆盖默认值。
func TestMemoryOverrides(t *testing.T) {
	cfg, err := loadMemoryConfig(t, "[memory]\nenabled=true\ninterval_seconds=60\nallow_private=true\nfrozen=true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Memory.Enabled || cfg.Memory.IntervalSeconds != 60 || !cfg.Memory.AllowPrivate {
		t.Fatalf("记忆配置未生效：%+v", cfg.Memory)
	}
	if !cfg.Memory.Frozen {
		t.Fatalf("冻结配置未生效：%+v", cfg.Memory)
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

// TestMemoryOwnerUIDValidation owner_uid 是唯一身份证据，必须是干净 UID。
func TestMemoryOwnerUIDValidation(t *testing.T) {
	cfg, err := loadMemoryConfig(t, "[memory]\nenabled=true\nowner_uid=\"313591854\"\n")
	if err != nil {
		t.Fatalf("合法 owner_uid 被拒绝：%v", err)
	}
	if cfg.Memory.OwnerUID != "313591854" {
		t.Fatalf("owner_uid 未生效：%q", cfg.Memory.OwnerUID)
	}

	for _, bad := range []string{"a b", "a\\tb", strings.Repeat("9", 65)} {
		_, err := loadMemoryConfig(t, "[memory]\nenabled=true\nowner_uid=\""+bad+"\"\n")
		if err == nil || !strings.Contains(err.Error(), "owner_uid") {
			t.Fatalf("owner_uid=%q 未按非法 UID 拒绝：%v", bad, err)
		}
	}
}
