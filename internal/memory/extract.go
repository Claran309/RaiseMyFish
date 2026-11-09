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
	// factConfidence：自我陈述是用户亲口说的，置信度略低于承诺。
	factConfidence = 0.85
)

// 长度上限（按 rune 计，不是字节）：Title ≤60、Summary ≤200、超长文本不提炼。
const (
	maxTitleRunes   = 60
	maxSummaryRunes = 200
	maxExtractRunes = 4000
)

// promiseMarkers 是「承诺」规则里可以独立成立的关键词。
//
// 0.9b 起把「一定」从这里拆出去单独处理：它是副词，单独匹配会误判。
var promiseMarkers = []string{"我答应", "我保证", "包在我身上"}

// promiseDingYiVerbs 是「一定」后面必须跟的动词白名单（0.9b 收紧）。
//
// 为什么不能直接匹配「一定」：「今天一定很热」「一定是这样」「他一定会来的」都是副词用法，
// 判成 0.9 重要度的承诺会污染记忆库。这里用**闭合白名单**，宁可漏判也不误判；
// 需要扩词就往列表里加一条，不做词性猜测、不加正则通配。
var promiseDingYiVerbs = []string{
	"一定帮", "一定给", "一定做到", "一定带", "一定发",
}

// ownerKeyword 是「主人相关」规则的文本触发词。
const ownerKeyword = "主人"

// factMarkers 是「自我陈述」触发词：命中即认为对方在说自己（事实类）。
//
// 只收「我 + 陈述」结构，避免把「你是学生吧」这类反问/第二人称当成事实。
var factMarkers = []string{
	"我是", "我叫", "我住", "我今年", "我大", "我姓",
	"我喜欢", "我爱吃", "我爱喝", "我玩", "我养", "我有",
	"我在", "我平时", "我每天", "我想",
}

// factImportance 是事实类的重要度（比承诺低、比首次出现高）。
const factImportance = 0.6

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
	// FromID/FromName 是已验证的陈述主体身份（说的是谁）：观众自述填发言人，
	// 主人自述填主人 UID；观众转述主人时留空，不把真实主人 UID 绑定到未验证的转述上。
	FromID   string
	FromName string
	// AssertedByID/AssertedByName 是说话人（谁说的），始终来自可信事件。
	AssertedByID   string
	AssertedByName string
	// Subject 是内容主体（说的是谁），取值 owner/viewer/fish，与 SoulSubject* 一致。
	Subject string
}

// ExtractOptions 是提炼所需的调用方上下文。
//
// 隐私授权用 AllowPrivate 显式表达，**默认拒绝**：不传就是 false，
// 私密事件一律不提炼（fail-closed）。要提炼私密会话，必须由知道的调用方主动置 true。
type ExtractOptions struct {
	// OwnerUID 是主人的可信平台 UID：FromID 与之相等才算主人自述。
	// 昵称可被改名冒充，OwnerName 只用于语义提示，不参与身份认证。
	OwnerUID string
	// OwnerName 主人的昵称，仅用于 LLM 提炼的语义提示；留空表示不提示。
	OwnerName string
	// IsFirstNick 由调用方判定：该昵称是否在本 session 首次出现。
	IsFirstNick bool
	// AllowPrivate 为 true 时才允许提炼 Private 事件（0.9b 起）。
	AllowPrivate bool
	// Context 是同 session 的最近若干条事件（旧→新），供 LLM 理解上下文；
	// 规则版忽略它。
	Context []Event
}

