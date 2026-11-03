package main

// 0.5 补丁：提炼 worker 的装配测试。全部用 mock 来源/仓库，不连 PG、不调模型。

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// memoryTestSource 按游标顺序返回预置的 done 事件。
type memoryTestSource struct {
	events []event.Event
}

func (s *memoryTestSource) ListDoneAfter(_ context.Context, after extractworker.Cursor, limit int) ([]event.Event, error) {
	var out []event.Event
	for _, ev := range s.events {
		if !after.IsZero() && !ev.At.After(after.At) && !(ev.At.Equal(after.At) && ev.ID > after.ID) {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// memoryTestRepo 只实现 worker 用到的写入与幂等语义，其余查询返回空。
type memoryTestRepo struct {
	mu      sync.Mutex
	records []store.MemoryRecord
}

func (r *memoryTestRepo) InsertRecord(_ context.Context, rec store.MemoryRecord) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := rec.Validate(); err != nil {
		return false, err
	}
	for _, existing := range r.records {
		if existing.ID == rec.ID {
			return false, nil
		}
	}
	r.records = append(r.records, rec)
	return true, nil
}

func (r *memoryTestRepo) Recent(context.Context, memory.Scope, int) ([]memory.Message, error) {
	return nil, nil
}

func (r *memoryTestRepo) CrossChannel(context.Context, memory.Scope, int) ([]memory.Message, error) {
	return nil, nil
}

func (r *memoryTestRepo) Records(context.Context, memory.Scope, int) ([]memory.Record, error) {
	return nil, nil
}

func (r *memoryTestRepo) ListRecords(context.Context, int) ([]store.MemoryRecord, error) {
	return nil, nil
}

func (r *memoryTestRepo) SearchRecords(context.Context, string, int) ([]store.MemoryRecord, error) {
	return nil, nil
}

func (r *memoryTestRepo) CountByKind(context.Context) (map[string]int, error) {
	return nil, nil
}

func (r *memoryTestRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

// TestNewMemoryWorkerAdvancesWatermark 装配真实 worker（mock 来源/仓库 + 文件水位），
// 一轮扫描后记录落库、水位推进到事件位置。
func TestNewMemoryWorkerAdvancesWatermark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "memory-watermark.json")
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := event.Event{
		ID: "live:room:m1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "u1", FromName: "路人", Text: "我保证明天带话", At: at, Status: event.StatusDone,
	}
	source := &memoryTestSource{events: []event.Event{ev}}
	repo := &memoryTestRepo{}

	worker, err := newMemoryWorker(config.Memory{Enabled: true, IntervalSeconds: 300}, source, repo, nil, path)
	if err != nil {
		t.Fatalf("装配 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Scanned != 1 || stats.Inserted != 1 {
		t.Fatalf("统计不符：%+v", stats)
	}
	if repo.count() != 1 {
		t.Fatalf("落库记录数 = %d，期望 1", repo.count())
	}

	watermark, err := extractworker.NewFileWatermark(path)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := watermark.Load(context.Background())
	if err != nil {
		t.Fatalf("读取水位失败：%v", err)
	}
	if cursor.ID != ev.ID || !cursor.At.Equal(ev.At) {
		t.Fatalf("水位 = %+v，期望 %s @ %v", cursor, ev.ID, ev.At)
	}
}

// scriptedWorker 让循环依次经历成功、失败、成功，用于验证失败后继续。
type scriptedWorker struct {
	mu    sync.Mutex
	calls int
	seen  chan int
}

func (w *scriptedWorker) RunOnce(context.Context) (extractworker.Stats, error) {
	w.mu.Lock()
	w.calls++
	n := w.calls
	w.mu.Unlock()
	select {
	case w.seen <- n:
	default:
	}
	if n == 2 {
		return extractworker.Stats{}, errors.New("模拟提炼失败")
	}
	return extractworker.Stats{Scanned: 1, Extracted: 1, Inserted: 2}, nil
}

// TestRunMemoryExtractLogsAndContinuesAfterError 单轮失败只记日志，下一轮继续；
// 成功轮输出 action=memory_extract 与 inserted。
func TestRunMemoryExtractLogsAndContinuesAfterError(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	worker := &scriptedWorker{seen: make(chan int, 8)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMemoryExtract(ctx, worker, 5*time.Millisecond, zap.New(core))
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
		t.Fatal("取消后提炼循环未退出")
	}

	var sawSuccess, sawFailure bool
	for _, entry := range logs.All() {
		fields := entry.ContextMap()
		if fields["action"] != "memory_extract" {
			continue
		}
		switch entry.Message {
		case "记忆提炼完成":
			if fields["inserted"] == int64(2) {
				sawSuccess = true
			}
		case "记忆提炼失败，等待下一轮":
			sawFailure = true
		}
	}
	if !sawSuccess || !sawFailure {
		t.Fatalf("日志缺少成功或失败记录：success=%v failure=%v", sawSuccess, sawFailure)
	}
}

// TestStartMemoryExtractorDisabled 关闭时不装配、不写日志。
func TestStartMemoryExtractorDisabled(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startMemoryExtractor(context.Background(), config.Memory{Enabled: false}, nil, zap.New(core))
	stop()
	if logs.Len() != 0 {
		t.Fatalf("关闭时不应启动或记日志：%v", logs.All())
	}
}

// TestStartMemoryExtractorDegradesOnBadDependency 依赖缺失只记日志并降级，不 panic。
func TestStartMemoryExtractorDegradesOnBadDependency(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startMemoryExtractor(context.Background(), config.Memory{Enabled: true, IntervalSeconds: 300}, nil, zap.New(core))
	stop()
	if logs.FilterMessage("记忆提炼装配失败，降级运行").Len() == 0 {
		t.Fatalf("初始化失败未记降级日志：%v", logs.All())
	}
}
