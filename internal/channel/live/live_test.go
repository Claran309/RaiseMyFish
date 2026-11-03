package live

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/channel"
	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

type fakeCollector struct {
	event   event.Event
	err     error
	started chan struct{}
	stopped chan struct{}
}

func newFakeCollector(ev event.Event) *fakeCollector {
	return &fakeCollector{event: ev, started: make(chan struct{}), stopped: make(chan struct{})}
}

func (c *fakeCollector) Name() string             { return "fake-live" }
func (c *fakeCollector) Health() collector.Health { return collector.Health{} }

func (c *fakeCollector) Start(ctx context.Context, out chan<- event.Event, _ chan<- error) error {
	close(c.started)
	defer close(c.stopped)
	if c.err != nil {
		return c.err
	}
	select {
	case out <- c.event:
	case <-ctx.Done():
		return nil
	}
	<-ctx.Done()
	return nil
}

type fakeBus struct {
	published chan event.Event
	notify    chan struct{}
}

func newFakeBus() *fakeBus {
	return &fakeBus{published: make(chan event.Event, 1), notify: make(chan struct{})}
}

func (b *fakeBus) Publish(ctx context.Context, ev event.Event) (bool, error) {
	select {
	case b.published <- ev:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
func (b *fakeBus) ClaimPending(context.Context, int) ([]event.Claim, error) { return nil, nil }
func (b *fakeBus) Mark(context.Context, event.Claim, []event.Status, event.Status) error {
	return nil
}
func (b *fakeBus) Defer(context.Context, event.Claim, time.Time) error { return nil }
func (b *fakeBus) Renew(context.Context, event.Claim) (event.Claim, error) {
	return event.Claim{}, nil
}
func (b *fakeBus) Recover(context.Context, int) (int, error) { return 0, nil }
func (b *fakeBus) Notify() <-chan struct{}                   { return b.notify }

type fakeSender struct {
	result sender.Result
	err    error
	claim  event.Claim
	reply  sender.Reply
}

func (s *fakeSender) Name() string { return "fake-live" }
func (s *fakeSender) Send(_ context.Context, c event.Claim, r sender.Reply) (sender.Result, error) {
	s.claim, s.reply = c, r
	return s.result, s.err
}
func (s *fakeSender) Resume(context.Context, event.Claim) (sender.Result, error) {
	return sender.Result{}, nil
}

func TestCollectForwardsToBusAndStops(t *testing.T) {
	ev := event.Event{ID: "evt-1", Channel: event.ChLive, Session: "30308220", Dir: event.DirIn, Text: "测试弹幕"}
	fc := newFakeCollector(ev)
	bus := newFakeBus()
	ch, err := New(fc, nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	stop, err := ch.Collect(context.Background(), bus)
	if err != nil {
		t.Fatalf("启动采集失败：%v", err)
	}
	select {
	case got := <-bus.published:
		if got.ID != ev.ID || got.Channel != event.ChLive {
			t.Fatalf("转发事件不符：%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("事件未写入总线")
	}

	stop()
	select {
	case <-fc.stopped:
	case <-time.After(time.Second):
		t.Fatal("采集器未停止")
	}
	stop() // 可重复调用，不阻塞、不 panic
}

func TestCollectPausedDoesNotStopService(t *testing.T) {
	fc := newFakeCollector(event.Event{})
	fc.err = errors.New("private-error-canary")
	ch, err := New(fc, nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	stop, err := ch.Collect(context.Background(), newFakeBus())
	if err != nil {
		t.Fatalf("采集暂停不应阻止服务启动：%v", err)
	}
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop 未返回")
	}
}

func TestCollectRejectsInvalidArgs(t *testing.T) {
	ch, err := New(newFakeCollector(event.Event{}), nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Collect(nil, newFakeBus()); err == nil {
		t.Fatal("nil 上下文应被拒绝")
	}
	if _, err := ch.Collect(context.Background(), nil); err == nil {
		t.Fatal("nil 总线应被拒绝")
	}
}

func TestSendForwardsToSender(t *testing.T) {
	want := sender.Result{OK: true, MsgID: "m1", Verified: true}
	fs := &fakeSender{result: want}
	ch, err := New(newFakeCollector(event.Event{}), fs, time.Second, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	claim := event.Claim{Event: event.Event{ID: "evt-1"}, Lease: event.Lease{Owner: "owner", Version: 3}}
	reply := channel.Reply{Text: "你好"}

	got, err := ch.Send(context.Background(), claim, reply)
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if got != want {
		t.Fatalf("返回结果不符：%+v", got)
	}
	if fs.claim.Lease != claim.Lease || fs.claim.Event.ID != claim.Event.ID || fs.reply.Text != reply.Text {
		t.Fatalf("未按原样转发：claim=%+v reply=%+v", fs.claim, fs.reply)
	}
}

func TestSendPropagatesError(t *testing.T) {
	want := &sender.Error{Kind: sender.KindRetry, Settled: true}
	ch, err := New(newFakeCollector(event.Event{}), &fakeSender{err: want}, time.Second, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Send(context.Background(), event.Claim{}, channel.Reply{}); !errors.Is(err, want) {
		t.Fatalf("错误未原样透传：%v", err)
	}
}

func TestSendWithoutSender(t *testing.T) {
	ch, err := New(newFakeCollector(event.Event{}), nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Send(context.Background(), event.Claim{}, channel.Reply{}); err == nil {
		t.Fatal("未装配发送器时应明确报错")
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(nil, nil, time.Second, zap.NewNop()); err == nil {
		t.Fatal("nil 采集器应被拒绝")
	}
	if _, err := New(newFakeCollector(event.Event{}), nil, 0, zap.NewNop()); err == nil {
		t.Fatal("非正超时应被拒绝")
	}
	if _, err := New(newFakeCollector(event.Event{}), nil, time.Second, nil); err == nil {
		t.Fatal("nil 日志器应被拒绝")
	}
	if _, err := New(newFakeCollector(event.Event{}), nil, time.Second, zap.NewNop()); err != nil {
		t.Fatalf("只采集模式应允许 nil 发送器：%v", err)
	}
}
