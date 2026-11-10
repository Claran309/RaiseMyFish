package brain

// 本文件把「XX 是谁」从一条字符串提示升级为可核验的主体分类。
//
// 分类只回答「在**当前可见证据**下，主体最可能属于哪类」：
//   - self：问的是本鱼自己；
//   - speaker：问的是当前发言人本人；
//   - room_candidate：昵称命中当前房间里出现过的观众（焦点/房间流/回复目标/@）；
//   - unresolved：当前窗口里没有对应参与者，可能是观众、公众人物或虚构角色，
//     不能直接断定是观众，也不能把「档案里没有」说成「世上没这个人」。
//
// 这里刻意不写死任何具体人名：分类只依赖本轮事件、人格名与记忆里
// 真实出现的参与者；「贞德」「鲁迅」走的是同一套证据规则。

import (
	"strings"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
)

// 问题主体分类（出现在 current_event.question_subject.kind）。
const (
	SubjectSelf          = "self"
	SubjectSpeaker       = "speaker"
	SubjectRoomCandidate = "room_candidate"
	SubjectUnresolved    = "unresolved"
)

// QuestionTarget 是问题主体在「生成前」的预取视图，不含身份结论。
//
// 上层（cmd/fishd）据此在组装快照时预取「甲问乙」里乙的档案；
// 与 Prepare 的 question_subject 用同一套解析，避免提示和注入两套口径。
type QuestionTarget struct {
	Kind  string
	Name  string
	UID   string
	Check bool
}

// ResolveQuestionTarget 解析当前问题的主体；不是身份查询时返回 nil。
//
// 只做纯解析，不查库：UID 仅在昵称命中本轮可见参与者时给出，
// 其余情况由调用方按昵称检索档案。
func ResolveQuestionTarget(in ContextInput) *QuestionTarget {
	current := currentEventSection{ReplyTarget: replyTargetOf(in)}
	subject := buildQuestionSubject(in, current)
	if subject == nil {
		return nil
	}
	target := &QuestionTarget{Kind: subject.Kind, Name: subject.Text, Check: subject.Check}
	if subject.Match != nil {
		if name := strings.TrimSpace(subject.Match.Name); name != "" {
			target.Name = name
		}
		target.UID = strings.TrimSpace(subject.Match.ID)
	}
	return target
}

// questionSubjectSection 是当前事件的问题主体分类，不是身份结论。
type questionSubjectSection struct {
	Text  string               `json:"text"`
	Kind  string               `json:"kind"`
	Match *subjectMatchSection `json:"match,omitempty"`
	Hint  string               `json:"hint,omitempty"`
	// Check 表示主体来自「记得/认识/聊过/喂过」类回忆核查，
	// 而不是「X 是谁」；提示会要求先查观众档案再查互动记忆。
	Check bool `json:"check,omitempty"`
}

// subjectMatchSection 说明分类依据（命中哪个参与者、来自哪类证据）。
type subjectMatchSection struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// buildQuestionSubject 解析「X 是谁」或回忆核查的主体并按可见证据分类。
func buildQuestionSubject(in ContextInput, current currentEventSection) *questionSubjectSection {
	text, fixedKind := questionSubjectText(in.Event.Text)
	if text == "" {
		return nil
	}
	check := checkedSubject(strings.TrimSpace(in.Event.Text)) != ""
	hint := func(kind string) string {
		if check {
			return checkSubjectHint(kind)
		}
		return subjectHint(kind)
	}
	if fixedKind != "" {
		return &questionSubjectSection{Text: text, Kind: fixedKind, Hint: hint(fixedKind), Check: check}
	}
	if match := selfMatch(text, in.Persona); match != nil {
		return &questionSubjectSection{Text: text, Kind: SubjectSelf, Match: match, Hint: hint(SubjectSelf), Check: check}
	}
	if match := speakerMatch(text, in.Event); match != nil {
		return &questionSubjectSection{Text: text, Kind: SubjectSpeaker, Match: match, Hint: hint(SubjectSpeaker), Check: check}
	}
	if match := roomMatch(text, in, current); match != nil {
		return &questionSubjectSection{Text: text, Kind: SubjectRoomCandidate, Match: match, Hint: hint(SubjectRoomCandidate), Check: check}
	}
	return &questionSubjectSection{Text: text, Kind: SubjectUnresolved, Hint: hint(SubjectUnresolved), Check: check}
}

// questionSubjectText 先识别代词与「本鱼是谁」，再提取普通昵称或回忆核查主体。
func questionSubjectText(text string) (string, string) {
	trimmed := strings.TrimSpace(text)
	// 这些短语用现有提取会得到「你知道我」这类残片，必须先按整句识别。
	for _, marker := range []string{"本鱼是谁", "本鱼是哪个"} {
		if strings.Contains(trimmed, marker) {
			return "本鱼", SubjectSelf
		}
	}
	for _, marker := range []string{"你是谁", "你叫啥", "你叫什么", "你是哪位", "你是什么鱼"} {
		if strings.Contains(trimmed, marker) {
			return "你", SubjectSelf
		}
	}
	for _, marker := range []string{"你知道我是谁", "猜猜我是谁", "我是谁"} {
		if strings.Contains(trimmed, marker) {
			return "我", SubjectSpeaker
		}
	}
	if name := questionedSubject(trimmed); name != "" {
		return name, ""
	}
	return checkedSubject(trimmed), ""
}

