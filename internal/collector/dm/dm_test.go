package dm

// 本文件测私信采集：会话解析、未读过滤、msg_id 去重、配置校验、鉴权停止。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

type fakeFetcher struct {
	batches [][]Message
	errs    []error
	calls   int
}

func (f *fakeFetcher) Fetch(context.Context) (Batch, error) {
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return Batch{}, f.errs[i]
	}
	if i < len(f.batches) {
		return Batch{Messages: f.batches[i]}, nil
	}
	return Batch{}, nil
}

// TestParseSessions 解析 mock 会话 JSON：字符串/数字 uid 都认，跳过自己发的。
func TestParseSessions(t *testing.T) {
	body := []byte(`{"code":0,"data":{"session_list":[
		{"talker_id":"2002","last_msg":{"msg_key":"9001","sender_uid":"2002","content":"在吗","timestamp":1700000000}},
		{"talker_id":3003,"last_msg":{"msg_key":9002,"sender_uid":3003,"content":"看看这个","timestamp":1700000100}},
		{"talker_id":"3747","last_msg":{"msg_key":"9003","sender_uid":"3747","content":"我自己发的","timestamp":1700000200}},
		{"talker_id":"4444","last_msg":{"msg_key":"9004","sender_uid":"4444","content":"","timestamp":1700000300}}
	]}}`)
	got, err := ParseSessions(body, "3747")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应解析出 2 条（自己发的/空正文跳过），实际 %d：%+v", len(got), got)
	}
	if got[0].ID != "9001" || got[0].PeerID != "2002" || got[0].Text != "在吗" {
		t.Errorf("第 1 条不符：%+v", got[0])
	}
	if got[1].PeerID != "3003" {
		t.Errorf("数字 uid 未解析：%+v", got[1])
	}
}

// TestParseSessionsCollectsAllPeers 私信全收：没有白名单过滤，所有会话都产出。
func TestParseSessionsCollectsAllPeers(t *testing.T) {
	body := []byte(`{"code":0,"data":{"session_list":[
		{"talker_id":"1001","last_msg":{"msg_key":"1","sender_uid":"1001","content":"甲","timestamp":1700000000}},
		{"talker_id":"1002","last_msg":{"msg_key":"2","sender_uid":"1002","content":"乙","timestamp":1700000001}},
		{"talker_id":"1003","last_msg":{"msg_key":"3","sender_uid":"1003","content":"丙","timestamp":1700000002}}
	]}}`)
	got, err := ParseSessions(body, "3747")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("所有会话都应采集，实际 %d：%+v", len(got), got)
	}
	for i, peer := range []string{"1001", "1002", "1003"} {
		if got[i].PeerID != peer {
			t.Errorf("第 %d 条 peer = %q，期望 %q", i+1, got[i].PeerID, peer)
		}
	}
}

// TestParseSessionsAuthError code≠0 归为鉴权错误。
func TestParseSessionsAuthError(t *testing.T) {
	_, err := ParseSessions([]byte(`{"code":-101,"message":"账号未登录"}`), "1")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("应归为鉴权错误：%v", err)
	}
}

// TestCollectorDedupByMsgID 同一 msg_id 只产出一条事件。
func TestCollectorDedupByMsgID(t *testing.T) {
	msg := Message{ID: "9001", PeerID: "2002", PeerName: "熟客乙", Text: "在吗", At: time.Now()}
	f := &fakeFetcher{batches: [][]Message{{msg}, {msg}}}
	c, err := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}

	events := make(chan event.Event, 4)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := c.poll(ctx, events, nil); err != nil {
			t.Fatalf("第 %d 轮失败：%v", i+1, err)
		}
	}
	if len(events) != 1 {
		t.Fatalf("应只产出一条事件，实际 %d", len(events))
	}
	ev := <-events
	if ev.Channel != event.ChDM || ev.Dir != event.DirIn || ev.Session != "2002" || !ev.Private {
		t.Errorf("事件字段不符：%+v", ev)
	}
	if ev.ID != "dm:2002:9001" {
		t.Errorf("事件 ID 不符：%q", ev.ID)
	}
	if ev.FromID != "2002" || ev.FromName != "熟客乙" {
		t.Errorf("主体不符：%+v", ev)
	}
}

// TestCollectorEmptyFieldsIgnored 缺 id/peer/text 的消息直接丢。
func TestCollectorEmptyFieldsIgnored(t *testing.T) {
	f := &fakeFetcher{batches: [][]Message{
		{{ID: "", PeerID: "1", Text: "x"}, {ID: "2", PeerID: "", Text: "x"}, {ID: "3", PeerID: "1", Text: "  "}},
	}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	events := make(chan event.Event, 4)
	if err := c.poll(context.Background(), events, nil); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("非法消息不应产出事件：%d", len(events))
	}
}

