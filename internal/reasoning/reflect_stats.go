package reasoning

// 本文件把 Reflection 的「有没有用」变成可累计的指标。
// 只统计调用/改写/跳过原因，不保存草稿或改写全文；
// 评测层可以用同一结构对比 Reflect=off/on 两组样本。

import (
	"fmt"
	"sort"
	"strings"
)

// ReflectStat 是单次运行或一组运行的归因汇总。
type ReflectStat struct {
	Runs        int            `json:"runs"`
	Called      int            `json:"called"`
	Changed     int            `json:"changed"`
	Skipped     int            `json:"skipped"`
	SkipReasons map[string]int `json:"skip_reasons,omitempty"`
	PromptBytes int            `json:"prompt_bytes"`
}

// ChangedRate 是「改写次数 / 实际调用次数」；没有调用时返回 0。
func (s ReflectStat) ChangedRate() float64 {
	if s.Called == 0 {
		return 0
	}
	return float64(s.Changed) / float64(s.Called)
}

// SkipRate 是「跳过次数 / 总运行次数」。
func (s ReflectStat) SkipRate() float64 {
	if s.Runs == 0 {
		return 0
	}
	return float64(s.Skipped) / float64(s.Runs)
}

// Add 累加一次 Result 的归因。
func (s *ReflectStat) Add(r Result) {
	if s == nil {
		return
	}
	s.Runs++
	if r.ReflectCalled {
		s.Called++
		s.PromptBytes += r.ReflectPromptBytes
		if r.ReflectChanged {
			s.Changed++
		}
		return
	}
	s.Skipped++
	if s.SkipReasons == nil {
		s.SkipReasons = map[string]int{}
	}
	reason := strings.TrimSpace(r.ReflectSkippedReason)
	if reason == "" {
		reason = "unknown"
	}
	s.SkipReasons[reason]++
}

// String 输出一行可进日报的摘要；跳过原因按名称排序，便于 diff。
func (s ReflectStat) String() string {
	keys := make([]string, 0, len(s.SkipReasons))
	for key := range s.SkipReasons {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, s.SkipReasons[key]))
	}
	return fmt.Sprintf("runs=%d called=%d changed=%d changed_rate=%.2f skipped=%d skip_rate=%.2f reasons=[%s] prompt_bytes=%d",
		s.Runs, s.Called, s.Changed, s.ChangedRate(), s.Skipped, s.SkipRate(), strings.Join(parts, ","), s.PromptBytes)
}

// ReflectComparison 是 Reflect=off/on 两组的对照结果。
// ImproveRate/HarmRate 需要人工或 LLM judge 标注；未标注时保持 0 并显式标 Pending。
type ReflectComparison struct {
	Off      ReflectStat `json:"off"`
	On       ReflectStat `json:"on"`
	Improved int         `json:"improved"`
	Harmed   int         `json:"harmed"`
	Pending  bool        `json:"judge_pending"`
}

// ImproveRate 是「变好 / 实际改写」。
func (c ReflectComparison) ImproveRate() float64 {
	if c.On.Changed == 0 {
		return 0
	}
	return float64(c.Improved) / float64(c.On.Changed)
}

// HarmRate 是「变坏 / 实际改写」。
func (c ReflectComparison) HarmRate() float64 {
	if c.On.Changed == 0 {
		return 0
	}
	return float64(c.Harmed) / float64(c.On.Changed)
}

// HarmAlarm 是 harm_rate 超过 5% 的告警信号。
func (c ReflectComparison) HarmAlarm() bool {
	return c.On.Changed > 0 && c.HarmRate() > 0.05
}