// questionSubjectName 返回问句里的主体昵称，供 subject_candidates 复用。
func questionSubjectName(text string) string {
	if name := questionedSubject(text); name != "" {
		return name
	}
	return checkedSubject(text)
}

// selfMatch 判断主体是否就是本鱼（人格显示名或称呼）。
func selfMatch(text string, p persona.Pack) *subjectMatchSection {
	name := selfName(p)
	if name == "" {
		return nil
	}
	if strings.EqualFold(text, name) {
		return &subjectMatchSection{Name: name, Source: "persona_name"}
	}
	return nil
}

// speakerMatch 判断主体是否就是当前发言人。
func speakerMatch(text string, e event.Event) *subjectMatchSection {
	name := strings.TrimSpace(e.FromName)
	if name == "" || !strings.EqualFold(text, name) {
		return nil
	}
	return &subjectMatchSection{ID: strings.TrimSpace(e.FromID), Name: name, Source: "speaker"}
}

// roomMatch 在焦点轮次、房间流、回复目标与 @ 里找同名参与者。
//
// Focus 优先：它代表本轮正在对话的人，比热闹房间的随机流更可信。
func roomMatch(text string, in ContextInput, current currentEventSection) *subjectMatchSection {
	matchIn := func(messages []memory.Message, source string) *subjectMatchSection {
		for _, m := range messages {
			if m.Dir != event.DirIn || strings.TrimSpace(m.FromName) == "" {
				continue
			}
			if strings.EqualFold(text, strings.TrimSpace(m.FromName)) {
				return &subjectMatchSection{ID: strings.TrimSpace(m.FromID), Name: strings.TrimSpace(m.FromName), Source: source}
			}
		}
		return nil
	}
	if match := matchIn(in.Memory.Focus, "focus"); match != nil {
		return match
	}
	if match := matchIn(in.Memory.Recent, "recent"); match != nil {
		return match
	}
	if current.ReplyTarget != nil && strings.EqualFold(text, strings.TrimSpace(current.ReplyTarget.Name)) {
		return &subjectMatchSection{Name: current.ReplyTarget.Name, Source: "reply_target"}
	}
	for _, name := range mentionedNames(in.Event.Text) {
		if strings.EqualFold(text, name) {
			return &subjectMatchSection{Name: name, Source: "mention"}
		}
	}
	return nil
}

// subjectHint 是给模型的消歧规则；分类不是结论，证据仍要查。
func subjectHint(kind string) string {
	switch kind {
	case SubjectSelf:
		return "问的是本鱼自己，不需要查观众档案。"
	case SubjectSpeaker:
		return "问的是当前发言人本人；需要身份细节时用 viewer_search 按 uid/昵称查档案。"
	case SubjectRoomCandidate:
		return "昵称命中当前可见的房间观众；需要身份结论时用 viewer_search 查档案，查不到就说没印象，不要编。"
	default:
		return "不在当前可见的房间参与者中；不要假定是观众，也不要把「档案里没有」说成「世上没这个人」；先查记忆/知识，确需联网再用 web_search，没有证据就说不知道。"
	}
}

// checkSubjectHint 是「记得/认识/聊过/喂过」类回忆核查的提示。
//
// 这类问法问的是「你还记不记得这个人/这件事」，即使昵称暂时不在当前房间窗口里，
// 也应该先查观众档案（可能只是最近没说话），再查本会话互动记忆。
func checkSubjectHint(kind string) string {
	switch kind {
	case SubjectSelf:
		return subjectHint(kind)
	case SubjectSpeaker:
		return "这是一句关于当前发言人的回忆核查；先用 viewer_search 按 uid/昵称查档案与侧写，再查本会话互动记忆。"
	case SubjectRoomCandidate:
		return "这是一句回忆核查，且昵称命中当前房间观众；先用 viewer_search 查档案与侧写，再用 memory_search 查本会话互动记忆。"
	default:
		return "这是一句关于某位观众的回忆核查（记得/认识/聊过/喂过），但昵称不在当前可见房间窗口里；先 viewer_search 按昵称查观众档案（可能只是最近没说话），再用 memory_search 查互动记忆；查不到就说没印象，不要编。"
	}
}

// checkSubjectLead 是「记得/认识」类回忆核查前缀，按长度降序匹配。
var checkSubjectLeads = []string{
	"你还记不记得", "你还记得", "还记不记得", "记不记得", "还记得",
	"你还认识", "还认识", "记得", "认识",
}

