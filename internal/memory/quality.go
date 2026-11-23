package memory

// 本文件判定一条文本是否值得写成长期偏好/事实。
//
// 目标：把「提到主人」从「直接写 preference」降级为「需要稳定信号」，
// 减少一次性玩笑、起哄、复读产生的低价值记忆。
// 规则必须可解释：返回值直接进日志/候选审计，不靠模型自觉。

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// stableSignals 是「长期偏好/稳定事实」的显式语言标记。
var stableSignals = []string{
	"喜欢", "讨厌", "爱吃", "不吃", "习惯", "以后", "记得", "答应", "承诺",
	"我是", "我在", "长期", "每次", "一直", "从不", "总是", "偏好", "最怕", "最想",
}

// jokeSignals 是「一次性玩笑/起哄/复读」的标记；命中即不写长期偏好。
var jokeSignals = []string{
	"哈哈", "hhh", "笑死", "草", "乐", "绷", "6", "复读", "整活", "开玩笑", "玩梗",
}

// ValueDecision 是可解释的写入判定。
type ValueDecision struct {
	Keep   bool
	Reason string
}

// ClassifyValue 判断一条文本是否值得作为长期偏好/事实候选。
//
// 顺序：空/纯符号/过短 → 玩笑/复读 → 稳定信号 → 默认丢弃。
// 「提到主人」不再是充分条件，只是一个主体提示。
func ClassifyValue(text string) ValueDecision {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ValueDecision{Reason: "empty"}
	}
	if isSymbolOnly(trimmed) {
		return ValueDecision{Reason: "symbol_only"}
	}
	if utf8.RuneCountInString(trimmed) < 4 {
		return ValueDecision{Reason: "too_short"}
	}
	if containsAny(strings.ToLower(trimmed), jokeSignals) {
		return ValueDecision{Reason: "joke_or_repeat"}
	}
	if !containsAny(trimmed, stableSignals) {
		return ValueDecision{Reason: "no_stable_signal"}
	}
	if repeatedPunctuation(trimmed) {
		return ValueDecision{Reason: "noise_punctuation"}
	}
	return ValueDecision{Keep: true, Reason: "stable_signal"}
}

// repeatedPunctuation 过滤「！！！！」这类情绪刷屏。
func repeatedPunctuation(text string) bool {
	var last rune
	run := 0
	for _, r := range text {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			if r == last {
				run++
				if run >= 3 {
					return true
				}
			} else {
				last, run = r, 1
			}
			continue
		}
		last, run = 0, 0
	}
	return false
}
