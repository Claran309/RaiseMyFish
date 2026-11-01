package memory

// 本文件实现 0.9a「规则版记忆提炼」纯函数：把一条已完成事件转成 0 条或多条 Candidate。
//
// 边界（plans/phase-0.9-workbuddy-dispatch.md 0.9a）：
//   - 只做四条规则的关键词匹配，不做语义理解、不调用模型、不读库、不写库；
//   - 全部纯函数，无全局可变状态、无 goroutine、无时钟依赖；
//   - 文本一律按 rune 计数与截断，不切坏中文；
//   - 提炼结果的落库、去重与幂等属于 0.9b worker，不在本文件。

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
)

// Event 是 event.Event 的别名。
//
// 契约（contracts/memory.md 开头）规定：Event 的字段与枚举唯一真源是 contracts/event.md，
// 所以这里不复制字段、不另建第二套事件模型，只做类型别名；
// 这样 Extract 的签名与派发单一致，又能直接消费总线产出的事件。
type Event = event.Event

// 候选种类（派发单规定的 4 个取值）。
const (
	// KindFact 事实类；四条规则目前都不产生 fact，预留给后续批次。
	KindFact = "fact"
	// KindPromise 承诺类。
	KindPromise = "promise"
	// KindPreference 偏好类；派发单把「主人相关」映射到这一类。
	KindPreference = "preference"
	// KindFirstSeen 首次出现类。
	KindFirstSeen = "first_seen"
)

// 规则权重。Importance 表示「值得记多久」，Confidence 表示「这条提炼有多可信」。
//
//   - 承诺：关键词明确、内容重要 → 0.9 / 0.95
//   - 主人相关：来源可信、内容次要一点 → 0.8 / 0.9
//   - 首次出现：观察事实、几乎不会错，但价值最低 → 0.4 / 0.9
const (
	promiseImportance    = 0.9
	promiseConfidence    = 0.95
	preferenceImportance = 0.8
	preferenceConfidence = 0.9
	firstSeenImportance  = 0.4
	firstSeenConfidence  = 0.9
)

// 长度上限（按 rune 计，不是字节）：Title ≤60、Summary ≤200、超长文本不提炼。
const (
	maxTitleRunes   = 60
	maxSummaryRunes = 200
	maxExtractRunes = 4000
)

// promiseMarkers 是「承诺」规则的触发词。
//
// 按派发单原样取四个，不做同义词扩展——扩词属于规则调参，需要另外裁决。
var promiseMarkers = []string{"我答应", "我保证", "一定", "包在我身上"}

// ownerKeyword 是「主人相关」规则的文本触发词。
const ownerKeyword = "主人"

// Candidate 是提炼候选，尚未落库。
type Candidate struct {
	// Kind 取值见 KindFact / KindPromise / KindPreference / KindFirstSeen。
	Kind string
	// Title 一句话标题，≤60 字（含前缀）。
	Title string
	// Summary 摘要，≤200 字。
	Summary string
	// Importance 重要度，0..1。
	Importance float64
	// Confidence 置信度，0..1。
	Confidence float64
}

// ExtractOptions 是提炼所需的调用方上下文。
//
// 这里刻意不放「是否允许提炼私密事件」的开关：派发单只给了两个字段，
// 隐私授权属于需要裁决的设计项，不能由实现自行发明。
type ExtractOptions struct {
	// OwnerName 主人的昵称；FromName 与之相等时判为主人相关。为空表示不按昵称判定。
	OwnerName string
	// IsFirstNick 由调用方判定：该昵称是否在本 session 首次出现。
	IsFirstNick bool
}

