package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresenceDefaultsOff(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Presence.HeartbeatLikeSeconds != 0 {
		t.Fatalf("常驻点赞默认值应为0：%+v", cfg.Presence)
	}
}

func TestPresenceHeartbeatLikeConfig(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := filepath.Join(t.TempDir(), "presence.toml")
	if err := os.WriteFile(path, []byte("[presence]\nheartbeat_like_seconds = 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Presence.HeartbeatLikeSeconds != 30 {
		t.Fatalf("常驻点赞间隔未生效：%+v", cfg.Presence)
	}
}

func TestPresenceHeartbeatLikeValidation(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	for _, seconds := range []int{-1, 1, 9, 601} {
		t.Run(fmt.Sprintf("invalid-%d", seconds), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "presence.toml")
			body := fmt.Sprintf("[presence]\nheartbeat_like_seconds = %d\n", seconds)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path, ""); err == nil || !strings.Contains(err.Error(), "常驻点赞") {
				t.Fatalf("非法间隔 %d 未被拒绝：%v", seconds, err)
			}
		})
	}
	for _, seconds := range []int{0, 10, 600} {
		t.Run(fmt.Sprintf("valid-%d", seconds), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "presence.toml")
			body := fmt.Sprintf("[presence]\nheartbeat_like_seconds = %d\n", seconds)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path, "")
			if err != nil {
				t.Fatalf("合法间隔 %d 被拒绝：%v", seconds, err)
			}
			if cfg.Presence.HeartbeatLikeSeconds != seconds {
				t.Fatalf("常驻点赞间隔不符：%+v", cfg.Presence)
			}
		})
	}
}
