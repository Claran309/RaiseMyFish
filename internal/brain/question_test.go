package brain

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

type questionSubjectView struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	Match *struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Source string `json:"source"`
	} `json:"match"`
	Hint  string `json:"hint"`
	Check bool   `json:"check"`
}

func preparedContext(t *testing.T, in ContextInput) (map[string]json.RawMessage, string) {
	t.Helper()
	task, _ := fixture()
	task.EventID = in.Event.ID
	task.Channel = in.Event.Channel
	task.Session = in.Event.Session
	prepared, err := Prepare(context.Background(), task, in, DefaultBudget())
	if err != nil {
		t.Fatalf("Prepare 失败：%v", err)
	}
	body := sectionBody(t, prepared.Messages[1].Content, "context")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("context 不是 JSON 对象：%v", err)
	}
	return raw, prepared.Messages[1].Content
}

func questionSubjectOf(t *testing.T, in ContextInput) (questionSubjectView, string) {
	t.Helper()
	raw, user := preparedContext(t, in)
	rawCurrent, ok := raw["current_event"]
	if !ok {
		t.Fatal("缺少 current_event")
	}
	var current struct {
		QuestionSubject *questionSubjectView `json:"question_subject"`
	}
	if err := json.Unmarshal(rawCurrent, &current); err != nil {
		t.Fatalf("current_event 解析失败：%v", err)
	}
	if current.QuestionSubject == nil {
		return questionSubjectView{}, user
	}
	return *current.QuestionSubject, user
}

func inboundFrom(id, uid, name, text string, at time.Time) memory.Message {
	return memory.Message{EventID: id, Channel: event.ChLive, Session: "room", Dir: event.DirIn, FromID: uid, FromName: name, Text: text, At: at}
}

// TestQuestionSubjectRoomCandidateVersusUnresolved 是组6的核心：
// 房间里的昵称与完全不在房间里的名字必须走不同分类，不能一律查观众或一律解释成自己。
func TestQuestionSubjectRoomCandidateVersusUnresolved(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	cases := []struct {
		name       string
		text       string
		focus      []memory.Message
		wantKind   string
		wantName   string
		wantSource string
		wantHint   string
	}{
		{
			name:       "贞德在焦点轮次",
			text:       "贞德是谁",
			focus:      []memory.Message{inboundFrom("live:room:j1", "7001", "贞德", "今天也要白饭", base.Add(time.Minute))},
			wantKind:   SubjectRoomCandidate,
			wantName:   "贞德",
			wantSource: "focus",
			wantHint:   "viewer_search",
		},
		{
			name:     "鲁迅不在房间",
			text:     "鲁迅是谁",
			wantKind: SubjectUnresolved,
			wantHint: "不要假定是观众",
		},
		{
			name:       "贞德只在房间流",
			text:       "贞德是谁",
			focus:      nil,
			wantKind:   SubjectRoomCandidate,
			wantName:   "贞德",
			wantSource: "recent",
			wantHint:   "viewer_search",
		},
		{
			name:     "问本鱼自己",
			text:     "你是谁",
			wantKind: SubjectSelf,
			wantHint: "不需要查观众档案",
		},
		{
			name:     "问发言人自己",
			text:     "我是谁",
			wantKind: SubjectSpeaker,
			wantHint: "当前发言人本人",
		},
		{
			name:       "人格名也算本鱼自己",
			text:       "test是谁",
			wantKind:   SubjectSelf,
			wantName:   "test",
			wantSource: "persona_name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, in := fixture()
			in.Event.ID = "live:room:q"
			in.Event.Text = tc.text
			in.Event.FromID = "1001"
			in.Event.FromName = "A"
			in.Event.At = base.Add(2 * time.Minute)
			in.Memory.Focus = tc.focus
			if tc.wantSource == "recent" {
				in.Memory.Recent = []memory.Message{inboundFrom("live:room:r1", "7001", "贞德", "路过", base.Add(time.Minute))}
			}
			subject, user := questionSubjectOf(t, in)
			if subject.Kind != tc.wantKind || subject.Text == "" {
				t.Fatalf("分类 = %+v，期望 kind=%s", subject, tc.wantKind)
			}
			if tc.wantName != "" {
				if subject.Match == nil || subject.Match.Name != tc.wantName || subject.Match.Source != tc.wantSource {
					t.Fatalf("命中证据 = %+v，期望 %s/%s", subject.Match, tc.wantName, tc.wantSource)
				}
			} else if subject.Match != nil {
				t.Fatalf("不应有命中证据：%+v", subject.Match)
			}
			if tc.wantHint != "" && !strings.Contains(user, tc.wantHint) {
				t.Fatalf("instructions 缺少提示 %q：%s", tc.wantHint, user)
			}
		})
	}
}