// checkRelationVerbs 是「跟 XX 聊过/打过招呼」类互动核查动词，按长度降序匹配。
var checkRelationVerbs = []string{
	"打过招呼", "聊过天", "说过话", "喂过饭", "聊过", "喂过",
}

// checkSubjectBadParts 是主体里不该出现的事件/动作词：
// 命中说明提取到的是「昨天聊天」「顶号的事」这类事件，而不是人名。
var checkSubjectBadParts = []string{
	"聊天", "聊过", "聊了", "顶号", "的事", "事情", "消息", "弹幕", "直播",
	"打招呼", "喂饭", "喂过", "说过话", "吃饭", "带我", "给我", "一下",
}

// checkSubjectTimeWords 是整句主体恰好为时间词的情况；昵称含「今夜」不算
// （例如「今夜琴声温柔」是观众昵称，不能按时间词拒掉）。
var checkSubjectTimeWords = map[string]bool{
	"昨天": true, "昨晚": true, "前天": true, "今天": true, "今晚": true,
	"早上": true, "中午": true, "晚上": true, "上次": true, "最近": true,
	"刚才": true, "之前": true, "以前": true, "那晚": true, "今晚的事": true,
}

// checkedSubject 提取「记得XX吗 / 认识XX / 跟XX聊过 / XX喂过饭」里的昵称。
//
// 只认疑问句式（吗/么/嘛/呢/不/有没有/是不是/？/?），避免把
// 「我认识XX」「记得给我带饭」这类陈述误当观众查询；提取不到人名时返回空，
// 让这类话走普通记忆召回。
func checkedSubject(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	if name := checkPrefixSubject(trimmed); name != "" {
		return name
	}
	return checkRelationSubject(trimmed)
}

// checkPrefixSubject 处理「记得/认识 + 昵称」前缀。
func checkPrefixSubject(text string) string {
	if !isQuestionText(text) {
		return ""
	}
	for _, lead := range checkSubjectLeads {
		idx := strings.Index(text, lead)
		if idx < 0 {
			continue
		}
		// 「我认识XX吗」是问自己认不认识，不是查观众。
		if prefix := text[:idx]; strings.Contains(prefix, "我") && !strings.Contains(prefix, "你") {
			continue
		}
		if name := checkCandidate(text[idx+len(lead):]); name != "" {
			return name
		}
	}
	return ""
}

// checkRelationSubject 处理「跟/和/与 XX 聊过」与「XX 喂过饭吗」两类框架。
func checkRelationSubject(text string) string {
	for _, prep := range []string{"跟", "和", "与"} {
		start := strings.Index(text, prep)
		if start < 0 {
			continue
		}
		if prefix := text[:start]; strings.Contains(prefix, "我") && !strings.Contains(prefix, "你") {
			continue
		}
		rest := text[start+len(prep):]
		for _, verb := range checkRelationVerbs {
			if idx := strings.Index(rest, verb); idx > 0 {
				if name := checkCandidate(rest[:idx]); name != "" {
					return name
				}
			}
		}
	}
	if !isQuestionText(text) {
		return ""
	}
	for _, verb := range checkRelationVerbs {
		idx := strings.Index(text, verb)
		if idx <= 0 {
			continue
		}
		candidate := text[:idx]
		for _, lead := range []string{"你还", "你有没有", "有没有人", "有没有", "有人", "还", "你"} {
			candidate = strings.TrimPrefix(candidate, lead)
		}
		if name := checkCandidate(candidate); name != "" {
			return name
		}
	}
	return ""
}

// checkCandidate 清洗并校验候选昵称；空串表示不是人名。
func checkCandidate(raw string) string {
	name := strings.Trim(strings.TrimSpace(raw), candidateTrimCutset)
	name = strings.TrimSuffix(name, "到底")
	for _, tail := range []string{"吗", "么", "嘛", "呢", "呀", "啊", "吧", "呗"} {
		name = strings.TrimSuffix(name, tail)
	}
	name = strings.Trim(name, candidateTrimCutset)
	if utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > maxCandidateRunes {
		return ""
	}
	switch name {
	case "你", "我", "他", "她", "它", "谁", "大家", "你们", "我们", "有人",
		"有没有", "什么", "啥", "这个", "那个", "这件事", "那件事",
		"记得", "还记得", "认识":
		return ""
	}
	if checkSubjectTimeWords[name] {
		return ""
	}
	for _, bad := range checkSubjectBadParts {
		if strings.Contains(name, bad) {
			return ""
		}
	}
	return name
}

// isQuestionText 判断句子是否在提问：疑问语气词、A 不 A 格式或问号。
func isQuestionText(text string) bool {
	for _, marker := range []string{"吗", "么", "嘛", "呢", "有没有", "是不是", "记不记得", "认不认识", "？", "?"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return strings.HasSuffix(text, "不") || strings.HasSuffix(text, "没")
}
