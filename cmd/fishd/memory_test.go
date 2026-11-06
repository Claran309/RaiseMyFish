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
		if !after.IsZero() && (ev.At.Before(after.At) || (ev.At.Equal(after.At) && ev.ID <= after.ID)) {
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

// memoryTestSoul 提供灵魂记忆候选，记录调用次数。
type memoryTestSoul struct {
	records []store.MemoryRecord
	calls   int
}

func (s *memoryTestSoul) SoulCandidates(context.Context, float64, int) ([]store.MemoryRecord, error) {
	s.calls++
	return s.records, nil
}

// TestNewMemoryWorkerWiresSoul soul_enabled=true 时装配并真正重建文件。
func TestNewMemoryWorkerWiresSoul(t *testing.T) {
	dir := t.TempDir()
	soulPath := filepath.Join(dir, "soul-memories.md")
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	soul := &memoryTestSoul{records: []store.MemoryRecord{{
		Record: memory.Record{ID: "soul-1", Summary: "主人是学生", Importance: 0.9, ValidFrom: at},
		Source: "persona", Kind: "fact", Title: "主人是学生",
	}}}
	worker, err := newMemoryWorker(memoryWorkerDeps{
		Config: config.Memory{
			Enabled: true, IntervalSeconds: 300,
			SoulEnabled: true, SoulPath: soulPath, SoulMaxEntries: 5,
		},
		Source:        &memoryTestSource{},
		Repo:          &memoryTestRepo{},
		Soul:          soul,
		WatermarkPath: filepath.Join(dir, "watermark.json"),
	})
	if err != nil {
		t.Fatalf("装配 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if soul.calls != 1 || stats.SoulEntries != 1 {
		t.Fatalf("灵魂记忆未接线：calls=%d stats=%+v", soul.calls, stats)
	}
	entries, err := memory.LoadSoul(context.Background(), soulPath)
	if err != nil || len(entries) != 1 || entries[0].Title != "主人是学生" {
		t.Fatalf("灵魂记忆文件不符：%+v / %v", entries, err)
	}
}

// TestNewMemoryWorkerSoulDisabled soul_enabled=false 时不接来源、不写文件。
func TestNewMemoryWorkerSoulDisabled(t *testing.T) {
	dir := t.TempDir()
	soulPath := filepath.Join(dir, "soul-memories.md")
	soul := &memoryTestSoul{}
	worker, err := newMemoryWorker(memoryWorkerDeps{
		Config: config.Memory{
			Enabled: true, IntervalSeconds: 300,
			SoulEnabled: false, SoulPath: soulPath, SoulMaxEntries: 5,
		},
		Source:        &memoryTestSource{},
		Repo:          &memoryTestRepo{},
		Soul:          soul,
		WatermarkPath: filepath.Join(dir, "watermark.json"),
	})
	if err != nil {
		t.Fatalf("装配 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if soul.calls != 0 || stats.SoulEntries != -1 {
		t.Fatalf("关闭时不应重建：calls=%d stats=%+v", soul.calls, stats)
	}
	if entries, err := memory.LoadSoul(context.Background(), soulPath); err != nil || len(entries) != 0 {
		t.Fatalf("关闭时不应写文件：%+v / %v", entries, err)
	}
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

	worker, err := newMemoryWorker(memoryWorkerDeps{
		Config: config.Memory{Enabled: true, IntervalSeconds: 300},
		Source: source, Repo: repo, WatermarkPath: path,
	})
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

// TestNewMemoryWorkerHonorsConfigAndStateFreeze 装配层要把配置冻结和运行时状态文件都接进 worker。
func TestNewMemoryWorkerHonorsConfigAndStateFreeze(t *testing.T) {
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := event.Event{
		ID: "live:room:f1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "u1", FromName: "路人", Text: "我保证明天带话", At: at, Status: event.StatusDone,
	}

	// 配置冻结
	source := &memoryTestSource{events: []event.Event{ev}}
	repo := &memoryTestRepo{}
	wmPath := filepath.Join(t.TempDir(), "watermark.json")
	freezePath := filepath.Join(t.TempDir(), "state", "memory.frozen")
	worker, err := newMemoryWorker(memoryWorkerDeps{
		Config: config.Memory{Enabled: true, IntervalSeconds: 300, Frozen: true},
		Source: source, Repo: repo, WatermarkPath: wmPath, FreezePath: freezePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if !stats.Frozen || stats.FrozenReason != "config" || repo.count() != 0 {
		t.Fatalf("配置冻结未生效：stats=%+v records=%d", stats, repo.count())
	}

	// 运行时状态文件冻结
	if err := memory.Freeze(freezePath); err != nil {
		t.Fatal(err)
	}
	worker, err = newMemoryWorker(memoryWorkerDeps{
		Config: config.Memory{Enabled: true, IntervalSeconds: 300},
		Source: source, Repo: repo, WatermarkPath: wmPath, FreezePath: freezePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, err = worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if !stats.Frozen || stats.FrozenReason != "state_file" || repo.count() != 0 {
		t.Fatalf("状态文件冻结未生效：stats=%+v records=%d", stats, repo.count())
	}

	// 解冻后恢复提炼
	if err := memory.Unfreeze(freezePath); err != nil {
		t.Fatal(err)
	}
	stats, err = worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Frozen || stats.Inserted != 1 || repo.count() != 1 {
		t.Fatalf("解冻后未恢复提炼：stats=%+v records=%d", stats, repo.count())
	}
}

// frozenWorker 始终返回冻结统计，用于验证循环日志。
type frozenWorker struct{}

func (frozenWorker) RunOnce(context.Context) (extractworker.Stats, error) {
	return extractworker.Stats{Frozen: true, FrozenReason: "state_file"}, nil
}

// fakeRecentEvents 记录上下文查询的边界参数，并按预置返回历史。
type fakeRecentEvents struct {
	rows  []event.Event
	gotAt time.Time
	gotID string
	calls int
}

func (f *fakeRecentEvents) RecentEvents(_ context.Context, _ memory.Scope, _ int) ([]event.Event, error) {
	f.calls++
	return f.rows, nil
}

// RecentEventsBefore 实现带时间上界的查询（M07 生产装配依赖它）。
func (f *fakeRecentEvents) RecentEventsBefore(_ context.Context, _ memory.Scope, at time.Time, id string, _ int) ([]event.Event, error) {
	f.calls++
	f.gotAt, f.gotID = at, id
	return f.rows, nil
}

// captureTestExtractor 记录提炼器收到的上下文，用来证明 Recent 真的接进了 worker。
type captureTestExtractor struct {
	seen []event.Event
}

func (c *captureTestExtractor) Extract(_ context.Context, _ event.Event, opts memory.ExtractOptions) ([]memory.Candidate, error) {
	c.seen = opts.Context
	return nil, nil
}

// TestNewMemoryWorkerWiresRecent M06/M07：生产装配必须把 Recent 接进 worker，
// 且查询要带「被处理事件」的时间上界，不能读现在最新的内容。
func TestNewMemoryWorkerWiresRecent(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := event.Event{
		ID: "live:room:w1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "u1", FromName: "路人", Text: "我保证明天带话", At: at, Status: event.StatusDone,
	}
	past := event.Event{
		ID: "live:room:w0", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "u2", FromName: "甲", Text: "前面说的话", At: at.Add(-time.Minute), Status: event.StatusDone,
	}
	recent := &fakeRecentEvents{rows: []event.Event{past}}
	cap := &captureTestExtractor{}

	worker, err := newMemoryWorker(memoryWorkerDeps{
		Config:        config.Memory{Enabled: true, IntervalSeconds: 300},
		Source:        &memoryTestSource{events: []event.Event{ev}},
		Recent:        recent,
		Repo:          &memoryTestRepo{},
		Extractor:     cap,
		WatermarkPath: filepath.Join(t.TempDir(), "watermark.json"),
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if recent.calls != 1 {
		t.Fatalf("Recent 未接线：查询 %d 次", recent.calls)
	}
	if !recent.gotAt.Equal(ev.At) || recent.gotID != ev.ID {
		t.Fatalf("上下文上界不符：at=%v id=%q（期望 %v / %q）", recent.gotAt, recent.gotID, ev.At, ev.ID)
	}
	if len(cap.seen) != 1 || cap.seen[0].ID != past.ID {
		t.Fatalf("历史未进入提炼器：%+v", cap.seen)
	}
}

// TestNewMemoryWorkerWiresOwnerName M06：owner_name 必须真正传到提炼规则。
//
// 发言人昵称等于 owner_name → 额外产出一条 preference；
// 没接线时 OwnerName 为空，这条规则永远不会命中。
func TestNewMemoryWorkerWiresOwnerName(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := event.Event{
		ID: "live:room:o1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "1000", FromName: "Claran", Text: "晚安", At: at, Status: event.StatusDone,
	}
	repo := &memoryTestRepo{}
	worker, err := newMemoryWorker(memoryWorkerDeps{
		Config:        config.Memory{Enabled: true, IntervalSeconds: 300, OwnerName: "Claran"},
		Source:        &memoryTestSource{events: []event.Event{ev}},
		Repo:          repo,
		WatermarkPath: filepath.Join(t.TempDir(), "watermark.json"),
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	stats, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 1 || repo.count() != 1 {
		t.Fatalf("未按 owner_name 产生偏好：stats=%+v records=%d", stats, repo.count())
	}
	if kind := repo.records[0].Kind; kind != memory.KindPreference {
		t.Fatalf("记录类型 = %q，期望 preference", kind)
	}
}

// TestRunMemoryExtractLogsFrozen 冻结轮必须打 action=memory_frozen，且不把它记成一次成功提炼。
func TestRunMemoryExtractLogsFrozen(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMemoryExtract(ctx, frozenWorker{}, 5*time.Millisecond, zap.New(core))
	}()
	deadline := time.After(2 * time.Second)
	for logs.FilterMessage("记忆已冻结，本轮只读不写").Len() == 0 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("未记录冻结日志：%v", logs.All())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done

	entry := logs.FilterMessage("记忆已冻结，本轮只读不写").All()[0]
	fields := entry.ContextMap()
	if fields["action"] != "memory_frozen" || fields["reason"] != "state_file" {
		t.Fatalf("冻结日志字段不符：%v", fields)
	}
	if logs.FilterMessage("记忆提炼完成").Len() != 0 {
		t.Errorf("冻结轮不应同时记成成功提炼：%v", logs.All())
	}
}

// TestStartMemoryExtractorDisabled 关闭时不装配、不写日志。
func TestStartMemoryExtractorDisabled(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startMemoryExtractor(context.Background(), config.Config{Memory: config.Memory{Enabled: false}}, nil, zap.New(core))
	stop()
	if logs.Len() != 0 {
		t.Fatalf("关闭时不应启动或记日志：%v", logs.All())
	}
}

// TestStartMemoryExtractorDegradesOnBadDependency 依赖缺失只记日志并降级，不 panic。
func TestStartMemoryExtractorDegradesOnBadDependency(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	stop := startMemoryExtractor(context.Background(), config.Config{Memory: config.Memory{Enabled: true, IntervalSeconds: 300}}, nil, zap.New(core))
	stop()
	if logs.FilterMessage("记忆提炼装配失败，降级运行").Len() == 0 {
		t.Fatalf("初始化失败未记降级日志：%v", logs.All())
	}
}