// TestQuestionSubjectSpeakerWinsOverRoom 当前发言人就是主体时，不能误判成房间候选。
func TestQuestionSubjectSpeakerWinsOverRoom(t *testing.T) {
	_, in := fixture()
	in.Event.Text = "阿明是谁"
	in.Event.FromID = "1001"
	in.Event.FromName = "阿明"
	in.Memory.Focus = []memory.Message{inboundFrom("live:room:a0", "1001", "阿明", "我来了", in.Event.At.Add(-time.Minute))}
	subject, _ := questionSubjectOf(t, in)
	if subject.Kind != SubjectSpeaker || subject.Match == nil || subject.Match.Source != "speaker" {
		t.Fatalf("当前发言人应判 speaker：%+v", subject)
	}
}

// TestQuestionSubjectReplyTargetAndMention 回复目标与 @ 也是房间证据。
func TestQuestionSubjectReplyTargetAndMention(t *testing.T) {
	_, in := fixture()
	in.Event.Text = "贞德是谁"
	in.Event.Meta = json.RawMessage(`{"reply_to_id":"live:room:j2","reply_to_name":"贞德"}`)
	subject, _ := questionSubjectOf(t, in)
	if subject.Kind != SubjectRoomCandidate || subject.Match == nil || subject.Match.Source != "reply_target" {
		t.Fatalf("回复目标应判 room_candidate：%+v", subject)
	}

	_, in = fixture()
	in.Event.Text = "贞德是谁呀@贞德"
	subject, _ = questionSubjectOf(t, in)
	if subject.Kind != SubjectRoomCandidate || subject.Match == nil || subject.Match.Source != "mention" {
		t.Fatalf("@ 昵称应判 room_candidate：%+v", subject)
	}
}

// TestQuestionSubjectAbsentForPlainChat 普通聊天不产生主体分类，避免污染 prompt。
func TestQuestionSubjectAbsentForPlainChat(t *testing.T) {
	_, in := fixture()
	in.Event.Text = "今天吃什么"
	subject, user := questionSubjectOf(t, in)
	if subject.Kind != "" || subject.Text != "" {
		t.Fatalf("普通聊天不应分类：%+v", subject)
	}
	if strings.Contains(user, "question_subject") {
		t.Fatal("普通聊天的 instructions 不应出现主体提示")
	}
}