// Extract 按规则从一条事件提炼候选，返回顺序固定为规则优先级：
// 承诺 → 事实 → 主人相关 → 首次出现。
//
// 规则（只做这四条）：
//  1. 承诺：文本含「我答应 / 我保证 / 包在我身上」，或含「一定 + 动词」白名单
//     （一定帮 / 一定给 / 一定做到 / 一定带 / 一定发）→ promise，Importance 0.9；
//  2. 主人相关：文本含「主人」，或 FromID 等于可信 OwnerUID → preference，Importance 0.8；
//  3. 首次出现：IsFirstNick 为真且 FromName 非空 → first_seen，Importance 0.4；
//  4. 默认：四条都不匹配则不提炼，返回空。
//
// 主体与说话人分开记录：AssertedBy* 始终是发言人，Subject 说的是这条内容关于谁。
// 只有 FromID 等于 OwnerUID 才算主人自述；观众转述「主人是…」仍是候选，
// 但主体身份留空、提升为全局设定的判断留给 Soul 重建层（那里要求可信证据）。
//
// 过滤（任一命中即返回空）：
//   - 私密事件（Private=true）且调用方未 AllowPrivate 授权；
//   - 文本 trim 后为空；
//   - 文本只有符号/标点/emoji，没有任何字母或数字；
//   - 文本超过 4000 字（按 rune 计）。
//
// 多条规则同时命中时返回多条候选，不回退、不合并——合并属于 worker 的去重策略。
func Extract(ev Event, opts ExtractOptions) []Candidate {
	// 私密事件 fail-closed：只有调用方显式 AllowPrivate 才提炼。
	if ev.Private && !opts.AllowPrivate {
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
	if containsAny(text, promiseMarkers) || containsAny(text, promiseDingYiVerbs) {
		out = append(out, Candidate{
			Kind:       KindPromise,
			Title:      titled("承诺", text),
			Summary:    truncateRunes(text, maxSummaryRunes),
			Importance: promiseImportance,
			Confidence: promiseConfidence,
		})
	}

	// 规则 2：自我陈述（fact）
	if body, ok := selfStatement(text); ok {
		out = append(out, Candidate{
			Kind:       KindFact,
			Title:      titled("事实", body),
			Summary:    truncateRunes(text, maxSummaryRunes),
			Importance: factImportance,
			Confidence: factConfidence,
		})
	}

	// 规则 3：主人相关
	if isOwnerRelated(ev, opts, text) {
		out = append(out, Candidate{
			Kind:       KindPreference,
			Title:      titled("主人相关", text),
			Summary:    truncateRunes(text, maxSummaryRunes),
			Importance: preferenceImportance,
			Confidence: preferenceConfidence,
		})
	}

	// 规则 4：首次出现
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
	// 统一带上来源与主体：谁说的来自事件，说的是谁按规则推导。
	for i := range out {
		out[i].FromID, out[i].FromName, out[i].AssertedByID, out[i].AssertedByName, out[i].Subject =
			candidateProvenance(ev, opts, out[i].Kind, out[i].Title, out[i].Summary)
	}
	return out
}

// candidateProvenance 分别填「谁说的」与「说的是谁」。
//
// 主人身份只认 OwnerUID：昵称可被改名冒充，不参与认证。
// 观众提到主人时主体仍记为 owner（内容关于主人），但断言来源是观众，
// 重建长期设定时只保留为带来源候选，不会提升为全局设定。
// 只有 fact/preference 会查正文里的「主人」：promise/first_seen 的主体是发言人本人。
func candidateProvenance(ev Event, opts ExtractOptions, kind, title, summary string) (fromID, fromName, assertedID, assertedName, subject string) {
	speakerID := strings.TrimSpace(ev.FromID)
	speakerName := strings.TrimSpace(ev.FromName)
	ownerUID := strings.TrimSpace(opts.OwnerUID)
	ownerSpoke := ownerUID != "" && speakerID == ownerUID

	subject = SoulSubjectViewer
	if ownerSpoke || ((kind == KindFact || kind == KindPreference) && strings.Contains(title+"\n"+summary, ownerKeyword)) {
		subject = SoulSubjectOwner
	}
	fromID, fromName = speakerID, speakerName
	if subject == SoulSubjectOwner && !ownerSpoke {
		// 转述不是主人自述：主体身份未知，不能把真实主人 UID 绑到这句话上。
		fromID, fromName = "", ""
	}
	return fromID, fromName, speakerID, speakerName, subject
}

// selfStatement 判断文本是不是「对方在说自己的事」，返回去掉首位标点的正文。
//
// 反例处理：「你是学生吧」「他是学生」不含「我 + 陈述」组合，自然不命中；
// 另外还要求触发词前面不是第二/第三人称（防止「你我是学生」这种粘连写法）。
func selfStatement(text string) (string, bool) {
	runes := []rune(text)
	for _, marker := range factMarkers {
		idx := strings.Index(text, marker)
		if idx < 0 {
			continue
		}
		// 触发词前一个字符是 你/他/她 → 主语不是「我」，不算自我陈述
		pos := len([]rune(text[:idx]))
		if pos > 0 {
			switch runes[pos-1] {
			case '你', '他', '她', '它':
				continue
			}
		}
		body := strings.TrimSpace(strings.TrimLeft(text, "，,。.！!？?、~ "))
		if body == "" {
			return "", false
		}
		return body, true
	}
	return "", false
}

// isOwnerRelated 判断是否「主人相关」：正文提到主人，或发言人就是主人。
//
// 主人身份只认可信 UID（OwnerUID）。OwnerName 仅用于语义提示，
// 按昵称判定会让改名成主人的观众被当成主人，不能再作为身份依据。
func isOwnerRelated(ev Event, opts ExtractOptions, text string) bool {
	if strings.Contains(text, ownerKeyword) {
		return true
	}
	owner := strings.TrimSpace(opts.OwnerUID)
	return owner != "" && strings.TrimSpace(ev.FromID) == owner
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
