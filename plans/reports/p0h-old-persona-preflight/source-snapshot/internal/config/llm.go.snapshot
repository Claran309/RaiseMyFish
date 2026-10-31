package config

import (
	"errors"
	"math"
	"net"
	"net/url"
	"strings"
)

// LLM 管理Provider/routing；启用仅装配Brain，不启用尚未实现的通道。
type LLM struct {
	Enabled          bool                `toml:"enabled"`
	PersonaDir       string              `toml:"persona_dir"`
	Providers        map[string]Provider `toml:"providers"`
	Routing          map[string]string   `toml:"routing"`
	MaxPromptTokens  int                 `toml:"max_prompt_tokens"`
	MaxReplyTokens   int                 `toml:"max_reply_tokens"`
	AttemptTimeoutMS int                 `toml:"attempt_timeout_ms"`
	TotalTimeoutMS   int                 `toml:"total_timeout_ms"`
	BackoffMS        int                 `toml:"backoff_ms"`
	MaxConcurrent    int                 `toml:"max_concurrent"`
	Temperature      float64             `toml:"temperature"`
}

// Provider 的密钥只从本地环境解析，不写入TOML或JSON输出。
type Provider struct {
	Type      string           `toml:"type"`
	BaseURL   string           `toml:"base_url"`
	APIKeyEnv string           `toml:"api_key_env"`
	APIKey    string           `toml:"-" json:"-"`
	Models    []string         `toml:"models"`
	Prices    map[string]Price `toml:"prices"`
}

// Price 的空指针表示未配置报价，不是免费。
type Price struct {
	InputUSDPerMillion  *float64 `toml:"input_usd_per_million"`
	OutputUSDPerMillion *float64 `toml:"output_usd_per_million"`
}

func defaultLLM() LLM {
	return LLM{PersonaDir: "personas/dafeiyu", MaxPromptTokens: 8192, MaxReplyTokens: 128, AttemptTimeoutMS: 10000, TotalTimeoutMS: 40000, BackoffMS: 250, MaxConcurrent: 4, Temperature: .7}
}

func (l *LLM) resolveEnvironment(lookup func(string) (string, bool)) {
	for name, p := range l.Providers {
		p.APIKey, _ = lookup(p.APIKeyEnv)
		l.Providers[name] = p
	}
	defaultName, _, ok := strings.Cut(l.Routing["default"], ":")
	if !ok {
		return
	}
	p, exists := l.Providers[defaultName]
	if !exists {
		return
	}
	if value, ok := lookup("FISH_LLM_BASE_URL"); ok {
		p.BaseURL = value
		l.Providers[defaultName] = p
	}
	if value, ok := lookup("FISH_LLM_MODEL"); ok {
		l.Routing["default"] = defaultName + ":" + value
	}
}

func validEndpoint(raw string) (bool, bool) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false, false
	}
	local := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	return u.Scheme == "https" || (u.Scheme == "http" && local), local
}
func hasModel(p Provider, model string) bool {
	for _, item := range p.Models {
		if item == model {
			return true
		}
	}
	return false
}

// Resolve 解析具名路由，不猜测或静默切换Provider。
func (l LLM) Resolve(route string) (string, Provider, string, error) {
	if !l.Enabled {
		return "", Provider{}, "", errors.New("LLM尚未启用")
	}
	ref, ok := l.Routing[route]
	if !ok {
		return "", Provider{}, "", errors.New("LLM路由未定义")
	}
	name, model, ok := strings.Cut(ref, ":")
	p, exists := l.Providers[name]
	if !ok || !exists || !hasModel(p, model) {
		return "", Provider{}, "", errors.New("LLM路由目标未定义")
	}
	return name, p, model, nil
}

// Validate 只在明确启用时要求端点/密钥，基础设施启动不会强迫读取模型。
func (l LLM) Validate() error {
	if !l.Enabled {
		return nil
	}
	if strings.TrimSpace(l.PersonaDir) == "" || l.MaxPromptTokens < 1 || l.MaxPromptTokens > 1000000 || l.MaxReplyTokens < 1 || l.MaxReplyTokens > 8192 || l.AttemptTimeoutMS < 1 || l.AttemptTimeoutMS > 60000 || l.TotalTimeoutMS < 1 || l.TotalTimeoutMS > 120000 || l.BackoffMS < 1 || l.BackoffMS > 2000 || l.MaxConcurrent < 1 || l.MaxConcurrent > 64 || math.IsNaN(l.Temperature) || math.IsInf(l.Temperature, 0) || l.Temperature < 0 || l.Temperature > 2 {
		return errors.New("LLM预算、超时、并发或人格路径无效")
	}
	if len(l.Providers) == 0 || len(l.Routing) == 0 {
		return errors.New("LLM启用需要Provider和路由")
	}
	for name, p := range l.Providers {
		valid, local := validEndpoint(p.BaseURL)
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, ":\r\n") || p.Type != "openai" || !valid || len(p.Models) == 0 || strings.ContainsAny(p.APIKey, "\r\n") || (!local && strings.TrimSpace(p.APIKey) == "") {
			return errors.New("LLM Provider类型、端点、密钥或模型配置无效")
		}
		seen := make(map[string]bool, len(p.Models))
		for _, model := range p.Models {
			if strings.TrimSpace(model) == "" || seen[model] {
				return errors.New("LLM模型清单为空或重复")
			}
			seen[model] = true
		}
		for model, price := range p.Prices {
			if !hasModel(p, model) || (price.InputUSDPerMillion == nil) != (price.OutputUSDPerMillion == nil) {
				return errors.New("LLM报价必须对应模型且同时配置输入/输出")
			}
			for _, v := range []*float64{price.InputUSDPerMillion, price.OutputUSDPerMillion} {
				if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
					return errors.New("LLM报价必须是非负有限数")
				}
			}
		}
	}
	if _, _, _, err := l.Resolve("default"); err != nil {
		return err
	}
	for key := range l.Routing {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
			return errors.New("LLM路由名无效")
		}
		if _, _, _, err := l.Resolve(key); err != nil {
			return err
		}
	}
	return nil
}
