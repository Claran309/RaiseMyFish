package collector

import (
	"RaiseMyFish/internal/event"
	"context"
	"errors"
	"go.uber.org/zap"
	"strings"
	"testing"
	"time"
)

type fakeCollector struct {
	stopped    chan struct{}
	panicStart bool
	cleanupErr error
}

func (f *fakeCollector) Name() string   { return "fixture" }
func (f *fakeCollector) Health() Health { return Health{} }
func (f *fakeCollector) Start(ctx context.Context, out chan<- event.Event, errc chan<- error) error {
	defer close(f.stopped)
	if f.panicStart {
		panic("private-canary")
	}
	for range 2 {
		select {
		case out <- event.Event{ID: "live:1:same"}:
		case <-ctx.Done():
			return f.cleanupErr
		}
	}
	<-ctx.Done()
	return f.cleanupErr
}

type fakePublisher struct {
	calls  int
	unique map[string]bool
	err    error
	cancel context.CancelFunc
}

func (f *fakePublisher) Publish(_ context.Context, e event.Event) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	newID := !f.unique[e.ID]
	f.unique[e.ID] = true
	if f.calls == 2 {
		f.cancel()
	}
	return newID, nil
}

func TestRunToBusDuplicatesAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &fakeCollector{stopped: make(chan struct{})}
	p := &fakePublisher{unique: map[string]bool{}, cancel: cancel}
	if err := RunToBus(ctx, c, p, time.Second, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 || len(p.unique) != 1 {
		t.Fatal("重复没有交给Bus幂等处理")
	}
	select {
	case <-c.stopped:
	default:
		t.Fatal("未等待采集退出")
	}
}
func TestRunToBusFailureStopsProducerWithoutLeak(t *testing.T) {
	sentinel := errors.New("credential-canary")
	c := &fakeCollector{stopped: make(chan struct{})}
	p := &fakePublisher{err: sentinel}
	err := RunToBus(context.Background(), c, p, time.Second, zap.NewNop())
	if !errors.Is(err, sentinel) || strings.Contains(err.Error(), "canary") {
		t.Fatal("错误链或脱敏错误", err)
	}
	select {
	case <-c.stopped:
	default:
		t.Fatal("发布失败未停采集")
	}
}
func TestRunToBusPanicIsSafe(t *testing.T) {
	c := &fakeCollector{stopped: make(chan struct{}), panicStart: true}
	p := &fakePublisher{}
	err := RunToBus(context.Background(), c, p, time.Second, zap.NewNop())
	if err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("panic未安全报告")
	}
}

func TestPublishAndCollectorShutdownErrorsBothRemain(t *testing.T) {
	publishErr := errors.New("publish-failed")
	cleanupErr := errors.New("shutdown-failed")
	c := &fakeCollector{stopped: make(chan struct{}), cleanupErr: cleanupErr}
	p := &fakePublisher{err: publishErr}
	err := RunToBus(context.Background(), c, p, time.Second, zap.NewNop())
	if !errors.Is(err, publishErr) || !errors.Is(err, cleanupErr) {
		t.Fatal("持久化失败收尾丢失错误链", err)
	}
}
