package store

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

func TestEventValidation(t *testing.T) {
	good := event.Event{ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room", Text: "你好", At: time.Now(), Status: event.StatusPending}
	if err := ValidateEvent(good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*event.Event){
		"空ID": func(e *event.Event) { e.ID = "" }, "错前缀": func(e *event.Event) { e.ID = "qq:room:1" },
		"缺平台ID": func(e *event.Event) { e.ID = "live:room:" }, "空会话": func(e *event.Event) { e.Session = " " },
		"空正文": func(e *event.Event) { e.Text = " " }, "过长": func(e *event.Event) { e.Text = strings.Repeat("鱼", 4001) },
		"错误UTF8": func(e *event.Event) { e.Text = string([]byte{255}) }, "零时间": func(e *event.Event) { e.At = time.Time{} },
		"DM降为公开": func(e *event.Event) { e.Channel = event.ChDM; e.ID = "dm:u:1" },
		"直播私密":   func(e *event.Event) { e.Private = true }, "无效Meta": func(e *event.Event) { e.Meta = json.RawMessage(`{`) },
		"入事件已完成": func(e *event.Event) { e.Status = event.StatusDone },
		"出事件待处理": func(e *event.Event) { e.Dir = event.DirOut },
	} {
		t.Run(name, func(t *testing.T) {
			bad := good
			mutate(&bad)
			if !errors.Is(ValidateEvent(bad), ErrInvalid) {
				t.Fatal("必须拒绝无效事件")
			}
		})
	}
	good.Dir = event.DirOut
	for _, status := range []event.Status{event.StatusDone, event.StatusRetry, event.StatusDead} {
		good.Status = status
		if err := ValidateEvent(good); err != nil {
			t.Fatal(err)
		}
	}
	good.Text = strings.Repeat("鱼", 4000)
	if err := ValidateEvent(good); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionEntireMatrix(t *testing.T) {
	edges := map[event.Status][]event.Status{
		event.StatusPending:  {event.StatusSkipped, event.StatusQueued},
		event.StatusQueued:   {event.StatusReplying, event.StatusRetry, event.StatusDead, event.StatusSkipped, event.StatusDone},
		event.StatusReplying: {event.StatusSent, event.StatusRetry, event.StatusDead, event.StatusQueued},
		event.StatusSent:     {event.StatusDone, event.StatusRetry}, event.StatusRetry: {event.StatusQueued, event.StatusDead},
	}
	all := []event.Status{event.StatusPending, event.StatusSkipped, event.StatusQueued, event.StatusReplying, event.StatusSent, event.StatusDone, event.StatusRetry, event.StatusDead, "bad"}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, allowed := range edges[from] {
				want = want || allowed == to
			}
			err := ValidateTransition([]event.Status{from}, to)
			if (err == nil) != want {
				t.Fatalf("%s→%s: %v", from, to, err)
			}
		}
	}
	if !errors.Is(ValidateTransition(nil, event.StatusQueued), ErrInvalid) {
		t.Fatal("空from未拒绝")
	}
	if !errors.Is(ValidateTransition([]event.Status{event.StatusPending, event.StatusDone}, event.StatusQueued), ErrInvalid) {
		t.Fatal("不能混入非法边")
	}
}

func TestTaskAndMemoryValidation(t *testing.T) {
	task := Task{ID: "task-1", EventID: "live:room:1", Channel: event.ChLive, Session: "room", Brain: "openai", Priority: 2, CreatedAt: time.Now()}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Task){func(x *Task) { x.ID = "" }, func(x *Task) { x.EventID = "" }, func(x *Task) { x.Session = "" }, func(x *Task) { x.Channel = "bad" }, func(x *Task) { x.Brain = "" }, func(x *Task) { x.Priority = 4 }, func(x *Task) { x.Priority = -1 }, func(x *Task) { x.CreatedAt = time.Time{} }, func(x *Task) { z := time.Time{}; x.Deadline = &z }} {
		bad := task
		mutate(&bad)
		if !errors.Is(bad.Validate(), ErrInvalid) {
			t.Fatal("无效任务被接受")
		}
	}
	record := MemoryRecord{Record: memory.Record{ID: "m1", Summary: "喜欢鱼", Importance: .5, Confidence: .8, ValidFrom: time.Now()}, Source: "owner_direct", Kind: "fact", Title: "偏好"}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*MemoryRecord){func(x *MemoryRecord) { x.Source = "" }, func(x *MemoryRecord) { x.Kind = "unknown" }, func(x *MemoryRecord) { x.Title = "" }, func(x *MemoryRecord) { x.Summary = "" }, func(x *MemoryRecord) { x.Importance = math.NaN() }, func(x *MemoryRecord) { x.Confidence = 1.1 }, func(x *MemoryRecord) { x.ValidFrom = time.Time{} }, func(x *MemoryRecord) { x.Private = true }, func(x *MemoryRecord) { x.Channel = event.ChDM; x.Session = "u" }, func(x *MemoryRecord) { x.EventIDs = []string{""} }} {
		bad := record
		mutate(&bad)
		if !errors.Is(bad.Validate(), ErrInvalid) {
			t.Fatal("无效记忆被接受")
		}
	}
	record.Private = true
	record.Channel = event.ChDM
	record.Session = "u"
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
}
