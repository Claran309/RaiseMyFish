package extractworker

// 本文件测 fact 记录的去重主键：同人同事实只留一条。

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

func factCandidate(title string) memory.Candidate {
	return memory.Candidate{Kind: memory.KindFact, Title: "事实：" + title, Summary: title, Importance: 0.6, Confidence: 0.85}
}

// TestFactRecordIDDedupBySubject 同一人同一事实 → 同一主键；不同人 → 不同主键。
func TestFactRecordIDDedupBySubject(t *testing.T) {
	w := &Worker{opts: Options{Now: func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }}}
	c := factCandidate("我是学生")

	ev1 := event.Event{ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room", FromID: "2002", FromName: "乙", Text: "我是学生", At: time.Now()}
	ev2 := ev1
	ev2.ID = "live:room:2" // 不同事件，同一个人同一句话
	ev2.Text = "我是学生"

	if id1, id2 := w.buildRecord(ev1, c).ID, w.buildRecord(ev2, c).ID; id1 != id2 {
		t.Errorf("同人同事实应去重：%q vs %q", id1, id2)
	}
	if !strings.HasPrefix(w.buildRecord(ev1, c).ID, "fact:public:2002:") {
		t.Errorf("事实主键格式不符：%q", w.buildRecord(ev1, c).ID)
	}

	// 不同人
	ev3 := ev1
	ev3.FromID = "3003"
	if w.buildRecord(ev1, c).ID == w.buildRecord(ev3, c).ID {
		t.Error("不同的人不应共享事实主键")
	}

	// 同一个人、不同事实
	other := factCandidate("我爱吃辣")
	if w.buildRecord(ev1, c).ID == w.buildRecord(ev1, other).ID {
		t.Error("不同事实不应共享主键")
	}
}

// TestFactRecordIDDomainSeparation 事实主键按可见域隔离：
// 公开域跨房间/平台共用，私密域按 channel+session 分域，DM-A/DM-B 互不共享。
func TestFactRecordIDDomainSeparation(t *testing.T) {
	w := &Worker{opts: Options{Now: func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }}}
	c := factCandidate("我是学生")
	base := event.Event{Channel: event.ChLive, Dir: event.DirIn, Session: "room", FromID: "2002", FromName: "乙", Text: "我是学生", At: time.Now()}

	liveID := w.buildRecord(base, c).ID
	if !strings.HasPrefix(liveID, "fact:public:2002:") {
		t.Errorf("公开事实主键格式不符：%q", liveID)
	}

	// 公开域跨房间、跨平台仍算同一条
	samePublic := []event.Event{base, base, base}
	samePublic[1].ID, samePublic[1].Session = "live:room2:1", "room2"
	samePublic[2].ID, samePublic[2].Channel = "comment:video:1", event.ChComment
	for _, ev := range samePublic[1:] {
		if id := w.buildRecord(ev, c).ID; id != liveID {
			t.Errorf("公开域应共用主键：%q vs %q", id, liveID)
		}
	}

	// 私密域：DM-A、DM-B、同 session 的 QQ 私聊都必须分开
	dmA := base
	dmA.Channel, dmA.Session, dmA.Private = event.ChDM, "dm-a", true
	dmB := dmA
	dmB.Session = "dm-b"
	qqA := dmA
	qqA.Channel = event.ChQQ

	ids := map[string]string{
		"dm-a": w.buildRecord(dmA, c).ID,
		"dm-b": w.buildRecord(dmB, c).ID,
		"qq-a": w.buildRecord(qqA, c).ID,
	}
	if !strings.HasPrefix(ids["dm-a"], "fact:private:dm:dm-a:2002:") {
		t.Errorf("私密事实主键格式不符：%q", ids["dm-a"])
	}
	for got, want := range map[string]string{"dm-b": ids["dm-a"], "qq-a": ids["dm-a"]} {
		if ids[got] == want {
			t.Errorf("%s 与 dm-a 不应共享主键：%q", got, ids[got])
		}
	}
	if ids["dm-a"] == liveID {
		t.Error("私密事实不应与公开事实共享主键")
	}
}

