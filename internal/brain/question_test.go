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
	Hint string `json:"hint"`
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
