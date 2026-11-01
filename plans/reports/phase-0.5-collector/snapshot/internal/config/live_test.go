package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveDefaultOffAndEnvironmentPrivate(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Channels.Live.Enabled || cfg.Collector.Live.NodePath != "node" {
		t.Fatal("默认启动了采集或缺运行时默认值")
	}
	path := filepath.Join(t.TempDir(), "live.toml")
	if err := os.WriteFile(path, []byte("[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\nself_id='8'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=credential-canary")
	cfg, err = Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collector.Live.Cookie == "" {
		t.Fatal("没解析显式凭据")
	}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), "credential-canary") {
		t.Fatal("序列化泄露采集凭据")
	}
}
func TestLiveConfigInvalidSafeAndFileNotRead(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_BILI_COOKIE", "")
	t.Setenv("FISH_BILI_COOKIE_FILE", "not-existing-private-canary.json")
	base := "[channels.live]\nenabled=true\n[collector.live]\nroom_id='1'\nself_id='8'\n"
	for _, suffix := range []string{"idle_ms=30000\n", "max_failures=0\n", "heartbeat_ms=0\n"} {
		p := filepath.Join(t.TempDir(), "invalid.toml")
		if err := os.WriteFile(p, []byte(base+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p, "")
		if err == nil || strings.Contains(err.Error(), "canary") {
			t.Fatal("无效配置未安全拒绝")
		}
	}
	for _, id := range []string{"invalid", "0", "", "8:9"} {
		p := filepath.Join(t.TempDir(), "bad-id.toml")
		text := strings.Replace(base, "room_id='1'", "room_id='"+id+"'", 1)
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, ""); err == nil {
			t.Fatal("接受无效房间ID")
		}
	}
	p := filepath.Join(t.TempDir(), "valid.toml")
	if err := os.WriteFile(p, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, ""); err != nil {
		t.Fatal("纯配置校验提前读取凭据文件", err)
	}
}
