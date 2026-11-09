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
	"fmt"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// fakeJobSource 是内存版任务队列，语义与 PG 实现对齐：
// Claim 只弹一次、attempts +1、Complete/CompleteWithRecords/Renew 都带租约栅栏。
type fakeJobSource struct {
	mu sync.Mutex

	pending   []Job
	completed []string
	failed    []string
	reasons   []string
	retryable bool
	claimed   int
	enqueue   int
	err       error

	// repo 非 nil 时 CompleteWithRecords 真实写库，用来验证「记录与完成同事务」。
	repo store.MemoryRepo
	// leaseUntil 是每个任务的租约到期时间；Complete/Renew 都按它做栅栏。
	leaseUntil map[string]time.Time
	renewCalls map[string]int
	// claimLimits 记录每次领取请求的 limit，验证小批领取。
	claimLimits []int
	// renewConflict 关闭后，下一次续租返回 ErrConflict（模拟被抢占）。
	renewConflict <-chan struct{}
	// expireOnClaim 为 true 时，领取后立刻把租约置为已过期（模拟排队太久）。
	expireOnClaim bool
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

func (f *fakeJobSource) Claim(_ context.Context, owner string, ttl time.Duration, limit int) ([]Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.claimLimits = append(f.claimLimits, limit)
	if len(f.pending) == 0 {
		return nil, nil
	}
	n := limit
	if n > len(f.pending) {
		n = len(f.pending)
	}
	batch := f.pending[:n]
	f.pending = f.pending[n:]
	now := time.Now()
	if f.leaseUntil == nil {
		f.leaseUntil = make(map[string]time.Time)
	}
	if f.renewCalls == nil {
		f.renewCalls = make(map[string]int)
	}
	for i := range batch {
		batch[i].LeaseOwner = owner
		batch[i].Status = JobRunning
		batch[i].Attempts++
		batch[i].LeaseUntil = now.Add(ttl)
		f.leaseUntil[batch[i].EventID] = batch[i].LeaseUntil
		if f.expireOnClaim {
			f.leaseUntil[batch[i].EventID] = now.Add(-time.Second)
		}
	}
	f.claimed += n
	return batch, nil
}

// leaseValidLocked 判断任务租约是否仍然有效；调用方必须持有锁。
func (f *fakeJobSource) leaseValidLocked(job Job) bool {
	until, ok := f.leaseUntil[job.EventID]
	return ok && !time.Now().After(until)
}

func (f *fakeJobSource) Complete(_ context.Context, job Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.leaseValidLocked(job) {
		return store.ErrConflict
	}
	f.completed = append(f.completed, job.EventID)
	return nil
}

func (f *fakeJobSource) CompleteWithRecords(ctx context.Context, job Job, records []store.MemoryRecord) ([]RecordOutcome, error) {
	f.mu.Lock()
	if !f.leaseValidLocked(job) {
		f.mu.Unlock()
		return nil, store.ErrConflict
	}
	repo := f.repo
	f.mu.Unlock()

	outcomes := make([]RecordOutcome, 0, len(records))
	if repo != nil {
		for _, record := range records {
			inserted, err := repo.InsertRecord(ctx, record)
			if err != nil {
				// 同事务语义：任一记录失败整体回滚，任务保持未完成。
				return nil, err
			}
			outcomes = append(outcomes, RecordOutcome{Inserted: inserted})
		}
	} else {
		for range records {
			outcomes = append(outcomes, RecordOutcome{Inserted: true})
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, job.EventID)
	return outcomes, nil
}

func (f *fakeJobSource) Fail(_ context.Context, job Job, reason string, retryable bool, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.leaseValidLocked(job) {
		return store.ErrConflict
	}
	f.failed = append(f.failed, job.EventID)
	f.reasons = append(f.reasons, reason)
	f.retryable = retryable
	return nil
}

func (f *fakeJobSource) Renew(_ context.Context, job Job, ttl time.Duration) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewCalls == nil {
		f.renewCalls = make(map[string]int)
	}
	f.renewCalls[job.EventID]++
	if !f.leaseValidLocked(job) {
		return Job{}, store.ErrConflict
	}
	if f.renewConflict != nil {
		select {
		case <-f.renewConflict:
			return Job{}, store.ErrConflict
		default:
		}
	}
	job.LeaseUntil = time.Now().Add(ttl)
	f.leaseUntil[job.EventID] = job.LeaseUntil
	return job, nil
}

func (f *fakeJobSource) add(evs ...event.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range evs {
		f.pending = append(f.pending, Job{EventID: ev.ID, Version: ExtractionVersion, Status: JobPending, Event: ev})
	}
}

// revoke 模拟租约到期且任务已被第二个 worker 接手。
func (f *fakeJobSource) revoke(eventID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaseUntil[eventID] = time.Now().Add(-time.Second)
}

func (f *fakeJobSource) renewCount(eventID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renewCalls[eventID]
}

func (f *fakeJobSource) lastClaimLimit() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.claimLimits) == 0 {
		return 0
	}
	return f.claimLimits[len(f.claimLimits)-1]
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
	if fake, ok := jobs.(*fakeJobSource); ok {
		fake.repo = repo
	}
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

