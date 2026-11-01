package main

import (
	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"context"
	"errors"
	"go.uber.org/zap"
	"testing"
	"time"
)

type pausedCollector struct {
	started chan struct{}
	stopped chan struct{}
}

func (c *pausedCollector) Name() string             { return "paused-fixture" }
func (c *pausedCollector) Health() collector.Health { return collector.Health{} }
func (c *pausedCollector) Start(context.Context, chan<- event.Event, chan<- error) error {
	close(c.started)
	close(c.stopped)
	return errors.New("private-error-canary")
}

type noPublish struct{}

func (noPublish) Publish(context.Context, event.Event) (bool, error) { return false, nil }
func TestLivePausedDoesNotStopInfrastructure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &pausedCollector{started: make(chan struct{}), stopped: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serveWithCollector(ctx, c, noPublish{}, time.Second, zap.NewNop(), func(ctx context.Context) error { <-ctx.Done(); return nil })
	}()
	select {
	case <-c.started:
	case <-time.After(time.Second):
		t.Fatal("采集没启动")
	}
	select {
	case err := <-done:
		t.Fatal("单通道暂停结束了基础设施", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("服务取消未退出")
	}
}
func TestBuildLiveDisabledDoesNotStartNode(t *testing.T) {
	var cfg config.Config
	c, err := buildLive(cfg, zap.NewNop())
	if err != nil || c != nil {
		t.Fatal("关闭还装配采集")
	}
}
