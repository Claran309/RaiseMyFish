package memory

import (
	"math"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

func TestScope(t *testing.T) {
	for _, scope := range []Scope{{event.ChLive, "room", false}, {event.ChComment, "video", false}, {event.ChDM, "uid", true}, {event.ChQQ, "group", false}, {event.ChQQ, "friend", true}} {
		if err := ValidateScope(scope); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []Scope{{}, {event.ChLive, "room", true}, {event.ChComment, "v", true}, {event.ChDM, "uid", false}, {event.Channel("other"), "x", false}, {event.ChLive, " ", false}} {
		if err := ValidateScope(scope); err == nil {
			t.Fatalf("无效Scope被接受：%+v", scope)
		}
	}
}

func TestMemoryBoundaries(t *testing.T) {
	public := Scope{event.ChLive, "room", false}
	private := Scope{event.ChDM, "owner", true}
	message := Message{EventID: "live:room:1", Channel: event.ChLive, Session: "room", Dir: event.DirIn, Text: "你好", At: time.Now()}
	if err := (Context{Scope: public}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (SharedState{Scope: public}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Context{Scope: public, Recent: []Message{message}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Message){func(m *Message) { m.Private = true }, func(m *Message) { m.Session = "other" }, func(m *Message) { m.At = time.Time{} }, func(m *Message) { m.Dir = "bad" }, func(m *Message) { m.Text = " " }} {
		m := message
		mutate(&m)
		if err := (Context{Scope: public, Recent: []Message{m}}).Validate(); err == nil {
			t.Fatal("越界/无效Recent被接受")
		}
	}
	m := message
	m.Channel = event.ChComment
	m.Session = "video"
	if err := (Context{Scope: public, CrossChannel: []Message{m}}).Validate(); err != nil {
		t.Fatal(err)
	}
	m.Private = true
	if err := (Context{Scope: private, CrossChannel: []Message{m}}).Validate(); err == nil {
		t.Fatal("私密CrossChannel被接受")
	}
	m = message
	if err := (Context{Scope: public, CrossChannel: []Message{m}}).Validate(); err == nil {
		t.Fatal("同通道被当作CrossChannel")
	}
	m.Channel = event.ChDM
	m.Session = "other"
	m.Private = false
	if err := (Context{Scope: public, CrossChannel: []Message{m}}).Validate(); err == nil {
		t.Fatal("DM误标公开被接受")
	}
	record := Record{ID: "r", Summary: "记忆", Importance: .5, Confidence: .8, Private: true, Channel: event.ChDM, Session: "owner"}
	if err := (Context{Scope: private, Records: []Record{record}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Context{Scope: public, Records: []Record{record}}).Validate(); err == nil {
		t.Fatal("公开场景读到私密记录")
	}
	record.Session = "other"
	if err := (Context{Scope: private, Records: []Record{record}}).Validate(); err == nil {
		t.Fatal("跨私密会话记录被接受")
	}
	for _, score := range []float64{math.NaN(), math.Inf(1), -1, 1.1} {
		r := Record{ID: "r", Summary: "事实", Importance: score, Confidence: .8}
		if err := (Context{Scope: public, Records: []Record{r}}).Validate(); err == nil {
			t.Fatal("非法评分被接受")
		}
		r.Importance = .5
		r.Confidence = score
		if err := (Context{Scope: public, Records: []Record{r}}).Validate(); err == nil {
			t.Fatal("非法置信度被接受")
		}
	}
}

func TestSharedFactBoundaries(t *testing.T) {
	scope := Scope{event.ChLive, "room", false}
	fact := Fact{Key: "live.status", Value: "online", Channel: event.ChLive, Session: "room", SourceID: "probe:live", At: time.Now()}
	if err := (SharedState{Scope: scope, Facts: []Fact{fact}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Fact){func(f *Fact) { f.Private = true }, func(f *Fact) { f.SourceID = "" }, func(f *Fact) { f.At = time.Time{} }, func(f *Fact) { f.Key = "" }, func(f *Fact) { f.Channel = "bad" }} {
		f := fact
		mutate(&f)
		if err := (SharedState{Scope: scope, Facts: []Fact{f}}).Validate(); err == nil {
			t.Fatal("无效/私密事实被接受")
		}
	}
	private := Scope{event.ChDM, "owner", true}
	fact.Channel = event.ChDM
	fact.Session = "owner"
	fact.Private = true
	if err := (SharedState{Scope: private, Facts: []Fact{fact}}).Validate(); err != nil {
		t.Fatal(err)
	}
	fact.Session = "other"
	if err := (SharedState{Scope: private, Facts: []Fact{fact}}).Validate(); err == nil {
		t.Fatal("跨私密会话事实被接受")
	}
}
