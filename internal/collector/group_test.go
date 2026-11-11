package collector

// 本文件测组合采集器：回执转给产出事件的子采集器、致命错误上传、健康聚合。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

// groupAckChild 实现 AckCollector：发一条事件后必须等到回执才继续。
type groupAckChild struct {
	id     string
	acked  chan DeliveryAck
	health Health
}

func (c *groupAckChild) Name() string   { return c.id }
func (c *groupAckChild) Health() Health { return c.health }

func (c *groupAckChild) Start(ctx context.Context, out chan<- event.Event, errc chan<- error) error {
	return c.StartAck(ctx, out, nil, errc)
}

func (c *groupAckChild) StartAck(ctx context.Context, out chan<- event.Event, acks <-chan DeliveryAck, errc chan<- error) error {
	select {
	case out <- event.Event{ID: c.id + ":1"}:
	case <-ctx.Done():
		return nil
	}
	if acks != nil {
		select {
		case ack := <-acks:
			select {
			case c.acked <- ack:
			default:
			}
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

// groupPlainChild 不实现 AckCollector；start 关闭后才发事件，便于固定先后顺序。
type groupPlainChild struct {
	id      string
	start   <-chan struct{}
	stopped chan struct{}
	health  Health
}

func (c *groupPlainChild) Name() string   { return c.id }
func (c *groupPlainChild) Health() Health { return c.health }

func (c *groupPlainChild) Start(ctx context.Context, out chan<- event.Event, errc chan<- error) error {
	defer close(c.stopped)
	select {
	case <-c.start:
	case <-ctx.Done():
		return nil
	}
	select {
	case out <- event.Event{ID: c.id + ":1"}:
	case <-ctx.Done():
		return nil
	}
	<-ctx.Done()
	return nil
}

// groupFailChild 立即以错误退出，模拟鉴权失效等不可自愈故障。
type groupFailChild struct{ err error }

func (c *groupFailChild) Name() string   { return "fail" }
func (c *groupFailChild) Health() Health { return Health{} }
func (c *groupFailChild) Start(context.Context, chan<- event.Event, chan<- error) error {
	return c.err
}

// TestGroupRoutesAckToProducer 回执必须转给真正产出事件的子采集器（消息监听水位依赖它）。
func TestGroupRoutesAckToProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := make(chan struct{})
	ack := &groupAckChild{id: "feed", acked: make(chan DeliveryAck, 1)}
	plain := &groupPlainChild{id: "dynamic", start: start, stopped: make(chan struct{})}
	group, err := NewGroup(ack, plain)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{unique: map[string]bool{}, cancel: cancel}
	done := make(chan error, 1)
	go func() { done <- RunToBus(ctx, group, publisher, time.Second, zap.NewNop()) }()
	select {
	case got := <-ack.acked:
		if got.EventID != "feed:1" || got.Err != nil {
			t.Fatalf("回执转错或带错误：%+v", got)
		}
		close(start)
	case <-time.After(time.Second):
		t.Fatal("消息监听没有收到落库回执")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("组合采集失败：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("组合采集未在取消后退出")
	}
	if publisher.calls != 2 || len(publisher.unique) != 2 {
		t.Fatalf("两个来源的事件未各发一次：%d %v", publisher.calls, publisher.unique)
	}
	select {
	case <-plain.stopped:
	default:
		t.Fatal("取消后普通来源未退出")
	}
}

// TestGroupPropagatesFatalChild 子采集器致命退出要上传给桥（而不是静默当成功）。
func TestGroupPropagatesFatalChild(t *testing.T) {
	sentinel := errors.New("child-failed")
	start := make(chan struct{})
	plain := &groupPlainChild{id: "dynamic", start: start, stopped: make(chan struct{})}
	group, err := NewGroup(&groupFailChild{err: sentinel}, plain)
	if err != nil {
		t.Fatal(err)
	}
	err = RunToBus(context.Background(), group, &fakePublisher{unique: map[string]bool{}}, time.Second, zap.NewNop())
	if !errors.Is(err, sentinel) {
		t.Fatalf("致命错误未上传：%v", err)
	}
	select {
	case <-plain.stopped:
	case <-time.After(time.Second):
		t.Fatal("一个来源退出后其他来源未停止")
	}
}

// TestGroupHealthAggregates 任一子来源不健康即整体不健康，事件时间取最近。
func TestGroupHealthAggregates(t *testing.T) {
	now := time.Now()
	a := &groupAckChild{id: "a", health: Health{OK: true, LastEventAt: now.Add(-time.Minute), Reconnects: 1}}
	b := &groupAckChild{id: "b", health: Health{OK: false, LastEventAt: now, LastError: "down", Reconnects: 2}}
	group, err := NewGroup(a, b)
	if err != nil {
		t.Fatal(err)
	}
	health := group.Health()
	if health.OK || health.LastError != "down" || health.Reconnects != 3 || !health.LastEventAt.Equal(now) {
		t.Fatalf("健康聚合不符：%+v", health)
	}
}

func TestNewGroupRejectsEmpty(t *testing.T) {
	if _, err := NewGroup(); err == nil {
		t.Fatal("空来源应拒绝")
	}
	if _, err := NewGroup(nil); err == nil {
		t.Fatal("nil 来源应拒绝")
	}
}
