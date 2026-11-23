package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/conversationsummary"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// scriptedSummaryRunner 让循环依次经历成功、失败、成功。
type scriptedSummaryRunner struct {
	mu    sync.Mutex
	calls int
	seen  chan int
}

func (r *scriptedSummaryRunner) RunOnce(context.Context) (conversationsummary.Stats, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	select {
	case r.seen <- n:
	default:
	}
	if n == 2 {
		return conversationsummary.Stats{}, errors.New("模拟摘要失败")
	}
	return conversationsummary.Stats{Enqueued: 1, Claimed: 1, Summarized: 1}, nil
}

// TestRunConversationSummaryContinuesAfterError 单轮失败只记日志，下一轮继续。
func TestRunConversationSummaryContinuesAfterError(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	runner := &scriptedSummaryRunner{seen: make(chan int, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runConversationSummary(ctx, runner, 10*time.Millisecond, zap.New(core))
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-runner.seen:
		case <-time.After(2 * time.Second):
			t.Fatal("摘要循环未按预期运行")
		}
	}
	cancel()
	<-done
	var errors, oks int
	for _, entry := range logs.All() {
		if entry.ContextMap()["action"] != "conversation_summary" {
			continue
		}
		switch entry.Level {
		case zapcore.ErrorLevel:
			errors++
		case zapcore.InfoLevel:
			oks++
		}
	}
	if errors != 1 || oks != 2 {
		t.Fatalf("日志不符：错误=%d 成功=%d", errors, oks)
	}
}

// TestStartConversationSummaryDisabled 关闭时不装配、不启动 goroutine。
func TestStartConversationSummaryDisabled(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	for _, cfg := range []config.Conversation{
		{Enabled: false, SummaryEnabled: true},
		{Enabled: true, SummaryEnabled: false},
	} {
		stop := startConversationSummary(context.Background(), config.Config{Conversation: cfg}, nil, zap.New(core))
		stop()
	}
	if logs.Len() != 0 {
		t.Fatalf("关闭时仍写日志：%+v", logs.All())
	}
}

// TestStartConversationSummaryDegradesOnMissingDB 缺数据库只记日志并降级，不 panic。
func TestStartConversationSummaryDegradesOnMissingDB(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := config.Config{Conversation: config.Conversation{
		Enabled: true, SummaryEnabled: true, SummaryEveryTurns: 8,
		SummaryIntervalSeconds: 60, SummaryMaxAttempts: 3, SummaryContextTurns: 8,
	}}
	stop := startConversationSummary(context.Background(), cfg, nil, zap.New(core))
	stop()
	if logs.FilterMessage("会话摘要任务队列装配失败，跳过摘要").Len() != 1 {
		t.Fatalf("缺少降级日志：%+v", logs.All())
	}
}