// TestQuestionCheckSubject 覆盖「记得/认识/聊过/喂过/打过招呼」类回忆核查（本批 M01）。
//
// 回忆核查和「X是谁」走同一条主体分类：昵称命中房间就 room_candidate，
// 提示先查观众档案；时间/事件词（昨天聊天/顶号/喂饭）不能被当成人名。
func TestQuestionCheckSubject(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	room := func(name string) []memory.Message {
		return []memory.Message{inboundFrom("live:room:c1", "7002", name, "来了", base.Add(time.Minute))}
	}
	cases := []struct {
		name       string
		text       string
		focus      []memory.Message
		wantKind   string
		wantName   string
		wantCheck  bool
		wantHint   string
		wantAbsent bool
	}{
		{name: "记得昵称吗", text: "记得今夜琴声温柔吗", focus: room("今夜琴声温柔"),
			wantKind: SubjectRoomCandidate, wantName: "今夜琴声温柔", wantCheck: true, wantHint: "viewer_search"},
		{name: "昵称含时间词也要认", text: "记得今天也要开心吗", focus: room("今天也要开心"),
			wantKind: SubjectRoomCandidate, wantName: "今天也要开心", wantCheck: true, wantHint: "viewer_search"},
		{name: "认识昵称吗", text: "你认识阿明吗", focus: room("阿明"),
			wantKind: SubjectRoomCandidate, wantName: "阿明", wantCheck: true, wantHint: "viewer_search"},
		{name: "跟昵称聊过吗", text: "跟阿明聊过吗", focus: room("阿明"),
			wantKind: SubjectRoomCandidate, wantName: "阿明", wantCheck: true, wantHint: "memory_search"},
		{name: "昵称喂过饭吗", text: "阿明喂过饭吗", focus: room("阿明"),
			wantKind: SubjectRoomCandidate, wantName: "阿明", wantCheck: true, wantHint: "memory_search"},
		{name: "跟昵称打过招呼吗", text: "跟阿明打过招呼吗", focus: room("阿明"),
			wantKind: SubjectRoomCandidate, wantName: "阿明", wantCheck: true, wantHint: "memory_search"},
		{name: "不在房间仍要查档案", text: "记得鲁迅吗",
			wantKind: SubjectUnresolved, wantName: "鲁迅", wantCheck: true, wantHint: "viewer_search"},
		{name: "记得时间事件", text: "记得昨天聊天", wantAbsent: true},
		{name: "记得顶号的事", text: "记得昨晚顶号的事吗", wantAbsent: true},
		{name: "有没有喂过饭", text: "有没有喂过饭", wantAbsent: true},
		{name: "记得喂过饭", text: "记得喂过饭吗", wantAbsent: true},
		{name: "昨晚顶号的事", text: "昨晚顶号的事", wantAbsent: true},
		{name: "我认识不是查询", text: "我认识阿明", wantAbsent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, in := fixture()
			in.Event.ID = "live:room:check"
			in.Event.Text = tc.text
			in.Event.FromID = "1001"
			in.Event.FromName = "A"
			in.Event.At = base.Add(2 * time.Minute)
			in.Memory.Focus = tc.focus
			subject, user := questionSubjectOf(t, in)
			if tc.wantAbsent {
				if subject.Kind != "" || subject.Text != "" {
					t.Fatalf("不应产生主体分类：%+v", subject)
				}
				return
			}
			if subject.Kind != tc.wantKind || subject.Text == "" || subject.Check != tc.wantCheck {
				t.Fatalf("分类 = %+v，期望 kind=%s check=%v", subject, tc.wantKind, tc.wantCheck)
			}
			if tc.wantName != "" && (subject.Match == nil || subject.Match.Name != tc.wantName) {
				if tc.wantKind == SubjectUnresolved {
					if subject.Text != tc.wantName {
						t.Fatalf("主体 = %q，期望 %q", subject.Text, tc.wantName)
					}
				} else {
					t.Fatalf("命中证据 = %+v，期望 %s", subject.Match, tc.wantName)
				}
			}
			if tc.wantHint != "" && !strings.Contains(user, tc.wantHint) {
				t.Fatalf("instructions 缺少提示 %q：%s", tc.wantHint, user)
			}
		})
	}
}

// TestSubjectCandidatesIncludeCheckName 回忆核查的昵称也要进入候选名单。
func TestSubjectCandidatesIncludeCheckName(t *testing.T) {
	_, in := fixture()
	in.Event.Text = "记得今夜琴声温柔吗"
	raw, _ := preparedContext(t, in)
	var current struct {
		Candidates []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"subject_candidates"`
	}
	if err := json.Unmarshal(raw["current_event"], &current); err != nil {
		t.Fatalf("current_event 解析失败：%v", err)
	}
	for _, candidate := range current.Candidates {
		if candidate.Name == "今夜琴声温柔" && candidate.Source == "question" {
			return
		}
	}
	t.Fatalf("候选名单缺少回忆核查昵称：%+v", current.Candidates)
}

// TestResolveQuestionTargetMatchesPrepare 生成前的预取解析与 Prepare 同口径：
// 房间候选给出 UID；回复目标只有昵称时 UID 为空；普通聊天返回 nil。
func TestResolveQuestionTargetMatchesPrepare(t *testing.T) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)

	_, in := fixture()
	in.Event.Text = "贞德是谁"
	in.Memory.Focus = []memory.Message{inboundFrom("live:room:j1", "7001", "贞德", "来了", base)}
	target := ResolveQuestionTarget(in)
	if target == nil || target.Kind != SubjectRoomCandidate || target.UID != "7001" || target.Name != "贞德" {
		t.Fatalf("房间候选预取不符：%+v", target)
	}

	_, in = fixture()
	in.Event.Text = "贞德是谁"
	in.Event.Meta = json.RawMessage(`{"reply_to_id":"live:room:j2","reply_to_name":"贞德"}`)
	target = ResolveQuestionTarget(in)
	if target == nil || target.Kind != SubjectRoomCandidate || target.UID != "" || target.Name != "贞德" {
		t.Fatalf("回复目标预取不符：%+v", target)
	}

	_, in = fixture()
	in.Event.Text = "今天吃什么"
	if target := ResolveQuestionTarget(in); target != nil {
		t.Fatalf("普通聊天不应有主体：%+v", target)
	}

	_, in = fixture()
	in.Event.Text = "我是谁"
	target = ResolveQuestionTarget(in)
	if target == nil || target.Kind != SubjectSpeaker {
		t.Fatalf("问发言人自己应判 speaker：%+v", target)
	}
}
