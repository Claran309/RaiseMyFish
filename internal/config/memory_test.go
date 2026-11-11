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

// TestViewerSummaryInterval 默认每小时；0 关闭；启用时限制在 60-86400 秒。
func TestViewerSummaryInterval(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.ViewerSummaryIntervalSeconds != 3600 {
		t.Fatalf("观众侧写默认间隔不符：%d", cfg.Memory.ViewerSummaryIntervalSeconds)
	}

	cfg, err = loadMemoryConfig(t, "[memory]\nenabled=true\nviewer_summary_interval_seconds=7200\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.ViewerSummaryIntervalSeconds != 7200 {
		t.Fatalf("观众侧写间隔覆盖失败：%d", cfg.Memory.ViewerSummaryIntervalSeconds)
	}

	cfg, err = loadMemoryConfig(t, "[memory]\nenabled=true\nviewer_summary_interval_seconds=0\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.ViewerSummaryIntervalSeconds != 0 {
		t.Fatalf("0 应表示关闭：%d", cfg.Memory.ViewerSummaryIntervalSeconds)
	}

	for _, interval := range []string{"59", "86401", "-1"} {
		t.Run(interval, func(t *testing.T) {
			_, err := loadMemoryConfig(t, "[memory]\nenabled=true\nviewer_summary_interval_seconds="+interval+"\n")
			if err == nil || !strings.Contains(err.Error(), "观众侧写间隔") {
				t.Fatalf("interval=%s 未按范围拒绝：%v", interval, err)
			}
		})
	}
}

// TestViewerSummaryBudgetDefaults R09 默认小批试跑：每轮 10 位、每次回填 2 位、
// 新发言 3 条才更新、近期层 7 天、每日预算 50 位。
func TestViewerSummaryBudgetDefaults(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Memory
	if m.ViewerSummaryMaxPerRun != 10 || m.ViewerSummaryBackfillLimit != 2 ||
		m.ViewerSummaryMinNewMessages != 3 || m.ViewerSummaryRecentTTLSeconds != 604800 ||
		m.ViewerSummaryDailyViewers != 50 {
		t.Fatalf("观众侧写预算默认值不符：%+v", m)
	}

	cfg, err = loadMemoryConfig(t, "[memory]\nenabled=true\nviewer_summary_max_per_run=4\nviewer_summary_backfill_limit=0\n"+
		"viewer_summary_min_new_messages=5\nviewer_summary_recent_ttl_seconds=86400\nviewer_summary_daily_viewers=20\n")
	if err != nil {
		t.Fatal(err)
	}
	m = cfg.Memory
	if m.ViewerSummaryMaxPerRun != 4 || m.ViewerSummaryBackfillLimit != 0 ||
		m.ViewerSummaryMinNewMessages != 5 || m.ViewerSummaryRecentTTLSeconds != 86400 ||
		m.ViewerSummaryDailyViewers != 20 {
		t.Fatalf("观众侧写预算覆盖失败：%+v", m)
	}
}

// TestViewerSummaryBudgetRange 预算字段超出范围必须拒绝，避免误配成无限跑。
func TestViewerSummaryBudgetRange(t *testing.T) {
	cases := []struct {
		key   string
		bad   []string
		match string
	}{
		{"viewer_summary_max_per_run", []string{"0", "101", "-1"}, "每轮上限"},
		{"viewer_summary_backfill_limit", []string{"101", "-1"}, "回填上限"},
		{"viewer_summary_min_new_messages", []string{"0", "101", "-1"}, "触发消息数"},
		{"viewer_summary_recent_ttl_seconds", []string{"3599", "2592001", "-1"}, "近期层有效期"},
		{"viewer_summary_daily_viewers", []string{"0", "501", "-1"}, "每日观众预算"},
	}
	for _, tc := range cases {
		for _, value := range tc.bad {
			t.Run(tc.key+"="+value, func(t *testing.T) {
				_, err := loadMemoryConfig(t, "[memory]\nenabled=true\n"+tc.key+"="+value+"\n")
				if err == nil || !strings.Contains(err.Error(), tc.match) {
					t.Fatalf("%s=%s 未按范围拒绝：%v", tc.key, value, err)
				}
			})
		}
	}
}
