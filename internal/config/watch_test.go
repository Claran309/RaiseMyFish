package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const watchLLMSection = `
[llm]
enabled = true

[llm.providers.deepseek]
type = "openai"
base_url = "https://api.deepseek.com/v1"
api_key_env = "FISH_LLM_KEY"
models = ["deepseek-chat"]

[llm.routing]
default = "deepseek:deepseek-chat"
`

func loadWatchConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "watch.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path, "")
}

func setWatchEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	t.Setenv("FISH_LLM_KEY", "test-key")
	t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
}

func TestWatchDefaultsOff(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Watch.Enabled || cfg.Watch.Source != "stream" || cfg.Watch.IntervalSeconds != 600 || cfg.Watch.Seconds != 10 || cfg.Watch.Frames != 4 {
		t.Fatalf("看直播默认值不符：%+v", cfg.Watch)
	}
	if cfg.Watch.ASRURL != "http://127.0.0.1:8077/v1/audio/transcriptions" || cfg.Watch.StreamQuality != "origin" || cfg.Watch.LLMRoute != "default" {
		t.Fatalf("看直播默认地址/清晰度/路由不符：%+v", cfg.Watch)
	}
	if cfg.Watch.Cookie != "" {
		t.Fatal("未启用时不应解析凭据")
	}
	if cfg.Watch.CurrentRoom != "" || cfg.Watch.StreamerName != "" {
		t.Fatalf("状态注入默认应为空：%+v", cfg.Watch)
	}
}

func TestWatchIdentityConfig(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := loadWatchConfig(t, "[watch]\ncurrent_room = \"1992099976\"\nstreamer_name = \"娇娇\"\n")
	if err != nil {
		t.Fatalf("合法状态注入配置被拒绝：%v", err)
	}
	if cfg.Watch.CurrentRoom != "1992099976" || cfg.Watch.StreamerName != "娇娇" {
		t.Fatalf("状态注入配置未生效：%+v", cfg.Watch)
	}
}

func TestWatchIdentityConfigValidation(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cases := []struct {
		name string
		body string
	}{
		{"房间非数字", "[watch]\ncurrent_room = \"room-1\"\n"},
		{"房间全零", "[watch]\ncurrent_room = \"000\"\n"},
		{"房间过长", "[watch]\ncurrent_room = \"123456789012345678901\"\n"},
		{"主播名含换行", "[watch]\nstreamer_name = \"娇\\n娇\"\n"},
		{"主播名含标签", "[watch]\nstreamer_name = \"<娇娇>\"\n"},
		{"主播名过长", "[watch]\nstreamer_name = \"" + strings.Repeat("鱼", 33) + "\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadWatchConfig(t, tc.body); err == nil {
				t.Fatal("非法状态注入配置未被拒绝")
			}
		})
	}
}

func TestWatchEnabledResolvesCookiePrivately(t *testing.T) {
	setWatchEnv(t)
	cfg, err := loadWatchConfig(t, watchLLMSection+"\n[watch]\nenabled = true\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Watch.Cookie != "SESSDATA=canary" {
		t.Fatal("未从cookie_env解析凭据")
	}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), "canary") {
		t.Fatal("序列化泄露看直播凭据")
	}
}

func TestWatchCustomCookieEnv(t *testing.T) {
	setWatchEnv(t)
	t.Setenv("MY_WATCH_COOKIE", "SESSDATA=custom")
	cfg, err := loadWatchConfig(t, watchLLMSection+"\n[watch]\nenabled = true\ncookie_env = \"MY_WATCH_COOKIE\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Watch.Cookie != "SESSDATA=custom" {
		t.Fatal("自定义cookie_env未生效")
	}
}

func TestWatchInvalidConfigIsSafe(t *testing.T) {
	cases := []struct {
		name string
		body string
		env  func(*testing.T)
	}{
		{"帧数超限", "[watch]\nenabled=true\nframes=6\n", setWatchEnv},
		{"screen未实现", "[watch]\nenabled=true\nsource=\"screen\"\n", setWatchEnv},
		{"秒数过短", "[watch]\nenabled=true\nseconds=1\n", setWatchEnv},
		{"间隔过短", "[watch]\nenabled=true\ninterval_seconds=1\n", setWatchEnv},
		{"间隔过长", "[watch]\nenabled=true\ninterval_seconds=86401\n", setWatchEnv},
		{"ASR非本机HTTP", "[watch]\nenabled=true\nasr_url=\"http://example.com/transcribe\"\n", setWatchEnv},
		{"未知清晰度", "[watch]\nenabled=true\nstream_quality=\"4k\"\n", setWatchEnv},
		{"未知字段", "[watch]\nenabled=true\nfoo=1\n", setWatchEnv},
		{"环境变量名非法", "[watch]\nenabled=true\ncookie_env=\"BAD-NAME\"\n", setWatchEnv},
		{"缺少凭据", "[watch]\nenabled=true\n", func(t *testing.T) {
			setWatchEnv(t)
			t.Setenv("FISH_BILI_COOKIE", "")
		}},
		{"LLM未启用", "[watch]\nenabled=true\n", func(t *testing.T) {
			t.Setenv("FISH_DB_DSN", "postgres://fixture")
			t.Setenv("FISH_BILI_COOKIE", "SESSDATA=canary")
		}},
		{"路由不存在", "[watch]\nenabled=true\nllm_route=\"story\"\n", setWatchEnv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.env(t)
			body := watchLLMSection + "\n" + tc.body
			if tc.name == "LLM未启用" {
				body = tc.body
			}
			_, err := loadWatchConfig(t, body)
			if err == nil {
				t.Fatal("无效看直播配置未被拒绝")
			}
			if strings.Contains(err.Error(), "canary") {
				t.Fatal("错误泄露凭据")
			}
		})
	}
}

// TestWatchASRContainerHost 容器内可用宿主机别名访问 ASR；相似域名/网关 IP 仍拒绝。
func TestWatchASRContainerHost(t *testing.T) {
	setWatchEnv(t)
	raw := "http://host.docker.internal:8077/v1/audio/transcriptions"
	cfg, err := loadWatchConfig(t, watchLLMSection+"\n[watch]\nenabled=true\nasr_url=\""+raw+"\"\n")
	if err != nil {
		t.Fatalf("host.docker.internal 的 ASR 地址应被接受：%v", err)
	}
	if cfg.Watch.ASRURL != raw {
		t.Fatalf("asr_url 未生效：%q", cfg.Watch.ASRURL)
	}
	for _, bad := range []string{
		"http://host.containers.internal:8077/v1/audio/transcriptions",
		"http://host.docker.internal.:8077/v1/audio/transcriptions",
		"http://192.168.65.254:8077/v1/audio/transcriptions",
	} {
		body := watchLLMSection + "\n[watch]\nenabled=true\nasr_url=\"" + bad + "\"\n"
		if _, err := loadWatchConfig(t, body); err == nil {
			t.Errorf("ASR 地址 %q 应被拒绝", bad)
		}
	}
}

func TestWatchDisabledIgnoresInvalidFields(t *testing.T) {
	// 默认关闭时，不因未配置凭据/LLM而拒绝启动；字段仍会做结构校验。
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	if _, err := loadWatchConfig(t, "[watch]\nenabled=false\nseconds=10\nframes=4\n"); err != nil {
		t.Fatalf("关闭状态不应校验运行时凭据：%v", err)
	}
}
