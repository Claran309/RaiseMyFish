package brain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

type correctionView struct {
	EventID            string                     `json:"event_id"`
	Speaker            struct{ ID, Name string }  `json:"speaker"`
	Text               string                     `json:"text"`
	Denied             *struct{ ID, Name string } `json:"denied"`
	Claimed            *struct{ ID, Name string } `json:"claimed"`
	SupersededEventIDs []string                   `json:"superseded_event_ids"`
	RetractedRecordIDs []string                   `json:"retracted_record_ids"`
	RetractedFactKeys  []string                   `json:"retracted_fact_keys"`
	OriginalEvidence   []struct {
		EventID string                    `json:"event_id"`
		Text    string                    `json:"text"`
		Speaker struct{ ID, Name string } `json:"speaker"`
	} `json:"original_evidence"`
	Note string `json:"note"`
}

func correctionFixture(t *testing.T) (ContextInput, time.Time) {
	t.Helper()
	base := time.Date(2026, 11, 10, 20, 0, 0, 0, time.Local)
	_, in := fixture()
	in.Event.ID = "live:room:a3"
	in.Event.FromID = "1001"
	in.Event.FromName = "A"
	in.Event.Text = "那是谁说的"
	in.Event.At = base.Add(4 * time.Minute)
	in.Event.ReplyTo = ""
	in.Memory.Focus = []memory.Message{
		inboundFrom("live:room:a1", "1001", "A", "刚才谁说要吃鱼", base),
		inboundFrom("live:room:b1", "2002", "B", "我要吃鱼", base.Add(time.Minute)),
		{EventID: "live:room:r1", Channel: event.ChLive, Session: "room", Dir: event.DirOut, Text: "你说要吃鱼", ReplyTo: "live:room:a1", At: base.Add(90 * time.Second)},
		inboundFrom("live:room:a2", "1001", "A", "那句不是我说的，是B说的", base.Add(2*time.Minute)),
	}
	in.Memory.Records = []memory.Record{
		{ID: "rec-wrong", Channel: event.ChLive, Session: "room", Summary: "A说想吃鱼", Importance: .9, Confidence: .8, EventIDs: []string{"live:room:r1"}},
		{ID: "rec-keep", Channel: event.ChLive, Session: "room", Summary: "B在直播间聊过吃的", Importance: .5, Confidence: .8, EventIDs: []string{"live:room:b1"}},
	}
	in.Shared.Facts = []memory.Fact{
		{Key: "wrong_owner", Value: "A想吃鱼", Channel: event.ChLive, Session: "room", SourceID: "live:room:r1", At: base.Add(90 * time.Second)},
		{Key: "b_likes_fish", Value: "B想吃鱼", Channel: event.ChLive, Session: "room", SourceID: "live:room:b1", At: base.Add(time.Minute)},
	}
	return in, base
}

