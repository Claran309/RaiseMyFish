package config

import "errors"

// Memory 是记忆提炼 worker 的配置（`[memory]`）。
//
// 默认开启：记忆是增强能力，worker 启动失败只降级，不阻塞 fishd。
// allow_private 默认 false，私密事件必须由配置显式授权才会提炼。
type Memory struct {
	Enabled         bool `toml:"enabled"`
	IntervalSeconds int  `toml:"interval_seconds"`
	AllowPrivate    bool `toml:"allow_private"`
	// LLMExtract 决定是否对「值得跑」的事件用模型做语义提炼（其余走规则版）。
	LLMExtract bool `toml:"llm_extract"`
	// LLMExtractMaxPerHour 是每小时最多调用几次模型（限流，超出回落规则版）。
	LLMExtractMaxPerHour int `toml:"llm_extract_max_per_hour"`
}

func defaultMemory() Memory {
	return Memory{Enabled: true, IntervalSeconds: 300, LLMExtract: true, LLMExtractMaxPerHour: 30}
}

// Validate 只做静态校验；水位文件与数据库在装配阶段检查。
// 未启用时忽略其余字段，允许关闭后保留占位值。
func (m Memory) Validate() error {
	if !m.Enabled {
		return nil
	}
	if m.IntervalSeconds < 10 || m.IntervalSeconds > 86400 {
		return errors.New("记忆提炼间隔必须在10至86400秒之间")
	}
	if m.LLMExtract && (m.LLMExtractMaxPerHour < 1 || m.LLMExtractMaxPerHour > 1000) {
		return errors.New("LLM 提炼每小时上限必须在1至1000之间")
	}
	return nil
}
