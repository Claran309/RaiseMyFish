package extractworker

// 本文件测 fact 记录的去重主键：同人同事实只留一条。

import (
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
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
	if !strings.HasPrefix(w.buildRecord(ev1, c).ID, "fact:2002:") {
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
