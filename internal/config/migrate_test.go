package config

// 本文件测 migrate 子命令的最小配置校验：
// 迁移只建表，不能因为通道开关/B站凭据/LLM 密钥缺失而挡住首次部署。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadMigrateConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("写配置失败：%v", err)
	}
	return LoadForMigrate(path, "")
}

// TestLoadForMigrateSkipsRuntimeCredentials 迁移放行运行期凭据缺失，CLI 加载仍拒绝。
func TestLoadForMigrateSkipsRuntimeCredentials(t *testing.T) {
	clearBiliCredentialEnv(t)
	t.Setenv("FISH_LLM_KEY", "")
	t.Setenv("FISH_EMBEDDING_KEY", "")
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	body := "[channels.live]\nenabled=true\n" +
		"[channels.comment]\nenabled=true\n" +
		"[collector.live]\nroom_id='1'\n" +
		"[collector.comment]\nenabled=true\nvideos=['1']\n"
	cfg, err := loadMigrateConfig(t, body)
	if err != nil {
		t.Fatalf("迁移不应要求运行期凭据：%v", err)
	}
	if cfg.Database.DSN == "" {
		t.Fatal("迁移加载应解析出数据库连接串")
	}
	// 同一份配置走 CLI 加载仍要求评论采集凭据，证明差异来自迁移专用校验。
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadForCLI(path, ""); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("CLI 加载应拒绝缺失的B站凭据，实际 %v", err)
	}
}

// TestLoadForMigrateRequiresDatabase 没有数据库连接串时迁移必须失败。
func TestLoadForMigrateRequiresDatabase(t *testing.T) {
	clearBiliCredentialEnv(t)
	t.Setenv("FISH_DB_DSN", "")
	if _, err := loadMigrateConfig(t, "[log]\nlevel='info'\n"); err == nil || !strings.Contains(err.Error(), "数据库") {
		t.Fatalf("缺数据库连接串应拒绝，实际 %v", err)
	}
}

// TestLoadForMigrateValidatesLogLevel 最小校验仍检查日志级别与目录。
func TestLoadForMigrateValidatesLogLevel(t *testing.T) {
	clearBiliCredentialEnv(t)
	t.Setenv("FISH_DB_DSN", "postgres://fish:secret@localhost/fish")
	if _, err := loadMigrateConfig(t, "[log]\nlevel='loud'\n"); err == nil || !strings.Contains(err.Error(), "日志级别") {
		t.Fatalf("非法日志级别应拒绝，实际 %v", err)
	}
	if _, err := loadMigrateConfig(t, "[log]\ndir=''\n"); err == nil || !strings.Contains(err.Error(), "日志目录") {
		t.Fatalf("空日志目录应拒绝，实际 %v", err)
	}
}
