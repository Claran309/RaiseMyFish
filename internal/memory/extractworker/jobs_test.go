package extractworker

// 本文件测 M05 的任务队列模式：入队 → 领取 → 完成/失败。
//
// 重点覆盖 T07 的三类场景：
//   - 未回复的入向消息（status=pending）也能被提炼；
//   - 晚完成事件（at 很早、done 很晚）不被时间水位跳过；
//   - skipped 事件处理完标记 done，不反复重试。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

// fakeJobSource 是内存版任务队列，语义与 PG 实现对齐：
// Claim 只弹一次、attempts +1、Complete/Fail 记录调用。
type fakeJobSource struct {
	mu sync.Mutex

	pending     []Job
	completed   []string
	failed      []string
	reasons     []string
	retryable   bool
	claimed     int
	enqueue     int
	err         error
	completeErr error
}

func (f *fakeJobSource) Enqueue(context.Context, int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	n := f.enqueue
	f.enqueue = 0
	return n, nil
}

func (f *fakeJobSource) Claim(_ context.Context, owner string, _ time.Duration, limit int) ([]Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pending) == 0 {
		return nil, nil
	}
	n := limit
	if n > len(f.pending) {
		n = len(f.pending)
	}
	batch := f.pending[:n]
	f.pending = f.pending[n:]
	for i := range batch {
		batch[i].LeaseOwner = owner
		batch[i].Status = JobRunning
		batch[i].Attempts++
	}
	f.claimed += n
	return batch, nil
}

func (f *fakeJobSource) Complete(_ context.Context, job Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.completeErr != nil {
		return f.completeErr
	}
	f.completed = append(f.completed, job.EventID)
	return nil
}

func (f *fakeJobSource) Fail(_ context.Context, job Job, reason string, retryable bool, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, job.EventID)
	f.reasons = append(f.reasons, reason)
	f.retryable = retryable
	return nil
}

func (f *fakeJobSource) Renew(_ context.Context, job Job, _ time.Duration) (Job, error) {
	return job, nil
}

func (f *fakeJobSource) add(evs ...event.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range evs {
		f.pending = append(f.pending, Job{EventID: ev.ID, Version: ExtractionVersion, Status: JobPending, Event: ev})
	}
}

func (f *fakeJobSource) snapshot() (completed, failed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.completed...), append([]string(nil), f.failed...)
}

// newJobWorker 装配任务队列模式的 worker。
func newJobWorker(t *testing.T, jobs JobSource, repo store.MemoryRepo, opts Options) *Worker {
	t.Helper()
	opts.Jobs = jobs
	return newWorker(t, &fakeSource{}, repo, NewMemoryWatermark(), opts)
}

