package brain

// 本文件测 B1/N02 的生成输入：当前事件身份、按 reply_to 配对的焦点轮次、
// 快照时间与来源水位，以及焦点段在预算裁剪中的受保护顺序。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

// focusInputSection 是测试侧对 <context> JSON 的投影。
type focusInputSection struct {
	Current struct {
		ID      string `json:"id"`
		Speaker struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"speaker"`
		ReplyTo     string `json:"reply_to"`
		ReplyTarget *struct {
			EventID string `json:"event_id"`
			Name    string `json:"name"`
			Kind    string `json:"kind"`
		} `json:"reply_target"`
		SubjectCandidates []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"subject_candidates"`
	} `json:"current_event"`
	Snapshot struct {
		AsOf              time.Time `json:"as_of"`
		SourceWatermark   string    `json:"source_watermark"`
		RecentUntil       string    `json:"recent_until"`
		FocusUntil        string    `json:"focus_until"`
		PersonaVersion    string    `json:"persona_version"`
		KnowledgeRevision string    `json:"knowledge_revision"`
	} `json:"snapshot"`
	Recent []memory.Message `json:"recent"`
	Focus  []memory.Message `json:"focus_turns"`
}

func decodeFocusSection(t *testing.T, prepared Prepared) focusInputSection {
	t.Helper()
	var got focusInputSection
	if err := json.Unmarshal([]byte(sectionBody(t, prepared.Messages[1].Content, "context")), &got); err != nil {
		t.Fatalf("解析 <context> 失败：%v", err)
	}
	return got
}

// focusFixture 构造「A 先问、鱼回过、B 刷屏、A 追问」的输入：
// Recent 只有房间流，Focus 保留 A 的原问题与鱼的回复。
func focusFixture(t *testing.T) (Task, ContextInput) {
	t.Helper()
	task, in := fixture()
	base := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	in.Persona.Meta.DisplayName = "测试鱼"
	in.Event = event.Event{
		ID: "live:room:q2", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "42", FromName: "阿问", Text: "你还记得我刚才问的吗 @阿狗", At: base,
		Meta: json.RawMessage(`{"reply_to_id":"live:room:q1","reply_to_name":"阿问"}`),
	}
	task.EventID = in.Event.ID
	in.Memory.Recent = []memory.Message{
		{EventID: "live:room:b1", Channel: event.ChLive, Session: "room", Dir: event.DirIn, FromID: "99", FromName: "刷屏的", Text: "插一句", At: base.Add(-30 * time.Second)},
	}
	in.Memory.Focus = []memory.Message{
		{EventID: "live:room:q1", Channel: event.ChLive, Session: "room", Dir: event.DirIn, FromID: "42", FromName: "阿问", Text: "我是谁", At: base.Add(-2 * time.Minute)},
		{EventID: "live:out:room:r1", Channel: event.ChLive, Session: "room", Dir: event.DirOut, FromID: "9", FromName: "测试鱼", Text: "你是阿问", ReplyTo: "live:room:q1", At: base.Add(-90 * time.Second)},
		{EventID: "live:room:q2", Channel: event.ChLive, Session: "room", Dir: event.DirIn, FromID: "42", FromName: "阿问", Text: "你还记得我刚才问的吗 @阿狗", At: base},
	}
	return task, in
}

// TestPrepareInjectsCurrentEventAndFocus 验收：B 插话后，模型输入仍明确本轮是 A，
// 且带原问题、鱼的回复与 reply_to 引用关系。
func TestPrepareInjectsCurrentEventAndFocus(t *testing.T) {
	task, in := focusFixture(t)
	prepared, err := Prepare(context.Background(), task, in, DefaultBudget())
	if err != nil {
		t.Fatalf("Prepare 失败：%v", err)
	}
	got := decodeFocusSection(t, prepared)

	if got.Current.ID != "live:room:q2" || got.Current.Speaker.ID != "42" || got.Current.Speaker.Name != "阿问" {
		t.Fatalf("当前事件身份不符：%+v", got.Current)
	}
	if got.Current.ReplyTarget == nil || got.Current.ReplyTarget.EventID != "live:room:q1" || got.Current.ReplyTarget.Name != "阿问" || got.Current.ReplyTarget.Kind != "other" {
		t.Fatalf("reply_target 不符：%+v", got.Current.ReplyTarget)
	}
	sources := make(map[string]string, len(got.Current.SubjectCandidates))
	for _, candidate := range got.Current.SubjectCandidates {
		sources[candidate.Source] = candidate.Name
	}
	if sources["speaker"] != "阿问" || sources["mention"] != "阿狗" {
		t.Fatalf("主体候选不符：%+v", got.Current.SubjectCandidates)
	}

	if len(got.Focus) != 3 {
		t.Fatalf("焦点轮次 = %d，期望 3：%+v", len(got.Focus), got.Focus)
	}
	if got.Focus[0].EventID != "live:room:q1" || got.Focus[0].Text != "我是谁" {
		t.Fatalf("原问题丢失：%+v", got.Focus[0])
	}
	if got.Focus[1].EventID != "live:out:room:r1" || got.Focus[1].ReplyTo != "live:room:q1" || got.Focus[1].Text != "你是阿问" {
		t.Fatalf("鱼的回复或配对关系丢失：%+v", got.Focus[1])
	}
	if got.Focus[2].EventID != "live:room:q2" {
		t.Fatalf("本轮追问不在焦点末尾：%+v", got.Focus)
	}

	// 房间流按 Recent 原样注入，不能替代焦点轮次。
	if len(got.Recent) != 1 || got.Recent[0].FromName != "刷屏的" {
		t.Fatalf("Recent 不符：%+v", got.Recent)
	}

	if got.Snapshot.PersonaVersion != "v1" {
		t.Fatalf("快照缺少人格版本：%+v", got.Snapshot)
	}
	if !got.Snapshot.AsOf.Equal(in.Event.At) {
		t.Fatalf("as_of = %v，期望当前事件时间 %v", got.Snapshot.AsOf, in.Event.At)
	}
	if !strings.HasPrefix(got.Snapshot.SourceWatermark, in.Event.At.UTC().Format(time.RFC3339Nano)+"#live:room:q2") {
		t.Fatalf("来源水位不符：%q", got.Snapshot.SourceWatermark)
	}
	if !strings.Contains(got.Snapshot.FocusUntil, "live:room:q2") || !strings.Contains(got.Snapshot.RecentUntil, "live:room:b1") {
		t.Fatalf("分段水位不符：%+v", got.Snapshot)
	}
}

