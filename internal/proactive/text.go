package proactive

import (
	"strings"
	"unicode"
)

const similarityThreshold = 0.75

var quoteChars = "\"'`“”‘’「」『』"

// sanitizeDanmaku 只保留一行正文：去标签/引号/控制字符，并按 15 字截断。
func sanitizeDanmaku(raw string) string {
	line := ""
	for _, candidate := range strings.Split(raw, "\n") {
		if candidate = strings.TrimSpace(strings.Trim(candidate, quoteChars)); candidate != "" {
			line = candidate
			break
		}
	}
	for _, prefix := range []string{"弹幕：", "弹幕:", "输出：", "输出:", "回答：", "回答:"} {
		if strings.HasPrefix(line, prefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			break
		}
	}
	line = strings.Trim(strings.TrimSpace(line), quoteChars)
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line)
	line = strings.TrimSpace(line)
	runes := []rune(line)
	if len(runes) > MaxDanmakuRunes {
		runes = runes[:MaxDanmakuRunes]
	}
	line = strings.TrimSpace(string(runes))
	if !hasText(line) {
		return ""
	}
	return line
}

func hasText(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// normalize 去掉空白和标点，只留字母/数字并转小写，避免标点差异绕过去重。
func normalize(s string) []rune {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return []rune(b.String())
}

// similarity 返回 0~1 的相似度；相同文本为 1。
func similarity(a, b string) float64 {
	first, second := normalize(a), normalize(b)
	if len(first) == 0 && len(second) == 0 {
		return 1
	}
	longest := max(len(first), len(second))
	return 1 - float64(levenshtein(first, second))/float64(longest)
}

// levenshtein 使用两行滚动数组，避免短文本去重引入额外依赖。
func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

// recent 只保存「已确认发送成功」的主动弹幕，用于挡住整句重复和语义近似。
// 失败/未接受的尝试走 policyState.attempts，不进入这里，不能算“已经说过”。
type recent struct {
	limit int
	texts []string
}

func newRecent(limit int) *recent { return &recent{limit: limit} }

func (r *recent) similar(text string) bool {
	for _, previous := range r.texts {
		if similarity(previous, text) >= similarityThreshold {
			return true
		}
		if shingleOverlap(previous, text) >= textSemanticThreshold {
			return true
		}
	}
	return false
}

func (r *recent) add(text string) {
	r.texts = append(r.texts, text)
	if len(r.texts) > r.limit {
		r.texts = r.texts[len(r.texts)-r.limit:]
	}
}
