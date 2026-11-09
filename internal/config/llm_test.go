package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testLLMConfig = `
[llm]
enabled=true
persona_dir="personas/dafeiyu"
[llm.providers.fake]
type="openai"
base_url="https://example.com/v1"
api_key_env="FISH_TEST_LLM_KEY"
models=["small","large"]
[llm.providers.fake.prices.small]
input_usd_per_million=0.1
output_usd_per_million=0.2
[llm.routing]
default="fake:small"
live="fake:small"
`

func TestLLMConfigResolutionAndEnvironment(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://unused")
	t.Setenv("FISH_TEST_LLM_KEY", "not-real-secret")
	t.Setenv("FISH_LLM_MODEL", "large")
	t.Setenv("FISH_LLM_BASE_URL", "http://127.0.0.1:1234/v1")
	p := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(p, []byte(testLLMConfig), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	name, provider, model, err := cfg.LLM.Resolve("default")
	if err != nil {
		t.Fatal(err)
	}
	if name != "fake" || model != "large" || provider.APIKey != "not-real-secret" || provider.BaseURL != "http://127.0.0.1:1234/v1" || cfg.LLM.MaxPromptTokens != 8192 {
		t.Fatal("路由/环境覆盖/默认值错误")
	}
	_, _, model, err = cfg.LLM.Resolve("live")
	if err != nil || model != "small" {
		t.Fatal("环境model覆盖不应改写专门路由")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "not-real-secret") {
		t.Fatal("序列化回显密钥")
	}
	if _, _, _, err := cfg.LLM.Resolve("unknown"); err == nil {
		t.Fatal("未知路由未报错")
	}
}

func TestLLMInvalidConfigurationDoesNotEchoValues(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://unused")
	t.Setenv("FISH_TEST_LLM_KEY", "not-real-secret")
	for _, content := range []string{
		strings.ReplaceAll(testLLMConfig, "fake:small", "missing:small"),
		strings.ReplaceAll(testLLMConfig, "fake:small", "fake:missing"),
		strings.ReplaceAll(testLLMConfig, "https://example.com/v1", "http://example.com/secret"),
		// host.docker.internal 的窄授权只覆盖 ASR/搜索；LLM 仍须 HTTPS 或回环。
		strings.ReplaceAll(testLLMConfig, "https://example.com/v1", "http://host.docker.internal/v1"),
		strings.ReplaceAll(testLLMConfig, "https://example.com/v1", "http://host.containers.internal/v1"),
		strings.ReplaceAll(testLLMConfig, "input_usd_per_million=0.1", "input_usd_per_million=-1"),
		strings.ReplaceAll(testLLMConfig, "output_usd_per_million=0.2", ""),
		strings.ReplaceAll(testLLMConfig, "enabled=true", "enabled=true\nattempt_timeout_ms=0"),
		strings.ReplaceAll(testLLMConfig, "enabled=true", "enabled=true\nmax_concurrent=0"),
	} {
		p := filepath.Join(t.TempDir(), "fishd.toml")
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, ""); err == nil || strings.Contains(err.Error(), "not-real-secret") {
			t.Fatalf("无效LLM配置未安全拒绝：%v", err)
		}
	}
}
