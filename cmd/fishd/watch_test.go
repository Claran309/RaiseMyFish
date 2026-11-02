package main

// 本文件测 0.14 的 watch 接线：定时触发、失败不阻塞、关闭不启动。
// 全部用 fake clock/流水线，不录制、不调模型、不访问平台。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// fakeWatchClock 是可手动推进的时钟，避免测试依赖真实 sleep。
type fakeWatchClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeWatchClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeWatchClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// scriptedWatchRunner 记录调用次数，并模拟每次运行的耗时与结果。
type scriptedWatchRunner struct {
	clock   *fakeWatchClock
	latency time.Duration
	results []watch.Result
	errs    []error

	mu    sync.Mutex
	calls int
	seen  chan int
}

func (r *scriptedWatchRunner) Run(context.Context) (watch.Result, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	r.clock.Advance(r.latency)
	var result watch.Result
	var err error
	if n-1 < len(r.results) {
		result = r.results[n-1]
	}
	if n-1 < len(r.errs) {
		err = r.errs[n-1]
	}
	select {
	case r.seen <- n:
	default:
	}
	return result, err
}

func (r *scriptedWatchRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func waitTick(t *testing.T, seen <-chan int, want int) {
	t.Helper()
	select {
	case got := <-seen:
		if got != want {
			t.Fatalf("第 %d 次触发，期望第 %d 次", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("等待第 %d 次触发超时", want)
	}
}

// TestRunWatchFakeClockTriggersPerTick 每个 tick 触发一次，延迟按注入时钟计算。
func TestRunWatchFakeClockTriggersPerTick(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	clock := &fakeWatchClock{now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	runner := &scriptedWatchRunner{
		clock:   clock,
		latency: 1500 * time.Millisecond,
		results: []watch.Result{
			{Description: "主播在打游戏", Transcript: "欢迎", Warnings: []string{"画面较暗"}},
			{Description: "主播在打游戏", Transcript: "欢迎", Warnings: []string{"画面较暗"}},
		},
		seen: make(chan int, 4),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ticks := make(chan time.Time)
	go func() {
		defer close(done)
		runWatch(ctx, runner, ticks, clock.Now, zap.New(core))
	}()

	ticks <- clock.Now()
	waitTick(t, runner.seen, 1)
	ticks <- clock.Now()
	waitTick(t, runner.seen, 2)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 watch 循环未退出")
	}

	if got := runner.callCount(); got != 2 {
		t.Fatalf("触发次数 = %d，期望 2", got)
	}
	entries := logs.FilterField(zap.String("action", "watch")).All()
	if len(entries) != 2 {
		t.Fatalf("watch 日志条数 = %d，期望 2：%v", len(entries), logs.All())
	}
	for _, entry := range entries {
		fields := entry.ContextMap()
		if fields["latency_ms"] != int64(1500) {
			t.Errorf("latency_ms = %v，期望 1500", fields["latency_ms"])
		}
		if fields["degraded"] != false || fields["has_description"] != true || fields["has_transcript"] != true {
			t.Errorf("结果字段不符：%+v", fields)
		}
	}
}

// TestRunWatchFailureDoesNotStopLoop 单轮失败只记日志，下一轮照常触发。
func TestRunWatchFailureDoesNotStopLoop(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	clock := &fakeWatchClock{now: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	runner := &scriptedWatchRunner{
		clock:   clock,
		latency: time.Second,
		errs:    []error{errors.New("录制失败"), nil},
		results: []watch.Result{{}, {Failures: []watch.Failure{{Stage: "describe", Err: errors.New("模型超时")}}}},
		seen:    make(chan int, 4),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ticks := make(chan time.Time)
	go func() {
		defer close(done)
		runWatch(ctx, runner, ticks, clock.Now, zap.New(core))
	}()

	ticks <- clock.Now()
	waitTick(t, runner.seen, 1)
	ticks <- clock.Now()
	waitTick(t, runner.seen, 2)
	cancel()
	<-done

	if got := runner.callCount(); got != 2 {
		t.Fatalf("失败后未继续触发：%d", got)
	}
	if logs.FilterMessage("看直播失败，等待下一轮").Len() != 1 {
		t.Fatalf("缺少失败日志：%v", logs.All())
	}
	degraded := logs.FilterMessage("看直播完成（有降级）").All()
	if len(degraded) != 1 || degraded[0].ContextMap()["failures"] != int64(1) {
		t.Fatalf("缺少降级日志：%v", logs.All())
	}
}

// TestRunWatchStopsOnCancel 取消后不再消费 tick。
func TestRunWatchStopsOnCancel(t *testing.T) {
	clock := &fakeWatchClock{now: time.Now()}
	runner := &scriptedWatchRunner{clock: clock, seen: make(chan int, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWatch(ctx, runner, make(chan time.Time), clock.Now, zap.NewNop())
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("已取消的上下文未退出")
	}
	if runner.callCount() != 0 {
		t.Fatal("取消后仍执行了看直播")
	}
}

// TestStartWatchDisabledDoesNothing 关闭时既不装配也不写日志。
func TestStartWatchDisabledDoesNothing(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startWatch(context.Background(), config.Config{}, zap.New(core))
	stop()
	if logs.Len() != 0 {
		t.Fatalf("关闭时不应启动：%v", logs.All())
	}
}

// TestStartWatchDegradesOnBuildError 装配失败只降级，不 panic。
func TestStartWatchDegradesOnBuildError(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startWatch(context.Background(), config.Config{
		Watch: config.Watch{Enabled: true, IntervalSeconds: 600},
	}, zap.New(core))
	stop()
	if logs.FilterMessage("看直播装配失败，降级运行").Len() == 1 {
		return
	}
	t.Fatalf("缺少降级日志：%v", logs.All())
}

func watchTestConfig() config.Config {
	return config.Config{
		Watch: config.Watch{
			Enabled:         true,
			Source:          "stream",
			IntervalSeconds: 600,
			Seconds:         10,
			Frames:          4,
			ASRURL:          "http://127.0.0.1:8077/v1/audio/transcriptions",
			StreamQuality:   "origin",
			FFmpegPath:      "ffmpeg",
			FFprobePath:     "ffprobe",
			LLMRoute:        "default",
			Cookie:          "SESSDATA=fixture",
		},
		Collector: config.Collectors{Live: config.LiveCollector{RoomID: "30308220"}},
		LLM: config.LLM{
			Enabled: true,
			Providers: map[string]config.Provider{
				"fake": {Type: "openai", BaseURL: "https://api.example.com/v1", Models: []string{"vision"}},
			},
			Routing: map[string]string{"default": "fake:vision"},
		},
	}
}

// TestBuildWatchPipeline 配置能构造出完整流水线（不联网、不执行 ffmpeg）。
func TestBuildWatchPipeline(t *testing.T) {
	pipeline, err := buildWatchPipeline(watchTestConfig(), zap.NewNop())
	if err != nil || pipeline == nil {
		t.Fatalf("装配失败：%v", err)
	}
}

// TestStartWatchStartsAndStops 启用配置能启动定时器，stop 后退出。
func TestStartWatchStartsAndStops(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startWatch(context.Background(), watchTestConfig(), zap.New(core))
	if logs.FilterMessage("看直播已启动").Len() != 1 {
		t.Fatalf("缺少启动日志：%v", logs.All())
	}
	stop()
}