// TestNonFactRecordIDStillEventScoped 非 fact 记录仍按事件去重（行为不变）。
func TestNonFactRecordIDStillEventScoped(t *testing.T) {
	w := &Worker{opts: Options{Now: func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) }}}
	c := memory.Candidate{Kind: memory.KindPromise, Title: "承诺：一定帮", Summary: "一定帮", Importance: 0.9, Confidence: 0.95}
	ev1 := event.Event{ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room", FromID: "2002", Text: "一定帮", At: time.Now()}
	ev2 := ev1
	ev2.ID = "live:room:2"

	if w.buildRecord(ev1, c).ID == w.buildRecord(ev2, c).ID {
		t.Error("非事实类仍应随事件区分")
	}
}

// staticExtractor 返回固定候选，用来单独测 worker 的落库护栏。
type staticExtractor []memory.Candidate

func (s staticExtractor) Extract(context.Context, event.Event, memory.ExtractOptions) ([]memory.Candidate, error) {
	return s, nil
}

// TestFactRecordCarriesSourceEventEvidence #93：事实记录必须带可核对的来源片段。
func TestFactRecordCarriesSourceEventEvidence(t *testing.T) {
	w := &Worker{opts: Options{Now: func() time.Time { return baseTime }}}
	ev := event.Event{
		ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "2002", FromName: "乙", Text: strings.Repeat("鱼", 260), At: baseTime,
	}
	rec := w.buildRecord(ev, factCandidate("我是学生"))
	if len(rec.Evidence) != 1 {
		t.Fatalf("fact 应带 1 条来源证据：%+v", rec.Evidence)
	}
	got := rec.Evidence[0]
	if got.Field != "source_event" || got.Type != store.ViewerSummaryBasisObserved {
		t.Errorf("证据字段/类型不符：%+v", got)
	}
	if n := utf8.RuneCountInString(got.Text); n != evidenceTextRunes {
		t.Errorf("证据片段长度 = %d，期望截断到 %d", n, evidenceTextRunes)
	}
}

// TestExtractEventDropsFactWithoutEvidence 无证据事实不落库（正文为空时无法举证）。
func TestExtractEventDropsFactWithoutEvidence(t *testing.T) {
	w := newWorker(t, &fakeSource{}, &fakeRepo{}, NewMemoryWatermark(), Options{
		Now:       func() time.Time { return baseTime },
		Extractor: staticExtractor{factCandidate("我是学生")},
	})
	ev := event.Event{
		ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "2002", FromName: "乙", Text: "", At: baseTime,
	}
	records, skipped, err := w.extractEvent(context.Background(), ev)
	if err != nil {
		t.Fatalf("extractEvent 失败：%v", err)
	}
	if !skipped || len(records) != 0 {
		t.Errorf("无证据事实应被丢弃：records=%+v skipped=%v", records, skipped)
	}
}

// TestExtractEventKeepsFactWithEvidence 有正文时事实正常落库并带证据。
func TestExtractEventKeepsFactWithEvidence(t *testing.T) {
	w := newWorker(t, &fakeSource{}, &fakeRepo{}, NewMemoryWatermark(), Options{
		Now:       func() time.Time { return baseTime },
		Extractor: staticExtractor{factCandidate("我是学生")},
	})
	ev := event.Event{
		ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "2002", FromName: "乙", Text: "我是学生", At: baseTime,
	}
	records, skipped, err := w.extractEvent(context.Background(), ev)
	if err != nil {
		t.Fatalf("extractEvent 失败：%v", err)
	}
	if skipped || len(records) != 1 {
		t.Fatalf("有证据事实应保留：records=%+v skipped=%v", records, skipped)
	}
	if len(records[0].Evidence) != 1 || records[0].Evidence[0].Text != "我是学生" {
		t.Errorf("证据不符：%+v", records[0].Evidence)
	}
}
