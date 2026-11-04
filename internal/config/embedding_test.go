package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadEmbeddingConfig(t *testing.T, body string) (Config, error) {
	t.Helper()
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	path := filepath.Join(t.TempDir(), "fishd.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path, "")
}

// TestEmbeddingDefaults 默认关闭、维度 1024、批量 32、混合权重 0.5、每小时刷新。
func TestEmbeddingDefaults(t *testing.T) {
	t.Setenv("FISH_DB_DSN", "postgres://fixture")
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	e := cfg.Embedding
	if e.Enabled || e.Dimension != 1024 || e.BatchSize != 32 || e.Alpha != 0.5 || e.AutoRefresh || e.RefreshIntervalSeconds != 3600 {
		t.Fatalf("embedding 默认值不符：%+v", e)
	}
	if e.Model != "embedding-3" || e.KeyEnv != "FISH_EMBEDDING_KEY" || e.URL == "" {
		t.Fatalf("embedding 默认服务参数不符：%+v", e)
	}
}

// TestEmbeddingResolvesKeyFromEnvironment 密钥只从 key_env 指向的环境变量读取。
func TestEmbeddingResolvesKeyFromEnvironment(t *testing.T) {
	t.Setenv("FISH_EMBEDDING_KEY", "embedding-secret-value")
	cfg, err := loadEmbeddingConfig(t, "[embedding]\nenabled=true\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Key != "embedding-secret-value" {
		t.Fatal("embedding 密钥未从环境变量解析")
	}
	encoded, err := json.Marshal(cfg.Embedding)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "embedding-secret-value") {
		t.Fatal("embedding 密钥被序列化输出")
	}
}

// TestEmbeddingEnabledRequiresKey 启用但环境变量缺失时拒绝，且错误不泄露密钥。
func TestEmbeddingEnabledRequiresKey(t *testing.T) {
	t.Setenv("FISH_EMBEDDING_KEY", "")
	_, err := loadEmbeddingConfig(t, "[embedding]\nenabled=true\n")
	if err == nil || !strings.Contains(err.Error(), "密钥") {
		t.Fatalf("缺密钥未被拒绝：%v", err)
	}
}

// TestEmbeddingDisabledSkipsKey 关闭时允许没有密钥。
func TestEmbeddingDisabledSkipsKey(t *testing.T) {
	t.Setenv("FISH_EMBEDDING_KEY", "")
	cfg, err := loadEmbeddingConfig(t, "[embedding]\nenabled=false\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Key != "" {
		t.Fatal("关闭时不应解析出密钥")
	}
}

// TestEmbeddingValidation 固定维度、批量、权重、刷新间隔与地址范围。
func TestEmbeddingValidation(t *testing.T) {
	t.Setenv("FISH_EMBEDDING_KEY", "secret")
	cases := []struct {
		name string
		body string
		want string
	}{
		{"维度", "[embedding]\nenabled=true\ndimension=768\n", "dimension"},
		{"批量", "[embedding]\nenabled=true\nbatch_size=0\n", "batch_size"},
		{"权重", "[embedding]\nenabled=true\nalpha=1.5\n", "alpha"},
		{"刷新关未启用", "[embedding]\nauto_refresh=true\n", "定时增量"},
		{"刷新间隔", "[knowledge]\nenabled=true\n[embedding]\nenabled=true\nauto_refresh=true\nrefresh_interval_seconds=10\n", "刷新间隔"},
		{"地址", "[embedding]\nenabled=true\nurl='http://example.com/embed'\n", "地址"},
		{"模型", "[embedding]\nenabled=true\nmodel=''\n", "model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadEmbeddingConfig(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("未按预期拒绝：%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("错误信息泄露密钥")
			}
		})
	}
}

// TestEmbeddingAutoRefreshRequiresKnowledge auto_refresh 依赖知识库启用。
func TestEmbeddingAutoRefreshRequiresKnowledge(t *testing.T) {
	t.Setenv("FISH_EMBEDDING_KEY", "secret")
	_, err := loadEmbeddingConfig(t, "[embedding]\nenabled=true\nauto_refresh=true\n")
	if err == nil || !strings.Contains(err.Error(), "知识库") {
		t.Fatalf("未启用知识库应拒绝定时增量：%v", err)
	}
}
