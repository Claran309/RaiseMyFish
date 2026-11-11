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
	// AllowDanmakuTool 是 live 的 send_danmaku 工具开关：开启后模型可在一条事件里
	// 直接发 1-3 条弹幕（发满后不再发普通回复）。默认 false，显式开启才装配。
	AllowDanmakuTool bool `toml:"allow_danmaku_tool"`
	// Comment/DM 是评论与私信的独立预算：两者没有直播弹幕的实时节奏约束，
	// 允许更长思考（派发单：5-10 步 / 30-60 秒），但弹幕仍用上面的 3 步 / 8 秒。
	Comment ReasoningChannel `toml:"comment"`
	DM      ReasoningChannel `toml:"dm"`
}

// ReasoningChannel 是单个通道的 ReAct 预算覆盖。
type ReasoningChannel struct {
	MaxSteps  int `toml:"max_steps"`
	TimeoutMS int `toml:"timeout_ms"`
}

// 弹幕预算刻意收得紧：ReAct 是给一条弹幕用的，不能拖成半分钟。
const (
	defaultReasoningMaxSteps  = 3
	defaultReasoningTimeoutMS = 8000
	maxReasoningSteps         = 10
	minReasoningTimeoutMS     = 1000
	maxReasoningTimeoutMS     = 10000 // 派发单要求「不能变成 30 秒」，硬顶 10 秒
)

// 评论/私信允许更长思考；默认取区间中位，仍受父级 worker 超时约束。
const (
	defaultLongReasoningMaxSteps  = 5
	defaultLongReasoningTimeoutMS = 45000
	minLongReasoningMaxSteps      = 5
	maxLongReasoningMaxSteps      = 10
	minLongReasoningTimeoutMS     = 30000
	maxLongReasoningTimeoutMS     = 60000
)

// DefaultReasoning 返回默认 ReAct 配置。
func DefaultReasoning() Reasoning {
	return Reasoning{
		Enabled:      true,
		MaxSteps:     defaultReasoningMaxSteps,
		TimeoutMS:    defaultReasoningTimeoutMS,
		DefaultReact: true,
		Comment:      ReasoningChannel{MaxSteps: defaultLongReasoningMaxSteps, TimeoutMS: defaultLongReasoningTimeoutMS},
		DM:           ReasoningChannel{MaxSteps: defaultLongReasoningMaxSteps, TimeoutMS: defaultLongReasoningTimeoutMS},
	}
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
	r.Comment.applyDefaults()
	r.DM.applyDefaults()
}

func (c *ReasoningChannel) applyDefaults() {
	if c.MaxSteps == 0 {
		c.MaxSteps = defaultLongReasoningMaxSteps
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = defaultLongReasoningTimeoutMS
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
	if err := r.Comment.validate("评论"); err != nil {
		return err
	}
	return r.DM.validate("私信")
}

func (c ReasoningChannel) validate(label string) error {
	if c.MaxSteps < minLongReasoningMaxSteps || c.MaxSteps > maxLongReasoningMaxSteps {
		return fmt.Errorf("%s ReAct 步数需为%d至%d", label, minLongReasoningMaxSteps, maxLongReasoningMaxSteps)
	}
	if c.TimeoutMS < minLongReasoningTimeoutMS || c.TimeoutMS > maxLongReasoningTimeoutMS {
		return fmt.Errorf("%s ReAct 超时需为%d至%d毫秒", label, minLongReasoningTimeoutMS, maxLongReasoningTimeoutMS)
	}
	return nil
}