// TestJobQueueClaimsSmallBatch 领取批次按实际容量收窄：默认 BatchSize=50 也只领 10。
func TestJobQueueClaimsSmallBatch(t *testing.T) {
	jobs := &fakeJobSource{}
	for i := 0; i < 12; i++ {
		ev := mkEvent(fmt.Sprintf("live:1:33%02d", i), baseTime.Add(time.Duration(i)*time.Second),
			"我保证明天带话", "小批观众", false, event.ChLive, "1")
		jobs.add(ev)
	}
	repo := &fakeRepo{}

	stats, err := newJobWorker(t, jobs, repo, Options{BatchSize: 50}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if jobs.claimed != 12 || stats.Inserted != 12 {
		t.Fatalf("全部任务应被处理：claimed=%d stats=%+v", jobs.claimed, stats)
	}
	jobs.mu.Lock()
	limits := append([]int(nil), jobs.claimLimits...)
	jobs.mu.Unlock()
	if len(limits) == 0 {
		t.Fatal("应发生领取")
	}
	for _, limit := range limits {
		if limit > maxJobClaimSize {
			t.Fatalf("领取 limit=%d，超过单批上限 %d", limit, maxJobClaimSize)
		}
	}
}

// TestJobQueueRenewsDuringLongExtraction 单条提炼耗时超过租约时，续租让整批都活下来。
//
// 回归背景：默认一次领 50 条、共用 2 分钟租约且从不 Renew，
// 队尾在轮到自己之前就过期，被第二个 worker 抢走后整条重做。
func TestJobQueueRenewsDuringLongExtraction(t *testing.T) {
	if testing.Short() {
		t.Skip("涉及真实时间等待，短路模式跳过")
	}
	jobs := &fakeJobSource{}
	first := mkEvent("live:1:3401", baseTime, "我保证明天带话", "慢速甲", false, event.ChLive, "1")
	second := mkEvent("live:1:3402", baseTime.Add(time.Second), "我是大二学生", "慢速乙", false, event.ChLive, "1")
	jobs.add(first, second)
	repo := &fakeRepo{}
	extractor := &slowExtractor{delay: 800 * time.Millisecond, inner: memory.NewRuleExtractor()}

	stats, err := newJobWorker(t, jobs, repo, Options{
		Extractor: extractor,
		JobTTL:    1500 * time.Millisecond,
	}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 2 || stats.LeaseLost != 0 || stats.Failed != 0 {
		t.Fatalf("续租后两条都应落库：%+v", stats)
	}
	completed, failed := jobs.snapshot()
	if len(completed) != 2 || len(failed) != 0 {
		t.Fatalf("两条任务都应完成：completed=%v failed=%v", completed, failed)
	}
	for _, id := range []string{first.ID, second.ID} {
		if jobs.renewCount(id) == 0 {
			t.Errorf("%s 执行期间未续租", id)
		}
	}
}

// TestJobQueueRenewConflictCancelsWork 续租发现失租：立即取消提炼，不写库不记失败。
func TestJobQueueRenewConflictCancelsWork(t *testing.T) {
	ev := mkEvent("live:1:3403", baseTime, "我保证明天带话", "被抢观众", false, event.ChLive, "1")
	conflict := make(chan struct{})
	jobs := &fakeJobSource{renewConflict: conflict}
	jobs.add(ev)
	repo := &fakeRepo{}
	extractor := &blockingExtractor{started: make(chan struct{}), canceled: make(chan struct{})}
	worker := newJobWorker(t, jobs, repo, Options{
		Extractor: extractor,
		JobTTL:    60 * time.Millisecond,
	})

	type outcome struct {
		stats Stats
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		stats, err := worker.RunOnce(context.Background())
		done <- outcome{stats: stats, err: err}
	}()

	<-extractor.started
	close(conflict) // 模拟另一个 worker 抢走租约
	got := <-done

	if got.err != nil {
		t.Fatalf("失租不应让整轮报错：%v", got.err)
	}
	select {
	case <-extractor.canceled:
	default:
		t.Fatal("续租失租后应取消提炼")
	}
	if got.stats.LeaseLost != 1 || got.stats.Inserted != 0 || got.stats.Failed != 0 {
		t.Fatalf("统计不符：%+v", got.stats)
	}
	if len(repo.all()) != 0 {
		t.Fatalf("失租后不应写库：%+v", repo.all())
	}
	completed, failed := jobs.snapshot()
	if len(completed) != 0 || len(failed) != 0 {
		t.Fatalf("失租既不算完成也不算失败：completed=%v failed=%v", completed, failed)
	}
}

// TestJobQueueStaleWorkerWritesNothing 旧 worker 提取完才发现租约被抢：事务栅栏整体回滚。
func TestJobQueueStaleWorkerWritesNothing(t *testing.T) {
	ev := mkEvent("live:1:3404", baseTime, "我保证明天带话", "旧worker", false, event.ChLive, "1")
	jobs := &fakeJobSource{}
	jobs.add(ev)
	repo := &fakeRepo{}
	extractor := &gatedExtractor{started: make(chan struct{}), release: make(chan struct{})}

	type outcome struct {
		stats Stats
		err   error
	}
	worker := newJobWorker(t, jobs, repo, Options{
		Extractor: extractor,
		JobTTL:    time.Hour, // 心跳不会触发，只有提交栅栏能拦住旧 worker
	})
	done := make(chan outcome, 1)
	go func() {
		stats, err := worker.RunOnce(context.Background())
		done <- outcome{stats: stats, err: err}
	}()

	<-extractor.started
	jobs.revoke(ev.ID) // 租约到期并被第二个 worker 接手
	close(extractor.release)
	got := <-done

	if got.err != nil {
		t.Fatalf("失租不应让整轮报错：%v", got.err)
	}
	if got.stats.LeaseLost != 1 || got.stats.Inserted != 0 || got.stats.Failed != 0 {
		t.Fatalf("统计不符：%+v", got.stats)
	}
	if len(repo.all()) != 0 {
		t.Fatalf("旧 worker 的提炼结果不得落库：%+v", repo.all())
	}
	completed, failed := jobs.snapshot()
	if len(completed) != 0 || len(failed) != 0 {
		t.Fatalf("旧 worker 既不能完成也不能记失败：completed=%v failed=%v", completed, failed)
	}
}

// TestJobQueueExpiredLeaseAtStartWritesNothing 领取时租约就已过期（队尾等待）：直接放弃。
func TestJobQueueExpiredLeaseAtStartWritesNothing(t *testing.T) {
	ev := mkEvent("live:1:3405", baseTime, "我保证明天带话", "排队观众", false, event.ChLive, "1")
	jobs := &fakeJobSource{expireOnClaim: true}
	jobs.add(ev)
	repo := &fakeRepo{}

	stats, err := newJobWorker(t, jobs, repo, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("失租不应让整轮报错：%v", err)
	}
	if stats.LeaseLost != 1 || stats.Inserted != 0 {
		t.Fatalf("统计不符：%+v", stats)
	}
	if len(repo.all()) != 0 {
		t.Fatalf("过期租约不应写库：%+v", repo.all())
	}
}

// slowExtractor 模拟耗时提炼（LLM 调用），让续租心跳有用武之地。
type slowExtractor struct {
	delay time.Duration
	inner memory.Extractor
}

func (s *slowExtractor) Extract(ctx context.Context, ev event.Event, opts memory.ExtractOptions) ([]memory.Candidate, error) {
	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return s.inner.Extract(ctx, ev, opts)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// blockingExtractor 一直阻塞到 ctx 被取消，用来验证失租取消。
type blockingExtractor struct {
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (b *blockingExtractor) Extract(ctx context.Context, _ event.Event, _ memory.ExtractOptions) ([]memory.Candidate, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	close(b.canceled)
	return nil, ctx.Err()
}

// gatedExtractor 在测试放行后完成提炼，用来模拟「提取完但提交前被抢占」。
type gatedExtractor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedExtractor) Extract(ctx context.Context, ev event.Event, opts memory.ExtractOptions) ([]memory.Candidate, error) {
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.release:
		return memory.NewRuleExtractor().Extract(ctx, ev, opts)
	case <-ctx.Done():
		return nil, ctx.Err()
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