// TestJobQueueExtractsUnansweredInbound T07：未回复的入向消息也要提炼。
//
// 回归背景：旧实现只扫 status=done，没被回复的弹幕永远轮不到，
// 「他说过我是学生」这类事实就永远进不了记忆库。
func TestJobQueueExtractsUnansweredInbound(t *testing.T) {
	ev := mkEvent("live:1:3001", baseTime, "我保证明天带话", "路人甲", false, event.ChLive, "1")
	ev.Status = event.StatusPending // 还没被回复

	jobs := &fakeJobSource{}
	jobs.add(ev)
	repo := &fakeRepo{}

	stats, err := newJobWorker(t, jobs, repo, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 1 {
		t.Fatalf("未回复入向消息应被提炼：%+v", stats)
	}
	completed, failed := jobs.snapshot()
	if len(completed) != 1 || completed[0] != ev.ID {
		t.Fatalf("任务应标记完成：%v", completed)
	}
	if len(failed) != 0 {
		t.Fatalf("不应有失败：%v", failed)
	}
}

// TestJobQueueProcessesLateDoneEvent T07：晚完成事件不受事件时间影响。
//
// 任务表没有事件时间游标，所以 at 很早、done 很晚的事件照样会被领取。
func TestJobQueueProcessesLateDoneEvent(t *testing.T) {
	// 事件时间是两小时前，远早于任何「当前水位」。
	late := mkEvent("live:1:3002", baseTime.Add(-2*time.Hour), "我是大二学生", "路人乙", false, event.ChLive, "1")
	jobs := &fakeJobSource{}
	jobs.add(late)
	repo := &fakeRepo{}

	stats, err := newJobWorker(t, jobs, repo, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 1 {
		t.Fatalf("晚完成事件应被提炼：%+v", stats)
	}
	recs := repo.all()
	if len(recs) != 1 || recs[0].FromName != "路人乙" {
		t.Fatalf("记录主体不符：%+v", recs)
	}
}

// TestJobQueueMarksSkippedDone T07：没有候选的 skipped 事件标记 done，不反复重试。
func TestJobQueueMarksSkippedDone(t *testing.T) {
	ev := mkEvent("live:1:3003", baseTime, "哈哈哈哈", "路人丙", false, event.ChLive, "1")
	jobs := &fakeJobSource{}
	jobs.add(ev)
	repo := &fakeRepo{}

	stats, err := newJobWorker(t, jobs, repo, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Skipped != 1 || stats.Inserted != 0 {
		t.Fatalf("统计不符：%+v", stats)
	}
	completed, failed := jobs.snapshot()
	if len(completed) != 1 || len(failed) != 0 {
		t.Fatalf("跳过也算处理完毕：completed=%v failed=%v", completed, failed)
	}
}

// TestJobQueuePrivateUnauthorizedIsDone 私密未授权：标记完成而不是失败。
func TestJobQueuePrivateUnauthorizedIsDone(t *testing.T) {
	ev := mkEvent("dm:u1:3004", baseTime, "我保证明天带话", "私密观众", true, event.ChDM, "u1")
	jobs := &fakeJobSource{}
	jobs.add(ev)
	repo := &fakeRepo{}

	stats, err := newJobWorker(t, jobs, repo, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Skipped != 1 || stats.Inserted != 0 {
		t.Fatalf("统计不符：%+v", stats)
	}
	completed, failed := jobs.snapshot()
	if len(completed) != 1 || len(failed) != 0 {
		t.Fatalf("未授权私密事件不应反复重试：completed=%v failed=%v", completed, failed)
	}
}

// TestJobQueueFailureRequeues 落库失败：记账 + 回队（retryable），本轮不崩。
func TestJobQueueFailureRequeues(t *testing.T) {
	ev := mkEvent("live:1:3005", baseTime, "我保证明天带话", "路人丁", false, event.ChLive, "1")
	jobs := &fakeJobSource{}
	jobs.add(ev)
	repo := &fakeRepo{err: errors.New("database is down")}

	stats, err := newJobWorker(t, jobs, repo, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("单条失败不应让整轮报错：%v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("应记录失败：%+v", stats)
	}
	_, failed := jobs.snapshot()
	if len(failed) != 1 || failed[0] != ev.ID {
		t.Fatalf("失败任务应回队：%v", failed)
	}
	if !jobs.retryable {
		t.Error("落库失败应标记为可重试")
	}
}

// TestJobQueueCompleteConflictIgnored 租约被抢（Complete 冲突）不算失败。
func TestJobQueueCompleteConflictIgnored(t *testing.T) {
	ev := mkEvent("live:1:3006", baseTime, "我保证明天带话", "路人戊", false, event.ChLive, "1")
	jobs := &fakeJobSource{completeErr: store.ErrConflict}
	jobs.add(ev)
	repo := &fakeRepo{}

	stats, err := newJobWorker(t, jobs, repo, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("租约冲突不应报错：%v", err)
	}
	if stats.Inserted != 1 || stats.Failed != 0 {
		t.Fatalf("统计不符：%+v", stats)
	}
}

// TestJobQueueEmptyEventCompletes 事件已删除：任务直接完成，不做无限重试。
func TestJobQueueEmptyEventCompletes(t *testing.T) {
	jobs := &fakeJobSource{}
	jobs.add(event.Event{ID: "live:1:3007"}) // 只有 ID，没有正文（模拟查不到事件）
	repo := &fakeRepo{}

	stats, err := newJobWorker(t, jobs, repo, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Skipped != 1 || stats.Inserted != 0 {
		t.Fatalf("统计不符：%+v", stats)
	}
	completed, _ := jobs.snapshot()
	if len(completed) != 1 {
		t.Fatalf("空任务应完成：%v", completed)
	}
}