// TestNewValidatesOptions 参数校验。
func TestNewValidatesOptions(t *testing.T) {
	f := &fakeFetcher{}
	if _, err := New(nil, Options{Log: zap.NewNop()}); err == nil {
		t.Error("nil fetcher 应报错")
	}
	if _, err := New(f, Options{}); err == nil {
		t.Error("nil logger 应报错")
	}
	if _, err := New(f, Options{Interval: time.Second, Log: zap.NewNop()}); err == nil {
		t.Error("间隔过短应报错")
	}
	if _, err := New(f, Options{Interval: 2 * time.Hour, Log: zap.NewNop()}); err == nil {
		t.Error("间隔过长应报错")
	}
}

// TestStartStopsOnAuthError 鉴权错误 → Start 返回并上报。
func TestStartStopsOnAuthError(t *testing.T) {
	f := &fakeFetcher{errs: []error{&Error{Kind: KindAuth}}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	events := make(chan event.Event, 1)
	errs := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, events, errs) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("鉴权错误应返回错误")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("鉴权错误后未停止")
	}
}

// commitFetcher 返回固定 Batch，用于断言提交时序。
type commitFetcher struct {
	batch Batch
}

func (f *commitFetcher) Fetch(context.Context) (Batch, error) { return f.batch, nil }

// TestCollectorCommitsAfterEmission 消息全部入总线后才提交水位。
func TestCollectorCommitsAfterEmission(t *testing.T) {
	committed := 0
	f := &commitFetcher{batch: Batch{
		Messages: []Message{{ID: "1", PeerID: "2", Text: "hi", At: time.Now()}},
		Commit:   func() error { committed++; return nil },
	}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	events := make(chan event.Event, 1)
	if err := c.poll(context.Background(), events, nil); err != nil {
		t.Fatal(err)
	}
	if committed != 1 {
		t.Fatalf("入总线后应提交一次，实际 %d", committed)
	}
	if len(events) != 1 {
		t.Fatalf("应产出一条事件，实际 %d", len(events))
	}
}

// TestCollectorNoCommitWhenCancelled 取消后不提交，下一轮可重新拉取。
func TestCollectorNoCommitWhenCancelled(t *testing.T) {
	msg := Message{ID: "1", PeerID: "2", Text: "hi", At: time.Now()}
	f := &fakeFetcher{batches: [][]Message{{msg}, {msg}}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	// 第一轮用已取消上下文 + 无缓冲通道：消息不会入总线，也不该记已见。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.poll(ctx, make(chan event.Event), nil); err != nil {
		t.Fatal(err)
	}
	// 第二轮正常发送：说明上轮没有提前 markSeen。
	events := make(chan event.Event, 1)
	if err := c.poll(context.Background(), events, nil); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("取消未送出的消息应重试，实际事件 %d", len(events))
	}
}

