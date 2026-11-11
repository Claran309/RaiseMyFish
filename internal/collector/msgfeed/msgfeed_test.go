package msgfeed

// 本文件测消息事件化、去重与 Ack 水位语义（不联网）。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

func testMessage() Message {
	return Message{
		Feed:       "reply",
		BusinessID: TypeVideo,
		OID:        "123",
		RPID:       "777",
		Root:       "666",
		FromID:     "42",
		FromName:   "小明",
		Text:       "回复你",
		At:         time.Unix(1700000000, 0),
	}
}

func TestToEventMapsCommentThread(t *testing.T) {
	c, err := New(stubFetcher{}, Options{Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := c.toEvent(testMessage(), "8")
	if !ok {
		t.Fatal("有效消息被丢弃")
	}
	if ev.ID != "comment:123:777" || ev.Channel != event.ChComment || ev.Dir != event.DirIn || ev.Session != "123" || ev.ReplyTo != "777" {
		t.Fatalf("事件身份不符：%+v", ev)
	}
	if ev.FromID != "42" || ev.FromName != "小明" || ev.Text != "回复你" || ev.Private {
		t.Fatalf("事件内容不符：%+v", ev)
	}
	var meta struct {
		Source      string `json:"source"`
		Feed        string `json:"feed"`
		CommentType int    `json:"comment_type"`
		Root        string `json:"root"`
		Parent      string `json:"parent"`
	}
	if err := json.Unmarshal(ev.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Source != "bili-msgfeed" || meta.Feed != "reply" || meta.CommentType != TypeVideo || meta.Root != "666" || meta.Parent != "777" {
		t.Fatalf("Meta 不符：%+v", meta)
	}
}

func TestToEventSkipsInvalidAndUnsupported(t *testing.T) {
	c, err := New(stubFetcher{}, Options{Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*Message)
	}{
		{"我自己", func(m *Message) { m.FromID = "8" }},
		{"空正文", func(m *Message) { m.Text = "  " }},
		{"非法oid", func(m *Message) { m.OID = "abc" }},
		{"零rpid", func(m *Message) { m.RPID = "0" }},
		{"不支持类型", func(m *Message) { m.BusinessID = 14 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testMessage()
			tc.edit(&m)
			if _, ok := c.toEvent(m, "8"); ok {
				t.Fatal("应跳过")
			}
		})
	}
}

func TestToEventKeepsSupportedCommentTypes(t *testing.T) {
	c, _ := New(stubFetcher{}, Options{Log: zap.NewNop()})
	for _, business := range []int{TypeDraw, TypeArticle, TypeDynamic} {
		m := testMessage()
		m.BusinessID = business
		ev, ok := c.toEvent(m, "8")
		if !ok {
			t.Fatalf("类型 %d 应支持", business)
		}
		var meta struct {
			CommentType int `json:"comment_type"`
		}
		if err := json.Unmarshal(ev.Meta, &meta); err != nil || meta.CommentType != business {
			t.Fatalf("comment_type 未保留：%+v %v", meta, err)
		}
	}
}

type stubFetcher struct{}

func (stubFetcher) Fetch(context.Context) (Batch, error) { return Batch{}, errors.New("未配置") }

type batchFetcher struct {
	batch    Batch
	fetchErr error
	fetched  bool
}

func (f *batchFetcher) Fetch(context.Context) (Batch, error) {
	if f.fetchErr != nil {
		return Batch{}, f.fetchErr
	}
	f.fetched = true
	return f.batch, nil
}

func TestPollDedupesSameCommentAcrossFeeds(t *testing.T) {
	m1 := testMessage()
	m2 := testMessage()
	m2.Feed = "at"
	fetcher := &batchFetcher{batch: Batch{Messages: []Message{m1, m2}, SelfID: "8"}}
	c, err := New(fetcher, Options{Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan event.Event, 2)
	if err := c.poll(context.Background(), events, nil); err != nil {
		t.Fatal(err)
	}
	close(events)
	count := 0
	for range events {
		count++
	}
	if count != 1 {
		t.Fatalf("同一评论应只产一条事件，实际 %d", count)
	}
}

func TestPollCommitsOnlyAfterAck(t *testing.T) {
	committed := false
	fetcher := &batchFetcher{batch: Batch{
		Messages: []Message{testMessage()},
		SelfID:   "8",
		Commit:   func() error { committed = true; return nil },
	}}
	c, err := New(fetcher, Options{Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan event.Event)
	acks := make(chan collector.DeliveryAck)
	done := make(chan error, 1)
	go func() { done <- c.poll(context.Background(), events, acks) }()

	ev := <-events
	if committed {
		t.Fatal("确认落库前不应提交水位")
	}
	acks <- collector.DeliveryAck{EventID: ev.ID}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("确认后应提交水位")
	}
}

func TestPollDoesNotCommitOnAckFailure(t *testing.T) {
	committed := false
	fetcher := &batchFetcher{batch: Batch{
		Messages: []Message{testMessage()},
		SelfID:   "8",
		Commit:   func() error { committed = true; return nil },
	}}
	c, err := New(fetcher, Options{Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan event.Event)
	acks := make(chan collector.DeliveryAck)
	done := make(chan error, 1)
	go func() { done <- c.poll(context.Background(), events, acks) }()
	ev := <-events
	acks <- collector.DeliveryAck{EventID: ev.ID, Err: errors.New("落库失败")}
	if err := <-done; err == nil {
		t.Fatal("落库失败应返回错误")
	}
	if committed {
		t.Fatal("落库失败不应提交水位")
	}
}

func TestPollAuthErrorStops(t *testing.T) {
	fetcher := &batchFetcher{fetchErr: &Error{Kind: KindAuth}}
	c, err := New(fetcher, Options{Log: zap.NewNop(), Interval: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 1)
	events := make(chan event.Event)
	if err := c.Start(context.Background(), events, errs); !errors.Is(err, errAuth) {
		t.Fatalf("鉴权错误应停止采集：%v", err)
	}
}

func TestNewRejectsBadInterval(t *testing.T) {
	if _, err := New(stubFetcher{}, Options{Log: zap.NewNop(), Interval: time.Second}); err == nil {
		t.Fatal("过小间隔应拒绝")
	}
	if _, err := New(stubFetcher{}, Options{Log: zap.NewNop(), Interval: 2 * time.Hour}); err == nil {
		t.Fatal("过大间隔应拒绝")
	}
}