// Extract 按规则从一条事件提炼候选，返回顺序固定为规则优先级：
// 承诺 → 主人相关 → 首次出现。
//
// 规则（只做这四条）：
//  1. 承诺：文本含「我答应 / 我保证 / 一定 / 包在我身上」→ promise，Importance 0.9；
//  2. 主人相关：文本含「主人」，或 FromName 等于 OwnerName → preference，Importance 0.8；
//  3. 首次出现：IsFirstNick 为真且 FromName 非空 → first_seen，Importance 0.4；
//  4. 默认：四条都不匹配则不提炼，返回空。
//
// 过滤（任一命中即返回空）：
//   - 私密事件（Private=true）：当前 API 没有授权字段，按「调用方未授权」处理，fail-closed；
//   - 文本 trim 后为空；
//   - 文本只有符号/标点/emoji，没有任何字母或数字；
//   - 文本超过 4000 字（按 rune 计）。
//
// 多条规则同时命中时返回多条候选，不回退、不合并——合并属于 worker 的去重策略。
func Extract(ev Event, opts ExtractOptions) []Candidate {
	// 私密事件 fail-closed：没有显式授权就不能提炼。
	// 若 0.9b 要提炼私密会话的主人偏好，须先补一个显式授权字段并另行裁决。
	if ev.Private {
		return nil
	}

	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return nil
	}
	if utf8.RuneCountInString(text) > maxExtractRunes {
		return nil
	}
	if isSymbolOnly(text) {
		return nil
	}

	var out []Candidate

	// 规则 1：承诺
	if containsAny(text, promiseMarkers) {
		out = append(out, Candidate{
			Kind:       KindPromise,
			Title:      titled("承诺", text),
			Summary:    truncateRunes(text, maxSummaryRunes),
			Importance: promiseImportance,
			Confidence: promiseConfidence,
		})
	}

	// 规则 2：主人相关
	if isOwnerRelated(ev, opts, text) {
		out = append(out, Candidate{
			Kind:       KindPreference,
			Title:      titled("主人相关", text),
			Summary:    truncateRunes(text, maxSummaryRunes),
			Importance: preferenceImportance,
			Confidence: preferenceConfidence,
		})
	}

	// 规则 3：首次出现
	if name := strings.TrimSpace(ev.FromName); opts.IsFirstNick && name != "" {
		out = append(out, Candidate{
			Kind:       KindFirstSeen,
			Title:      titled("首次出现", name),
			Summary:    firstSeenSummary(name, text),
			Importance: firstSeenImportance,
			Confidence: firstSeenConfidence,
		})
	}

	// 规则 4：默认不提炼（out 为 nil 时直接返回空）
	return out
}

// isOwnerRelated 判断是否「主人相关」：文本提到主人，或发言人就是主人。
func isOwnerRelated(ev Event, opts ExtractOptions, text string) bool {
	if strings.Contains(text, ownerKeyword) {
		return true
	}
	owner := strings.TrimSpace(opts.OwnerName)
	return owner != "" && strings.TrimSpace(ev.FromName) == owner
}

// containsAny 判断 s 是否包含任一子串。
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// isSymbolOnly 判断文本是否只有符号：没有任何字母或数字即为纯符号。
//
// 用 unicode 分类而不是自建字符表，中文/日文/韩文都算字母，
// 所以「？？？」「。。。」「——」「🙂🙂」会被过滤，「666」不会被过滤。
func isSymbolOnly(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// truncateRunes 按 rune 截断到 max 个字符（含省略号），绝不切坏多字节字符。
//
// 超长时用「…」占最后一位，保证返回值总长不超过 max。
func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	if max == 1 {
		return string(r[:1])
	}
	return string(r[:max-1]) + "…"
}

// titled 拼「前缀：正文」形式的一句话标题，总长不超过 maxTitleRunes。
func titled(prefix, body string) string {
	head := prefix + "："
	budget := maxTitleRunes - utf8.RuneCountInString(head)
	if budget < 1 {
		return truncateRunes(head, maxTitleRunes)
	}
	return head + truncateRunes(body, budget)
}

// firstSeenSummary 组装首次出现的摘要：昵称 + 原话，总长不超过 maxSummaryRunes。
func firstSeenSummary(name, text string) string {
	head := "首次出现的昵称「" + name + "」，原话："
	budget := maxSummaryRunes - utf8.RuneCountInString(head)
	if budget < 1 {
		return truncateRunes(head, maxSummaryRunes)
	}
	return head + truncateRunes(text, budget)
}
