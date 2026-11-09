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

// questionSubjectSection 是当前事件的问题主体分类，不是身份结论。
type questionSubjectSection struct {
	Text  string               `json:"text"`
	Kind  string               `json:"kind"`
	Match *subjectMatchSection `json:"match,omitempty"`
	Hint  string               `json:"hint,omitempty"`
}

// subjectMatchSection 说明分类依据（命中哪个参与者、来自哪类证据）。
type subjectMatchSection struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// buildQuestionSubject 解析「X 是谁」的主体并按可见证据分类。
func buildQuestionSubject(in ContextInput, current currentEventSection) *questionSubjectSection {
	text, fixedKind := questionSubjectText(in.Event.Text)
	if text == "" {
		return nil
	}
	if fixedKind != "" {
		return &questionSubjectSection{Text: text, Kind: fixedKind, Hint: subjectHint(fixedKind)}
	}
	if match := selfMatch(text, in.Persona); match != nil {
		return &questionSubjectSection{Text: text, Kind: SubjectSelf, Match: match, Hint: subjectHint(SubjectSelf)}
	}
	if match := speakerMatch(text, in.Event); match != nil {
		return &questionSubjectSection{Text: text, Kind: SubjectSpeaker, Match: match, Hint: subjectHint(SubjectSpeaker)}
	}
	if match := roomMatch(text, in, current); match != nil {
		return &questionSubjectSection{Text: text, Kind: SubjectRoomCandidate, Match: match, Hint: subjectHint(SubjectRoomCandidate)}
	}
	return &questionSubjectSection{Text: text, Kind: SubjectUnresolved, Hint: subjectHint(SubjectUnresolved)}
}

// questionSubjectText 先识别代词与「本鱼是谁」，再提取普通昵称。
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
	return questionedSubject(trimmed), ""
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
