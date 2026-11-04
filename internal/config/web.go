package config

import (
	"errors"
	"os"
	"strings"
)

// Web 是 ReAct 联网工具的外部服务配置（`[web]`）。
// search_url 为空表示未配置搜索源，工具会明确回复「未配置」而不是编造结果。
type Web struct {
	SearchURL string `toml:"search_url"`
	// SearchKey 作为 Authorization: Bearer，不序列化、不记日志。
	SearchKey string `toml:"search_key" json:"-"`
	// SearchKeyEnv 是读 key 的环境变量名，优先于 SearchKey（推荐，避免明文落盘）。
	SearchKeyEnv string `toml:"search_key_env" json:"-"`
}

func defaultWeb() Web { return Web{} }

// ResolveKey 返回实际使用的 key：环境变量优先，其次字面值（兼容旧配置）。
func (w Web) ResolveKey() string {
	if name := strings.TrimSpace(w.SearchKeyEnv); name != "" {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return strings.TrimSpace(w.SearchKey)
}

// Validate 只校验已配置的搜索源；空地址合法。
func (w Web) Validate() error {
	raw := strings.TrimSpace(w.SearchURL)
	if raw != "" {
		if valid, _ := validEndpoint(raw); !valid {
			return errors.New("搜索服务地址必须是HTTPS或本机HTTP地址")
		}
	}
	if strings.ContainsAny(w.SearchKey, "\r\n\x00") || strings.ContainsAny(w.SearchKeyEnv, "\r\n\x00") {
		return errors.New("搜索服务密钥不能包含控制字符")
	}
	return nil
}
