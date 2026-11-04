package dm

// 本文件测通道适配：Collect 转发到总线、Send 透传、stop 可重复调用。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// fakeCollector 定时产出固定事件。
type fakeCollector struct{ events []event.Event }

func (f *fakeCollector) Name() string { return "fake-dm" }
func (f *fakeCollector) Health() collector.Health {
	return collector.Health{OK: true}
}
func (f *fakeCollector) Start(ctx context.Context, out chan<- event.Event, _ chan<- error) error {
	for _, ev := range f.events {
		select {
		case out <- ev:
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

type fakeSender struct {
	calls int
	got   string
}

func (f *fakeSender) Name() string { return "fake" }
func (f *fakeSender) Send(_ context.Context, _ event.Claim, r sender.Reply) (sender.Result, error) {
	f.calls++
	f.got = r.Text
	return sender.Result{OK: true}, nil
}
func (f *fakeSender) Resume(context.Context, event.Claim) (sender.Result, error) {
	return sender.Result{}, errors.New("no resume")
}

// fakeBus 的 got 由采集 goroutine 写、测试线程读，必须加锁（race 测试要求）。
type fakeBus struct {
	mu  sync.Mutex
	got []event.Event
}

func (f *fakeBus) Publish(_ context.Context, ev event.Event) (bool, error) {
	f.mu.Lock()
	f.got = append(f.got, ev)
	f.mu.Unlock()
	return true, nil
}

// events 返回 got 的快照，供测试线程安全读取。
func (f *fakeBus) events() []event.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]event.Event(nil), f.got...)
}
func (f *fakeBus) ClaimPending(context.Context, int) ([]event.Claim, error) { return nil, nil }
func (f *fakeBus) Mark(context.Context, event.Claim, []event.Status, event.Status) error {
	return nil
}
func (f *fakeBus) Defer(context.Context, event.Claim, time.Time) error { return nil }
func (f *fakeBus) Renew(context.Context, event.Claim) (event.Claim, error) {
	return event.Claim{}, nil
}
func (f *fakeBus) Recover(context.Context, int) (int, error) { return 0, nil }
func (f *fakeBus) Notify() <-chan struct{}                   { return nil }
func (f *fakeBus) Close() error                              { return nil }

// TestChannelCollectForwards 采集事件被转发到总线；stop 幂等。
func TestChannelCollectForwards(t *testing.T) {
	col := &fakeCollector{events: []event.Event{{
		ID: "dm:2002:1", Channel: event.ChDM, Dir: event.DirIn, Session: "2002",
		FromID: "2002", FromName: "乙", Text: "在吗", At: time.Now(), Status: event.StatusPending,
	}}}
	bus := &fakeBus{}
	ch, err := New(col, nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if ch.Name() != event.ChDM {
		t.Errorf("通道名 = %s", ch.Name())
	}

	stop, err := ch.Collect(context.Background(), bus)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(bus.events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := bus.events()
	if len(got) != 1 || got[0].ID != "dm:2002:1" {
		t.Fatalf("未转发事件：%+v", got)
	}
	stop()
	stop() // 重复调用不应 panic
}

// TestChannelSendPassthrough 发送透传；未装配 sender 时报错。
func TestChannelSendPassthrough(t *testing.T) {
	col := &fakeCollector{}
	snd := &fakeSender{}
	ch, _ := New(col, snd, time.Second, zap.NewNop())

	claim := event.Claim{Event: event.Event{ID: "dm:2002:1", Channel: event.ChDM, Session: "2002"}}
	res, err := ch.Send(context.Background(), claim, sender.Reply{Text: "回复"})
	if err != nil || !res.OK || snd.calls != 1 || snd.got != "回复" {
		t.Fatalf("透传失败：%+v / %v", res, err)
	}

	noSender, _ := New(col, nil, time.Second, zap.NewNop())
	if _, err := noSender.Send(context.Background(), claim, sender.Reply{Text: "x"}); err == nil {
		t.Error("未装配发送器应报错")
	}
}

// TestNewValidates 构造期校验。
func TestNewValidates(t *testing.T) {
	col := &fakeCollector{}
	if _, err := New(nil, nil, time.Second, zap.NewNop()); err == nil {
		t.Error("nil 采集器应报错")
	}
	if _, err := New(col, nil, 0, zap.NewNop()); err == nil {
		t.Error("零超时应报错")
	}
	if _, err := New(col, nil, time.Second, nil); err == nil {
		t.Error("nil 日志应报错")
	}
}
