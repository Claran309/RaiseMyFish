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
	// NativeTools 是原生 tool calling 模式："off"（默认，走文本 ReAct）、
	// "on"（走 native tools）、"auto"（预留：按 Provider 能力探测）。
	NativeTools string `toml:"native_tools"`
	// ReflectBudget 是每条事件允许的自检次数；默认 1，-1 表示关闭。
	ReflectBudget int `toml:"reflect_budget"`
	// 评论/私信默认不反思；显式开启后按长度/工具/问答规则触发。
	ReflectComment bool `toml:"reflect_comment"`
	ReflectDM      bool `toml:"reflect_dm"`
	// ReflectMinRunes 是触发反思的最小草稿长度（字符）。
	ReflectMinRunes int `toml:"reflect_min_runes"`
	// ReflectOnTools 表示本轮用过工具就触发反思。
	ReflectOnTools bool `toml:"reflect_on_tools"`
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
	maxReasoningTimeoutMS     = 20000 // 弹幕预算 4 步/12 秒（原 3 步/8 秒）；硬顶 20 秒防止拖成半分钟
	// 原生 tool calling 默认关闭：先保留已验收的文本 ReAct，显式开启再切换。
	defaultNativeTools = "off"
	// Reflection 默认允许一次；显式 -1 关闭。
	defaultReflectBudget   = 1
	defaultReflectMinRunes = 60
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
		Enabled:         true,
		MaxSteps:        defaultReasoningMaxSteps,
		TimeoutMS:       defaultReasoningTimeoutMS,
		DefaultReact:    true,
		NativeTools:     defaultNativeTools,
		ReflectBudget:   defaultReflectBudget,
		ReflectMinRunes: defaultReflectMinRunes,
		ReflectOnTools:  true,
		Comment:         ReasoningChannel{MaxSteps: defaultLongReasoningMaxSteps, TimeoutMS: defaultLongReasoningTimeoutMS},
		DM:              ReasoningChannel{MaxSteps: defaultLongReasoningMaxSteps, TimeoutMS: defaultLongReasoningTimeoutMS},
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
	if r.NativeTools == "" {
		r.NativeTools = defaultNativeTools
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
	switch r.NativeTools {
	case "", "off", "on", "auto":
	default:
		return fmt.Errorf("reasoning.native_tools 只能是 off/on/auto")
	}
	if r.ReflectBudget < -1 || r.ReflectBudget > 1 {
		return fmt.Errorf("reasoning.reflect_budget 只能是 -1/0/1")
	}
	if r.ReflectMinRunes < 0 || r.ReflectMinRunes > 2000 {
		return fmt.Errorf("reasoning.reflect_min_runes 需在0至2000")
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
