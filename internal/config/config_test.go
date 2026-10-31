package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPrecedenceAndDefaults(t *testing.T) {
	dir := t.TempDir()
	toml := filepath.Join(dir, "fishd.toml")
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(toml, []byte("[log]\nlevel='warn'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("FISH_DB_DSN='postgres://fish:private@localhost/fish'\nFISH_LOG_LEVEL=debug\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FISH_LOG_LEVEL", "error")
	t.Setenv("FISH_DB_DSN", "postgres://fish:process-secret@localhost/fish")
	cfg, err := Load(toml, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != "error" || cfg.Database.DSN != os.Getenv("FISH_DB_DSN") || cfg.Database.TimeoutSeconds != 5 {
		t.Fatalf("配置优先级或默认值错误")
	}
	if cfg.Runtime.HeartbeatSeconds != 5 || cfg.Runtime.StaleSeconds != 20 {
		t.Fatal("心跳默认值错误")
	}
}

func TestDotenvDoesNotMutateProcess(t *testing.T) {
	const key = "FISH_TEST_CONFIG_DSN"
	t.Setenv(key, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("[db]\ndsn_env='"+key+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte(key+"=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// 显式空环境变量也优先，不能悄悄回退到文件。
	_, err := Load(path, env)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("缺少DSN或脱敏行为不符：%v", err)
	}
	if os.Getenv(key) != "" {
		t.Fatal("加载污染了进程环境")
	}
}

func TestInvalidConfigurationIsSafe(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fish:private-secret@localhost/fish")
	for _, content := range []string{"[log]\nlevel='private-secret'", "[db]\ntimeout_seconds=0", "[channels.live]\nenabled=true", "[runtime]\nheartbeat_seconds=25\nstale_seconds=20", "unknown='private-secret'", "[log]\nlevel=\"private-secret"} {
		t.Run(content, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "fishd.toml")
			if err := os.WriteFile(p, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p, "")
			if err == nil || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("错误缺失或含敏感值：%v", err)
			}
		})
	}
}

func TestDotenvFallbackAndMissingOptionalFile(t *testing.T) {
	key := "FISH_CONFIG_FILE_ONLY_DSN"
	if _, exists := os.LookupEnv(key); exists {
		t.Fatal("测试变量已被占用")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "fishd.toml")
	e := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("[db]\ndsn_env='"+key+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e, []byte(key+"=postgres://file-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, e)
	if err != nil || cfg.Database.DSN != "postgres://file-only" {
		t.Fatalf("文件回退失败：%v", err)
	}
	if _, exists := os.LookupEnv(key); exists {
		t.Fatal("进程环境被修改")
	}
	t.Setenv("FISH_DB_DSN", "postgres://from-env")
	if _, err := Load("", filepath.Join(dir, "absent")); err != nil {
		t.Fatal(err)
	}
}