func TestCorrectionSupersedesAttributionAndDerivedData(t *testing.T) {
	in, _ := correctionFixture(t)
	raw, user := preparedContext(t, in)
	if !strings.Contains(user, "corrections 是已经发生的纠正") {
		t.Fatalf("instructions 缺少纠正提示：%s", user)
	}
	var corrections []correctionView
	if err := json.Unmarshal(raw["corrections"], &corrections); err != nil {
		t.Fatalf("corrections 解析失败：%v", err)
	}
	if len(corrections) != 1 {
		t.Fatalf("纠正条数 = %d，期望 1：%+v", len(corrections), corrections)
	}
	entry := corrections[0]
	if entry.EventID != "live:room:a2" || entry.Speaker.Name != "A" {
		t.Fatalf("纠正来源错误：%+v", entry)
	}
	if entry.Denied == nil || entry.Denied.Name != "A" {
		t.Fatalf("被否认归属应为 A：%+v", entry.Denied)
	}
	if entry.Claimed == nil || entry.Claimed.ID != "2002" || entry.Claimed.Name != "B" {
		t.Fatalf("正确归属应为 B：%+v", entry.Claimed)
	}
	if len(entry.OriginalEvidence) != 1 || entry.OriginalEvidence[0].EventID != "live:room:b1" ||
		!strings.Contains(entry.OriginalEvidence[0].Text, "我要吃鱼") || entry.OriginalEvidence[0].Speaker.Name != "B" {
		t.Fatalf("原始证据应指向 B 的原话：%+v", entry.OriginalEvidence)
	}
	if len(entry.SupersededEventIDs) != 1 || entry.SupersededEventIDs[0] != "live:room:r1" {
		t.Fatalf("失效回复应包含 r1：%+v", entry.SupersededEventIDs)
	}
	if len(entry.RetractedRecordIDs) != 1 || entry.RetractedRecordIDs[0] != "rec-wrong" {
		t.Fatalf("撤回摘要应包含 rec-wrong：%+v", entry.RetractedRecordIDs)
	}
	if len(entry.RetractedFactKeys) != 1 || entry.RetractedFactKeys[0] != "wrong_owner" {
		t.Fatalf("撤回事实应包含 wrong_owner：%+v", entry.RetractedFactKeys)
	}
	if !strings.Contains(entry.Note, "先承认纠正") || !strings.Contains(entry.Note, "B") {
		t.Fatalf("纠正说明不完整：%s", entry.Note)
	}

	var records []memory.Record
	if err := json.Unmarshal(raw["records"], &records); err != nil {
		t.Fatalf("records 解析失败：%v", err)
	}
	if len(records) != 1 || records[0].ID != "rec-keep" {
		t.Fatalf("被撤回的摘要不应再注入：%+v", records)
	}
	var facts []memory.Fact
	if err := json.Unmarshal(raw["shared"], &facts); err != nil {
		t.Fatalf("shared 解析失败：%v", err)
	}
	if len(facts) != 1 || facts[0].Key != "b_likes_fish" {
		t.Fatalf("被撤回的事实不应再注入：%+v", facts)
	}
	if strings.Contains(string(raw["records"]), "rec-wrong") || strings.Contains(string(raw["shared"]), "wrong_owner") {
		t.Fatal("撤回内容仍出现在 records/shared 资料段里")
	}
}

// TestCorrectionReaskKeepsOriginalEvidence 重问时纠正仍来自焦点窗口，
// 并保留正确归属人的原话，供模型「先承认再按正确归属回答」。
func TestCorrectionReaskKeepsOriginalEvidence(t *testing.T) {
	in, base := correctionFixture(t)
	// 第二次重问：纠正已不在当前事件，而在焦点窗口里。
	in.Event.ID = "live:room:a4"
	in.Event.Text = "所以到底是谁说的"
	in.Event.At = base.Add(5 * time.Minute)
	raw, user := preparedContext(t, in)
	if !strings.Contains(user, "corrections 是已经发生的纠正") {
		t.Fatal("重问时未注入纠正提示")
	}
	var corrections []correctionView
	if err := json.Unmarshal(raw["corrections"], &corrections); err != nil || len(corrections) != 1 {
		t.Fatalf("重问时纠正丢失：%v %+v", err, corrections)
	}
	if corrections[0].Claimed == nil || corrections[0].Claimed.Name != "B" ||
		len(corrections[0].OriginalEvidence) == 0 || corrections[0].OriginalEvidence[0].EventID != "live:room:b1" {
		t.Fatalf("重问时正确归属/原始证据不完整：+%v", corrections[0])
	}
}

// TestCorrectionNotTriggeredByPlainNegation 普通否定不能误判成纠正。
func TestCorrectionNotTriggeredByPlainNegation(t *testing.T) {
	_, in := fixture()
	in.Event.ID = "live:room:n1"
	in.Event.FromID = "1001"
	in.Event.FromName = "A"
	in.Event.Text = "我不是这个意思"
	raw, _ := preparedContext(t, in)
	if raw["corrections"] != nil {
		t.Fatal("普通否定不应产生 corrections")
	}
}
