package reasoning

// 本文件决定「这条事件要不要反思」，规则必须可解释、可测试。
// 只做长度/工具/问答判定，不调用模型；评论与私信默认关闭。

import (
	"strings"
	"unicode/utf8"
)

// ReflectPolicy 是 Reflection 的触发配置。
type ReflectPolicy struct {
	Live     bool
	Comment  bool
	DM       bool
	MinRunes int
	OnTools  bool
}

// ShouldReflect 返回是否触发反思；第二返回值是原因，便于日志解释。
func (p ReflectPolicy) ShouldReflect(channel string, draft string, usedTools int, question string) (bool, string) {
	switch channel {
	case "live":
		if !p.Live {
			return false, "channel_off"
		}
	case "comment":
		if !p.Comment {
			return false, "channel_off"
		}
	case "dm":
		if !p.DM {
			return false, "channel_off"
		}
	default:
		return false, "channel_off"
	}
	if utf8.RuneCountInString(strings.TrimSpace(draft)) == 0 {
		return false, "empty"
	}
	if p.OnTools && usedTools > 0 {
		return true, "tools"
	}
	if p.MinRunes > 0 && utf8.RuneCountInString(draft) > p.MinRunes {
		return true, "long_draft"
	}
	if isQuestionLike(question) {
		return true, "question"
	}
	return false, "short"
}

// isQuestionLike 只识别显式问句，避免把寒暄当问答。
func isQuestionLike(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, marker := range []string{"?", "？", "吗", "怎么", "为什么", "啥", "什么", "哪", "多少", "谁"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
