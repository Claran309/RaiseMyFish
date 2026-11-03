package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type fakeHeartbeatLiker struct {
	mu        sync.Mutex
	calls     int
	rooms     []string
	failFirst bool
	seen      chan int
}

func (f *fakeHeartbeatLiker) Like(_ context.Context, room string, count int) error {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.rooms = append(f.rooms, room)
	fail := f.failFirst && n == 1
	f.mu.Unlock()
	select {
	case f.seen <- n:
	default:
	}
	if fail {
		return errors.New("模拟风控")
	}
	return nil
}

func (f *fakeHeartbeatLiker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func heartbeatConfig() config.Config {
	return config.Config{
		Presence: config.Presence{HeartbeatLikeSeconds: 30},
		Channels: config.Channels{Live: config.Channel{Enabled: true}},
		Collector: config.Collectors{Live: config.LiveCollector{
			RoomID: "30308220",
			Cookie: "SESSDATA=secret-sess; bili_jct=csrf-token",
		}},
	}
}

func wantHeartbeatCall(t *testing.T, seen <-chan int, want int) {
	t.Helper()
	select {
	case got := <-seen:
		if got != want {
			t.Fatalf("第 %d 次调用，期望第 %d 次", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("等待第 %d 次点赞超时", want)
	}
}

// TestRunHeartbeatLikeCallsPerTickAndContinuesAfterError 每 tick 只调一次；失败后继续。
func TestRunHeartbeatLikeCallsPerTickAndContinuesAfterError(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	liker := &fakeHeartbeatLiker{failFirst: true, seen: make(chan int, 4)}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeatLike(ctx, liker, "30308220", 30*time.Second, ticks, zap.New(core))
	}()

	ticks <- time.Now()
	wantHeartbeatCall(t, liker.seen, 1)
	ticks <- time.Now()
	wantHeartbeatCall(t, liker.seen, 2)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后常驻点赞循环未退出")
	}
	if got := liker.count(); got != 2 {
		t.Fatalf("调用次数 = %d，期望每个 tick 恰好一次（共2次）", got)
	}
	liker.mu.Lock()
	rooms := append([]string(nil), liker.rooms...)
	liker.mu.Unlock()
	for i, room := range rooms {
		if room != "30308220" {
			t.Fatalf("第 %d 次点赞房间 = %q", i+1, room)
		}
	}

	var sawFailure, sawSuccess bool
	for _, entry := range logs.All() {
		fields := entry.ContextMap()
		if fields["action"] != heartbeatLikeAction || fields["interval_seconds"] != int64(30) {
			continue
		}
		switch entry.Message {
		case "常驻点赞失败，等待下一轮":
			sawFailure = true
		case "常驻点赞成功":
			sawSuccess = true
		}
	}
	if !sawFailure || !sawSuccess {
		t.Fatalf("缺少成功或失败日志：failure=%v success=%v", sawFailure, sawSuccess)
	}
}

func TestStartHeartbeatLikeDisabled(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startHeartbeatLike(context.Background(), config.Config{}, zap.New(core))
	stop()
	if logs.Len() != 0 {
		t.Fatalf("关闭时不应启动或记日志：%v", logs.All())
	}
}

func TestStartHeartbeatLikeLiveDisabled(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := heartbeatConfig()
	cfg.Channels.Live.Enabled = false
	stop := startHeartbeatLike(context.Background(), cfg, zap.New(core))
	stop()
	entries := logs.FilterMessage("常驻点赞未启动：直播采集未启用")
	if entries.Len() != 1 || entries.All()[0].ContextMap()["reason"] != "live_disabled" {
		t.Fatalf("直播未启用未正确降级：%v", logs.All())
	}
}

func TestStartHeartbeatLikeWithoutCookie(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := heartbeatConfig()
	cfg.Collector.Live.Cookie = ""
	stop := startHeartbeatLike(context.Background(), cfg, zap.New(core))
	stop()
	entries := logs.FilterMessage("常驻点赞未启动：未配置凭据")
	if entries.Len() != 1 || entries.All()[0].ContextMap()["reason"] != "no_cookie" {
		t.Fatalf("无凭据未正确降级：%v", logs.All())
	}
}

func TestStartHeartbeatLikeWithoutCSRF(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := heartbeatConfig()
	cfg.Collector.Live.Cookie = "SESSDATA=only"
	stop := startHeartbeatLike(context.Background(), cfg, zap.New(core))
	stop()
	entries := logs.FilterMessage("常驻点赞未启动：凭据缺少 bili_jct")
	if entries.Len() != 1 || entries.All()[0].ContextMap()["reason"] != "no_csrf" {
		t.Fatalf("缺 CSRF 未正确降级：%v", logs.All())
	}
}

func TestStartHeartbeatLikeStartsAndStops(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startHeartbeatLike(context.Background(), heartbeatConfig(), zap.New(core))
	stop()
	entries := logs.FilterMessage("常驻点赞已启动")
	if entries.Len() != 1 || entries.All()[0].ContextMap()["interval_seconds"] != int64(30) {
		t.Fatalf("常驻点赞未按预期启动：%v", logs.All())
	}
}
