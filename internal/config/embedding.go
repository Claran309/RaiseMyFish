package config

import (
	"errors"
	"math"
	"strings"

	"RaiseMyFish/internal/embedding"
)

// Embedding 是知识库语义检索的嵌入服务配置（`[embedding]`）。
//
// 密钥只从 key_env 指向的环境变量（或本地 dotenv）解析，不写配置、不输出日志。
// 关闭时仍保留其余占位值，便于随时启用。
type Embedding struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
	Model   string `toml:"model"`
	KeyEnv  string `toml:"key_env"`
	// Key 由环境变量解析，禁止序列化。
	Key string `toml:"-" json:"-"`
	// Dimension 必须与 knowledge_embeddings.embedding 的 vector(1024) 一致。
	Dimension int `toml:"dimension"`
	BatchSize int `toml:"batch_size"`
	// Alpha 是混合召回里语义相似度的权重：score = 关键词档位 + alpha*sim。
	Alpha float64 `toml:"alpha"`
	// AutoRefresh 开启后 fishd 按 refresh_interval_seconds 检查知识库 revision，
	// 变化时增量 embedding。
	AutoRefresh            bool `toml:"auto_refresh"`
	RefreshIntervalSeconds int  `toml:"refresh_interval_seconds"`
}

func defaultEmbedding() Embedding {
	return Embedding{
		URL:                    "https://open.bigmodel.cn/api/paas/v4/embeddings",
		Model:                  "embedding-3",
		KeyEnv:                 "FISH_EMBEDDING_KEY",
		Dimension:              embedding.DefaultDimension,
		BatchSize:              32,
		Alpha:                  0.5,
		RefreshIntervalSeconds: 3600,
	}
}

// resolveEnvironment 只解析密钥；环境变量优先于本地 dotenv 由调用方统一处理。
func (e *Embedding) resolveEnvironment(lookup func(string) (string, bool)) {
	if name := strings.TrimSpace(e.KeyEnv); name != "" {
		e.Key, _ = lookup(name)
		e.Key = strings.TrimSpace(e.Key)
	}
}

// Validate 做静态校验；未启用时不要求密钥存在。
func (e Embedding) Validate() error {
	rawURL := strings.TrimSpace(e.URL)
	if rawURL == "" {
		if e.Enabled {
			return errors.New("embedding 启用需要配置 url")
		}
	} else if valid, _ := validEndpoint(rawURL); !valid {
		return errors.New("embedding 地址必须是HTTPS或本机HTTP地址")
	}
	if strings.TrimSpace(e.Model) == "" {
		return errors.New("embedding model 不能为空")
	}
	if e.Dimension != embedding.DefaultDimension {
		return errors.New("embedding dimension 必须为1024以匹配 knowledge_embeddings")
	}
	if e.BatchSize < 1 || e.BatchSize > 128 {
		return errors.New("embedding batch_size 必须在1至128之间")
	}
	if math.IsNaN(e.Alpha) || e.Alpha < 0 || e.Alpha > 1 {
		return errors.New("embedding alpha 必须在0至1之间")
	}
	if e.AutoRefresh && !e.Enabled {
		return errors.New("embedding 定时增量需要先启用 embedding")
	}
	if e.AutoRefresh && (e.RefreshIntervalSeconds < 60 || e.RefreshIntervalSeconds > 86400) {
		return errors.New("embedding 刷新间隔必须在60至86400秒之间")
	}
	if strings.ContainsAny(e.KeyEnv, "\r\n\x00") {
		return errors.New("embedding key_env 不能包含控制字符")
	}
	if e.Enabled {
		if strings.TrimSpace(e.KeyEnv) == "" {
			return errors.New("embedding 启用需要配置 key_env")
		}
		if e.Key == "" {
			return errors.New("embedding 密钥环境变量未解析")
		}
		if strings.ContainsAny(e.Key, "\r\n\x00") {
			return errors.New("embedding 密钥不能包含控制字符")
		}
	}
	return nil
}
