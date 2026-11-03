package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/knowledge"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// scriptedMemeWorker 让循环经历成功、失败、成功，验证单轮失败后继续。
type scriptedMemeWorker struct {
	mu    sync.Mutex
	calls int
	seen  chan int
}

func (w *scriptedMemeWorker) RunOnce(context.Context) (knowledge.MemeLearnStats, error) {
	w.mu.Lock()
	w.calls++
	n := w.calls
	w.mu.Unlock()
	select {
	case w.seen <- n:
	default:
	}
	if n == 2 {
		return knowledge.MemeLearnStats{}, errors.New("模拟梗白学习失败")
	}
	return knowledge.MemeLearnStats{Scanned: 3, Candidates: 2, Learned: 1}, nil
}

func TestRunMemeLearnLogsAndContinuesAfterError(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	worker := &scriptedMemeWorker{seen: make(chan int, 8)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMemeLearn(ctx, worker, 5*time.Millisecond, zap.New(core))
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-worker.seen:
		case <-time.After(2 * time.Second):
			cancel()
			<-done
			t.Fatalf("循环只执行了 %d 轮", i)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后梗白学习循环未退出")
	}

	var sawSuccess, sawFailure bool
	for _, entry := range logs.All() {
		fields := entry.ContextMap()
		if fields["action"] != memeLearnAction {
			continue
		}
		switch entry.Message {
		case "梗白学习完成":
			if fields["learned"] == int64(1) {
				sawSuccess = true
			}
		case "梗白学习失败，等待下一轮":
			sawFailure = true
		}
	}
	if !sawSuccess || !sawFailure {
		t.Fatalf("日志缺少成功或失败记录：success=%v failure=%v", sawSuccess, sawFailure)
	}
}

func TestStartMemeLearnDisabled(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startMemeLearn(context.Background(), config.Config{Knowledge: config.Knowledge{MemeLearn: false}}, nil, knowledgeAssembly{}, zap.New(core))
	stop()
	if logs.Len() != 0 {
		t.Fatalf("关闭时不应启动或记日志：%v", logs.All())
	}
}

func TestStartMemeLearnWithoutLLM(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := config.Config{Knowledge: config.Knowledge{Enabled: true, MemeLearn: true, MemeLearnMaxPerDay: 10}, LLM: config.LLM{Enabled: false}}
	stop := startMemeLearn(context.Background(), cfg, nil, knowledgeAssembly{}, zap.New(core))
	stop()
	if logs.FilterMessage("梗白学习已配置但 LLM 未启用，跳过").Len() == 0 {
		t.Fatalf("LLM 未启用未记跳过日志：%v", logs.All())
	}
}

func TestStartMemeLearnWithoutReader(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := config.Config{
		Knowledge: config.Knowledge{Enabled: true, MemeLearn: true, MemeLearnMaxPerDay: 10},
		LLM:       config.LLM{Enabled: true, Routing: map[string]string{"default": "p:m"}},
	}
	stop := startMemeLearn(context.Background(), cfg, nil, knowledgeAssembly{}, zap.New(core))
	stop()
	if logs.FilterMessage("梗白学习已配置但知识Reader未装配，跳过").Len() == 0 {
		t.Fatalf("Reader 缺失未记跳过日志：%v", logs.All())
	}
}

func TestBackgroundRoute(t *testing.T) {
	if got := backgroundRoute(config.Config{LLM: config.LLM{Routing: map[string]string{"live": "p:m", "default": "p:d"}}}); got != "default" {
		t.Fatalf("优先 default 失败：%q", got)
	}
	if got := backgroundRoute(config.Config{LLM: config.LLM{Routing: map[string]string{"live": "p:l", "brain": "p:b"}}}); got != "brain" {
		t.Fatalf("fallback 未按名称排序：%q", got)
	}
	if got := backgroundRoute(config.Config{}); got != "" {
		t.Fatalf("空路由应返回空：%q", got)
	}
}
