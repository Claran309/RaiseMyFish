package config

import (
	"testing"
)

const proactiveLiveSection = `
[channels.live]
enabled = true

[collector.live]
room_id = "123"
self_id = "456"

[sender]
script_path = "tools/bili-whale/danmaku.mjs"
`

func loadProactive(t *testing.T, body string) (Config, error) {
	t.Helper()
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_LLM_KEY", "test-key")
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
	return loadWatchConfig(t, body)
}

func TestProactiveDefaultsOff(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proactive.Enabled || cfg.Proactive.IntervalSeconds != 20 || cfg.Proactive.MaxPerHour != 6 || cfg.Proactive.MinChangePercent != 5 {
		t.Fatalf("主动弹幕默认值不符：%+v", cfg.Proactive)
	}
}

func TestProactiveEnabledResolvesWatchStream(t *testing.T) {
	body := watchLLMSection + proactiveLiveSection + "\n[proactive]\nenabled = true\n"
	cfg, err := loadProactive(t, body)
	if err != nil {
		t.Fatalf("合法主动弹幕配置被拒绝：%v", err)
	}
	if !cfg.Proactive.Enabled || cfg.Proactive.IntervalSeconds != 20 || cfg.Proactive.MaxPerHour != 6 || cfg.Proactive.MinChangePercent != 5 {
		t.Fatalf("主动弹幕显式值未生效：%+v", cfg.Proactive)
	}
	// [watch] 未启用，但主动弹幕复用同一套截帧配置，凭据仍需解析。
	if cfg.Watch.Cookie != "SESSDATA=canary" {
		t.Fatal("主动弹幕启用时未解析B站凭据")
	}
}

func TestProactiveValidationRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"间隔过短", "\n[proactive]\nenabled = true\ninterval_seconds = 4\n"},
		{"间隔过长", "\n[proactive]\nenabled = true\ninterval_seconds = 3601\n"},
		{"每小时为0", "\n[proactive]\nenabled = true\nmax_per_hour = 0\n"},
		{"每小时超限", "\n[proactive]\nenabled = true\nmax_per_hour = 61\n"},
		{"变化阈值为负", "\n[proactive]\nenabled = true\nmin_change_percent = -1\n"},
		{"变化阈值超限", "\n[proactive]\nenabled = true\nmin_change_percent = 101\n"},
		{"未知字段", "\n[proactive]\nenabled = true\nfoo = 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadProactive(t, watchLLMSection+proactiveLiveSection+tc.body); err == nil {
				t.Fatal("非法主动弹幕配置未被拒绝")
			}
		})
	}
}

func TestProactiveDisabledIgnoresInvalidRange(t *testing.T) {
	body := watchLLMSection + proactiveLiveSection + "\n[proactive]\nenabled = false\ninterval_seconds = 1\nmax_per_hour = 0\n"
	cfg, err := loadProactive(t, body)
	if err != nil {
		t.Fatalf("关闭状态不应校验运行参数：%v", err)
	}
	if cfg.Proactive.Enabled {
		t.Fatal("主动弹幕应保持关闭")
	}
}

func TestProactiveRequiresRuntimeDependencies(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"缺少发送通道", watchLLMSection + "\n[channels.live]\nenabled = true\n[collector.live]\nroom_id = \"123\"\nself_id = \"456\"\n[proactive]\nenabled = true\n"},
		{"live未开启", watchLLMSection + "\n[collector.live]\nroom_id = \"123\"\nself_id = \"456\"\n[sender]\nscript_path = \"a.mjs\"\n[proactive]\nenabled = true\n"},
		{"LLM未启用", proactiveLiveSection + "\n[proactive]\nenabled = true\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadProactive(t, tc.body); err == nil {
				t.Fatal("缺少运行依赖的主动弹幕配置未被拒绝")
			}
		})
	}

	t.Run("缺少凭据", func(t *testing.T) {
		t.Setenv("FISH_DB_DSN", "postgres://fixture")
		t.Setenv("FISH_LLM_KEY", "test-key")
		t.Setenv("FISH_BILI_COOKIE", "")
		if _, err := loadWatchConfig(t, watchLLMSection+proactiveLiveSection+"\n[proactive]\nenabled = true\n"); err == nil {
			t.Fatal("缺少B站凭据未被拒绝")
		}
	})
}