// TestPrepareBudgetProtectsFocusTurns 预算紧张时先裁房间流，焦点轮次最后才让位。
func TestPrepareBudgetProtectsFocusTurns(t *testing.T) {
	task, in := focusFixture(t)
	spam := make([]memory.Message, 0, 30)
	for i := 0; i < 30; i++ {
		spam = append(spam, memory.Message{
			EventID: fmt.Sprintf("live:room:b%02d", i), Channel: event.ChLive, Session: "room", Dir: event.DirIn,
			FromID: "99", FromName: "刷屏的", Text: strings.Repeat("刷屏", 40),
			At: in.Event.At.Add(-time.Duration(60-i) * time.Second),
		})
	}
	in.Memory.Recent = spam

	prepared, err := Prepare(context.Background(), task, in, Budget{MaxPromptTokens: 3000, KnowledgeBudgetPercent: 10})
	if err != nil {
		t.Fatalf("超预算应受控裁剪而不是报错：%v", err)
	}
	if !prepared.Truncated || estimate(prepared.Messages) > 3000 {
		t.Fatalf("裁剪状态不符：truncated=%v estimate=%d", prepared.Truncated, estimate(prepared.Messages))
	}
	got := decodeFocusSection(t, prepared)
	if len(got.Focus) != 3 {
		t.Fatalf("焦点轮次被连带裁掉：%+v", got.Focus)
	}
	if len(got.Recent) >= len(spam) {
		t.Fatalf("房间流未被裁剪：%d 条", len(got.Recent))
	}
	for _, want := range []string{"我是谁", "你是阿问", "live:room:q1"} {
		if !strings.Contains(prepared.Context, want) {
			t.Errorf("裁剪后资料段缺少 %q", want)
		}
	}
}

// TestPrepareRejectsFocusLeak 焦点段同样受统一 Scope 校验：越界消息不能进入生成输入。
func TestPrepareRejectsFocusLeak(t *testing.T) {
	task, in := fixture()
	in.Memory.Focus = []memory.Message{{
		EventID: "live:other:q1", Channel: event.ChLive, Session: "other", Dir: event.DirIn,
		Text: "别人的会话", At: time.Now(),
	}}
	prepared, err := Prepare(context.Background(), task, in, DefaultBudget())
	if err == nil {
		t.Fatalf("越界焦点未被拒绝：%+v", prepared)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindInput {
		t.Fatalf("错误类别应为 input：%v", err)
	}
}

// TestPrepareFocusSortAndSnapshotStable 同一输入重复准备必须得到逐字节相同的资料段。
func TestPrepareFocusSortAndSnapshotStable(t *testing.T) {
	task, in := focusFixture(t)
	// 打乱输入顺序，装配层必须按时间排序并稳定产出。
	in.Memory.Focus[0], in.Memory.Focus[2] = in.Memory.Focus[2], in.Memory.Focus[0]
	first, err := Prepare(context.Background(), task, in, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Prepare(context.Background(), task, in, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if first.Context != second.Context || first.Messages[1].Content != second.Messages[1].Content {
		t.Fatal("同一输入两次准备的资料段不一致")
	}
	got := decodeFocusSection(t, first)
	if got.Focus[0].EventID != "live:room:q1" || got.Focus[2].EventID != "live:room:q2" {
		t.Fatalf("焦点轮次未按时间升序：%+v", got.Focus)
	}
}
