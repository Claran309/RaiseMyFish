package config

// 本文件定义 `[reasoning]` 段：ReAct 思考链的开关与预算。

import (
	"fmt"
)

// Reasoning 是 ReAct 思考链配置。
//
// enabled 默认 true（派发单要求）；它只决定是否装配 ReAct，
// 真正能不能跑还取决于 [llm] 是否启用、路由是否存在。
type Reasoning struct {
	Enabled   bool `toml:"enabled"`
	MaxSteps  int  `toml:"max_steps"`
	TimeoutMS int  `toml:"timeout_ms"`
	// DefaultReact 默认 true：除短句（少于 6 个字符）外，所有弹幕都走 ReAct。
	// 置 false 回到旧两段式：只有命中召回词表才走 ReAct。
	DefaultReact bool `toml:"default_react"`
	// AllowSideEffectTools 是副作用工具（送礼/关注/点赞/私信/发动态）的总开关。
	// 默认 false：模型即使调用也只会收到「未授权」，不会真的动平台。
	AllowSideEffectTools bool `toml:"allow_side_effect_tools"`
}

// 预算上限刻意收得紧：ReAct 是给一条弹幕用的，不能拖成半分钟。
const (
	defaultReasoningMaxSteps  = 3
	defaultReasoningTimeoutMS = 8000
	maxReasoningSteps         = 10
	minReasoningTimeoutMS     = 1000
	maxReasoningTimeoutMS     = 10000 // 派发单要求「不能变成 30 秒」，硬顶 10 秒
)

// DefaultReasoning 返回默认 ReAct 配置。
func DefaultReasoning() Reasoning {
	return Reasoning{Enabled: true, MaxSteps: defaultReasoningMaxSteps, TimeoutMS: defaultReasoningTimeoutMS, DefaultReact: true}
}

// ApplyDefaults 只补零值。
func (r *Reasoning) ApplyDefaults() {
	if r == nil {
		return
	}
	if r.MaxSteps == 0 {
		r.MaxSteps = defaultReasoningMaxSteps
	}
	if r.TimeoutMS == 0 {
		r.TimeoutMS = defaultReasoningTimeoutMS
	}
}

// Validate 校验预算区间。
func (r Reasoning) Validate() error {
	if r.MaxSteps < 1 || r.MaxSteps > maxReasoningSteps {
		return fmt.Errorf("ReAct 步数需为1至%d", maxReasoningSteps)
	}
	if r.TimeoutMS < minReasoningTimeoutMS || r.TimeoutMS > maxReasoningTimeoutMS {
		return fmt.Errorf("ReAct 超时需为%d至%d毫秒", minReasoningTimeoutMS, maxReasoningTimeoutMS)
	}
	return nil
}