// TestCollectorCommitFailure 水位落盘失败按非鉴权错误上抛，调度继续。
func TestCollectorCommitFailure(t *testing.T) {
	f := &commitFetcher{batch: Batch{Commit: func() error { return errors.New("disk full") }}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	err := c.poll(context.Background(), make(chan event.Event, 1), nil)
	if err == nil || errors.Is(err, errAuth) {
		t.Fatalf("应返回非鉴权错误，实际 %v", err)
	}
}

// onePollCollector 只执行一轮 poll，便于在 RunToBus 下精确断言水位时序。
type onePollCollector struct{ *Collector }

func (c onePollCollector) Start(ctx context.Context, events chan<- event.Event, errs chan<- error) error {
	return c.poll(ctx, events, nil)
}

func (c onePollCollector) StartAck(ctx context.Context, events chan<- event.Event, acks <-chan collector.DeliveryAck, errs chan<- error) error {
	return c.poll(ctx, events, acks)
}

// TestCollectorWaitsForPersistentAckBeforeCommit channel 接收后必须等桥回执才提交水位。
func TestCollectorWaitsForPersistentAckBeforeCommit(t *testing.T) {
	var committed atomic.Int32
	f := &commitFetcher{batch: Batch{
		Messages: []Message{{ID: "1", PeerID: "2", Text: "hi", At: time.Now()}},
		Commit:   func() error { committed.Add(1); return nil },
	}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	events := make(chan event.Event)
	acks := make(chan collector.DeliveryAck)
	done := make(chan error, 1)
	go func() { done <- c.poll(context.Background(), events, acks) }()

	ev := <-events
	select {
	case <-time.After(100 * time.Millisecond):
	case err := <-done:
		t.Fatalf("未收到回执 poll 不应结束：%v", err)
	}
	if committed.Load() != 0 {
		t.Fatal("channel 接收后未获回执就提交了水位")
	}

	acks <- collector.DeliveryAck{EventID: ev.ID}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if committed.Load() != 1 {
		t.Fatalf("确认成功后应恰好提交一次，实际 %d", committed.Load())
	}
}

// TestCollectorNoCommitWhenAckFails 回执失败 → poll 报错、水位不前进、消息可重拉。
func TestCollectorNoCommitWhenAckFails(t *testing.T) {
	var committed atomic.Int32
	f := &commitFetcher{batch: Batch{
		Messages: []Message{{ID: "1", PeerID: "2", Text: "hi", At: time.Now()}},
		Commit:   func() error { committed.Add(1); return nil },
	}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	events := make(chan event.Event)
	acks := make(chan collector.DeliveryAck)
	done := make(chan error, 1)
	go func() { done <- c.poll(context.Background(), events, acks) }()

	ev := <-events
	acks <- collector.DeliveryAck{EventID: ev.ID, Err: errors.New("pg down")}
	err := <-done
	if err == nil || errors.Is(err, errAuth) {
		t.Fatalf("回执失败应返回非鉴权错误，实际 %v", err)
	}
	if committed.Load() != 0 {
		t.Fatal("持久化失败后水位不得前进")
	}
	// 未记已见：下一轮应重新产出同一条消息。
	retry := make(chan event.Event, 1)
	if err := c.poll(context.Background(), retry, nil); err != nil {
		t.Fatal(err)
	}
	if len(retry) != 1 {
		t.Fatalf("未落库的消息应重试，实际事件 %d", len(retry))
	}
}

// blockingFailPublisher 模拟桥已接收、PG 写入卡住后失败。
type blockingFailPublisher struct {
	entered chan struct{}
	release chan struct{}
}

func (p *blockingFailPublisher) Publish(ctx context.Context, _ event.Event) (bool, error) {
	close(p.entered)
	select {
	case <-p.release:
		return false, errors.New("fixture: postgres write failed")
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// countingPublisher 记录发布次数并总是成功（等价落库成功）。
type countingPublisher struct{ calls atomic.Int32 }

func (p *countingPublisher) Publish(context.Context, event.Event) (bool, error) {
	p.calls.Add(1)
	return true, nil
}

// TestRunToBusDoesNotCommitWhenPublishFails 验收：假 Publisher 阻塞后失败 → 水位不前进。
func TestRunToBusDoesNotCommitWhenPublishFails(t *testing.T) {
	var committed atomic.Int32
	f := &commitFetcher{batch: Batch{
		Messages: []Message{{ID: "9001", PeerID: "2002", Text: "fixture", At: time.Unix(1700000000, 0)}},
		Commit:   func() error { committed.Add(1); return nil },
	}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	pub := &blockingFailPublisher{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- collector.RunToBus(context.Background(), onePollCollector{c}, pub, time.Second, zap.NewNop())
	}()

	<-pub.entered
	if committed.Load() != 0 {
		t.Fatal("Publish 未返回前水位已前进")
	}
	close(pub.release)
	if err := <-done; err == nil {
		t.Fatal("发布失败应返回错误")
	}
	if committed.Load() != 0 {
		t.Fatal("假 Publisher 阻塞后失败 → 水位不得前进")
	}
}

// TestRunToBusCommitsAfterPublishSucceeds 发布成功路径：每条事件确认后才提交一次水位。
func TestRunToBusCommitsAfterPublishSucceeds(t *testing.T) {
	var committed atomic.Int32
	f := &commitFetcher{batch: Batch{
		Messages: []Message{{ID: "9001", PeerID: "2002", Text: "fixture", At: time.Unix(1700000000, 0)}},
		Commit:   func() error { committed.Add(1); return nil },
	}}
	c, _ := New(f, Options{Interval: 10 * time.Second, Log: zap.NewNop()})
	pub := &countingPublisher{}
	if err := collector.RunToBus(context.Background(), onePollCollector{c}, pub, time.Second, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	if pub.calls.Load() != 1 || committed.Load() != 1 {
		t.Fatalf("应发布并提交各一次，实际 publish=%d commit=%d", pub.calls.Load(), committed.Load())
	}
}
