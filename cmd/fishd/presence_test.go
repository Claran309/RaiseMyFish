package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/presence"
)

// capturePublisher 记录转发时 presence 的摘要，用来验证「先写入、再转发」。
type capturePublisher struct {
	result bool
	err    error
	calls  int
	got    event.Event

	table  *presence.Table
	during string
}

func (p *capturePublisher) Publish(_ context.Context, ev event.Event) (bool, error) {
	p.calls++
	p.got = ev
	if p.table != nil {
		p.during = p.table.Summary(time.Now())
	}
	return p.result, p.err
}

func TestPresencePublisherMarksWatching(t *testing.T) {
	table := presence.NewTable()
	inner := &capturePublisher{result: true, table: table}
	wrapped := withPresence(inner, table)

	ev := event.Event{ID: "live:30308220:1", Channel: event.ChLive, Dir: event.DirIn, Session: "30308220", Text: "主播好"}
	ok, err := wrapped.Publish(context.Background(), ev)
	if err != nil || !ok {
		t.Fatalf("Publish = %v/%v，期望透传成功", ok, err)
	}
	if inner.calls != 1 || inner.got.ID != ev.ID {
		t.Fatalf("inner 调用 = %d 次，事件 = %+v", inner.calls, inner.got)
	}
	if inner.during != "在看直播 30308220" {
		t.Errorf("转发时摘要 = %q，期望先写入 watching", inner.during)
	}

	active := table.Active(time.Now())
	if len(active) != 1 {
		t.Fatalf("Active = %+v，期望 1 条", active)
	}
	got := active[0]
	if got.Kind != presence.KindWatching || got.Channel != string(event.ChLive) || got.Detail != "30308220" {
		t.Errorf("watching 状态不符：%+v", got)
	}
	if got.TTL != presence.DefaultTTL {
		t.Errorf("TTL = %v，期望 %v", got.TTL, presence.DefaultTTL)
	}
}

func TestPresencePublisherSkipsOtherChannels(t *testing.T) {
	table := presence.NewTable()
	inner := &capturePublisher{result: true, table: table}
	wrapped := withPresence(inner, table)

	ev := event.Event{ID: "comment:1:1", Channel: event.ChComment, Dir: event.DirIn, Session: "1", Text: "好"}
	if _, err := wrapped.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish 失败：%v", err)
	}
	if inner.calls != 1 {
		t.Errorf("inner 调用 = %d，期望 1", inner.calls)
	}
	if got := table.Summary(time.Now()); got != "" {
		t.Errorf("非 live 事件不应记 watching：%q", got)
	}
}

func TestPresencePublisherPassesThroughResult(t *testing.T) {
	boom := errors.New("落库失败")
	inner := &capturePublisher{result: false, err: boom}
	wrapped := withPresence(inner, presence.NewTable())

	ok, err := wrapped.Publish(context.Background(), event.Event{Channel: event.ChLive, Session: "1"})
	if ok || !errors.Is(err, boom) {
		t.Errorf("Publish = %v/%v，期望透传 false/boom", ok, err)
	}
}

func TestWithPresenceFallsBackWhenMissing(t *testing.T) {
	inner := &capturePublisher{result: true}
	if got := withPresence(inner, nil); got != inner {
		t.Error("table 为 nil 时应原样返回 inner")
	}
	if got := withPresence(nil, presence.NewTable()); got != nil {
		t.Error("inner 为 nil 时应返回 nil")
	}
}
