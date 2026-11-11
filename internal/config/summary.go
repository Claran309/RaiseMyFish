package config

import "errors"

// Summary 是每日总结（`[summary]`）：默认关闭，开启后每天固定时刻
// （上海时区）汇总前一天的事件并生成日报。
type Summary struct {
	Enabled bool `toml:"enabled"`
	// Hour/Minute 是触发时刻（24 小时制）；默认 00:10，避开零点整的采集高峰。
	Hour   int `toml:"hour"`
	Minute int `toml:"minute"`
	// MaxEvents 是单次生成读取的事件上限；超出时保留最新的部分。
	MaxEvents int `toml:"max_events"`
	// MaxTokens 是日报模型输出的 token 上限。
	MaxTokens int `toml:"max_tokens"`
}

func defaultSummary() Summary {
	return Summary{Hour: 0, Minute: 10, MaxEvents: 300, MaxTokens: 900}
}

// Validate 校验调度时刻与预算范围；关闭时也校验，避免配置错误被静默忽略。
func (s Summary) Validate() error {
	if s.Hour < 0 || s.Hour > 23 {
		return errors.New("每日总结小时必须在0至23之间")
	}
	if s.Minute < 0 || s.Minute > 59 {
		return errors.New("每日总结分钟必须在0至59之间")
	}
	if s.MaxEvents < 20 || s.MaxEvents > 1000 {
		return errors.New("每日总结事件上限必须在20至1000之间")
	}
	if s.MaxTokens < 200 || s.MaxTokens > 4000 {
		return errors.New("每日总结输出上限必须在200至4000之间")
	}
	return nil
}
