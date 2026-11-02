package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/proactive"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type fakeProactiveRunner struct {
	mu    sync.Mutex
	calls int
	errs  []error
}

func (f *fakeProactiveRunner) RunOnce(context.Context) (proactive.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.calls
	f.calls++
	if index < len(f.errs) && f.errs[index] != nil {
		return proactive.Result{}, f.errs[index]
	}
	return proactive.Result{Sent: true}, nil
}

func TestRunProactiveContinuesAfterFailure(t *testing.T) {
	runner := &fakeProactiveRunner{errs: []error{errors.New("one bad round"), nil}}
	ticks := make(chan time.Time, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	core, logs := observer.New(zap.DebugLevel)
	go func() {
		defer close(done)
		runProactive(ctx, runner, ticks, time.Now, zap.New(core))
	}()
	ticks <- time.Now()
	ticks <- time.Now()
	deadline := time.Now().Add(2 * time.Second)
	for {
		runner.mu.Lock()
		calls := runner.calls
		runner.mu.Unlock()
		if calls == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("定时循环未处理第二轮，calls=%d", calls)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后定时循环未退出")
	}
	if logs.FilterMessage("主动弹幕失败，等待下一轮").Len() != 1 {
		t.Fatal("失败轮次未记录日志")
	}
}

func TestRunProactiveStopsWithoutTicks(t *testing.T) {
	runner := &fakeProactiveRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runProactive(ctx, runner, make(chan time.Time), time.Now, zap.NewNop())
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后定时循环未退出")
	}
	if runner.calls != 0 {
		t.Fatal("没有 tick 不应执行")
	}
}

func TestStartProactiveDisabledOrDegraded(t *testing.T) {
	ctx := context.Background()
	stop := startProactive(ctx, config.Config{}, nil, nil, persona.Pack{}, zap.NewNop())
	stop()

	cfg := config.Config{Proactive: config.Proactive{Enabled: true, IntervalSeconds: 5, SendGapSeconds: 30}}
	core, logs := observer.New(zap.DebugLevel)
	stop = startProactive(ctx, cfg, nil, nil, persona.Pack{}, zap.New(core))
	stop()
	if logs.FilterMessage("主动弹幕装配失败，降级运行").Len() != 1 {
		t.Fatal("装配失败未降级")
	}
}
