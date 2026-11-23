package config

import (
	"errors"
	"net/url"
	"strings"
)

// Observability 是本地可观测性开关；B2 trace 只写元数据，不引入 OTel 依赖。
type Observability struct {
	Trace Trace `toml:"trace"`
}

type Trace struct {
	Enabled  bool   `toml:"enabled"`
	Path     string `toml:"path"`
	MaxSpans int    `toml:"max_spans"`
	// OTLPEndpoint 为空表示只保留本地 trace；配置后按 OTLP/HTTP JSON 导出。
	OTLPEndpoint string `toml:"otlp_endpoint"`
	// ServiceName 是导出 resource 的 service.name。
	ServiceName string `toml:"service_name"`
	// OTLPTimeoutMS 是单次导出超时。
	OTLPTimeoutMS int `toml:"otlp_timeout_ms"`
}

func defaultObservability() Observability {
	return Observability{Trace: Trace{
		Enabled: false, Path: "logs/traces.jsonl", MaxSpans: 5000,
		ServiceName: "fishd", OTLPTimeoutMS: 3000,
	}}
}

func (t Trace) Validate() error {
	if !t.Enabled {
		return nil
	}
	if t.Path == "" {
		return errors.New("trace.path 不能为空")
	}
	if t.MaxSpans < 100 || t.MaxSpans > 200000 {
		return errors.New("trace.max_spans 需在100至200000")
	}
	if endpoint := strings.TrimSpace(t.OTLPEndpoint); endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("trace.otlp_endpoint 必须是 http/https URL")
		}
		if t.OTLPTimeoutMS < 100 || t.OTLPTimeoutMS > 30000 {
			return errors.New("trace.otlp_timeout_ms 需在100至30000")
		}
	}
	return nil
}
