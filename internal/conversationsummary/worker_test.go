package conversationsummary

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

// fakeJobs 是内存版摘要任务队列，复刻租约/栅栏语义。
type fakeJobs struct {
	mu         sync.Mutex
	jobs       map[string]*store.ConversationSummaryJob
	enqueued   int
	completeFn func(job store.ConversationSummaryJob, summary, unanswered string) error
	failCalls  int
	renewCalls int
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{jobs: map[string]*store.ConversationSummaryJob{}}
}

func (f *fakeJobs) EnqueueSummaries(context.Context, time.Time, []string, int, time.Duration, int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enqueued, nil
}

func (f *fakeJobs) ClaimSummaries(context.Context, string, time.Duration, int) ([]store.ConversationSummaryJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.ConversationSummaryJob, 0, len(f.jobs))
	for _, job := range f.jobs {
		// 假队列只领取一次：真实队列的失败回队由仓储测试覆盖，
		// 这里要断言的是「单次处理」的结果，而不是重试循环。
		if job.Status != "pending" || job.Attempts > 0 {
			continue
		}
		job.Status = "running"
		job.LeaseOwner = "owner"
		job.Attempts++
		out = append(out, *job)
	}
	return out, nil
}

func (f *fakeJobs) CompleteSummary(_ context.Context, job store.ConversationSummaryJob, summary, unanswered string) error {
	f.mu.Lock()
	stored, ok := f.jobs[job.ConversationID]
	f.mu.Unlock()
	if !ok || stored.Status != "running" || stored.LeaseOwner != job.LeaseOwner {
		return store.ErrConflict
	}
	if f.completeFn != nil {
		return f.completeFn(job, summary, unanswered)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	stored.Status = "done"
	stored.LastError = ""
	return nil
}

func (f *fakeJobs) FailSummary(_ context.Context, job store.ConversationSummaryJob, reason string, _ bool, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.jobs[job.ConversationID]
	if !ok || stored.LeaseOwner != job.LeaseOwner {
		return store.ErrConflict
	}
	f.failCalls++
	stored.Status = "pending"
	stored.LastError = reason
	return nil
}

func (f *fakeJobs) RenewSummary(_ context.Context, job store.ConversationSummaryJob, _ time.Duration) (store.ConversationSummaryJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	stored, ok := f.jobs[job.ConversationID]
	if !ok || stored.LeaseOwner != job.LeaseOwner {
		return store.ConversationSummaryJob{}, store.ErrConflict
	}
	return *stored, nil
}

// fakeStates 是内存版会话状态仓库。
type fakeStates struct {
	mu      sync.Mutex
	states  map[string]store.ConversationState
	turns   map[string][]store.ConversationTurn
	loadErr error
	turnErr error
}

func newFakeStates() *fakeStates {
	return &fakeStates{states: map[string]store.ConversationState{}, turns: map[string][]store.ConversationTurn{}}
}

func (f *fakeStates) LoadState(_ context.Context, id string, now time.Time) (store.ConversationState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return store.ConversationState{}, false, f.loadErr
	}
	state, ok := f.states[id]
	if !ok {
		return store.ConversationState{}, false, nil
	}
	if !state.ExpiresAt.IsZero() && !state.ExpiresAt.After(now) {
		return store.ConversationState{}, false, nil
	}
	return state, true, nil
}

func (f *fakeStates) Touch(context.Context, store.ConversationState) error { return nil }

func (f *fakeStates) RecentTurns(_ context.Context, id string, limit int) ([]store.ConversationTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.turnErr != nil {
		return nil, f.turnErr
	}
	turns := f.turns[id]
	if len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	return turns, nil
}

type fakeLLM struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	reply   string
	err     error
	delay   time.Duration
}

func (f *fakeLLM) Complete(ctx context.Context, _, user string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.prompts = append(f.prompts, user)
	reply, err, delay := f.reply, f.err, f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return reply, err
}

func newTestWorker(t *testing.T, jobs *fakeJobs, states *fakeStates, llm LLM) *Worker {
	t.Helper()
	worker, err := New(Options{
		Jobs: jobs, States: states, LLM: llm,
		EveryTurns: 4, Idle: time.Minute, Interval: time.Minute,
		Timeout: 2 * time.Second, ContextTurns: 6, SummaryMaxRunes: 100,
		Owner: "test-owner", LeaseTTL: time.Minute,
		Now: func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func seedConversation(states *fakeStates, id string) {
	states.states[id] = store.ConversationState{
		ConversationID: id, Channel: "dm", Summary: "旧摘要：在聊鱼",
		TurnCount: 6, ExpiresAt: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
	}
	states.turns[id] = []store.ConversationTurn{
		{EventID: "e1", Dir: "in", FromName: "阿明", Text: "你记得我吗"},
		{EventID: "e2", Dir: "out", Text: "记得，你是养鱼那位"},
		{EventID: "e3", Dir: "in", FromName: "阿明", Text: "那上次说的鱼呢"},
	}
}

// TestRunOnceWritesSummary 正常路径：读旧摘要+轮次，写回新摘要。
func TestRunOnceWritesSummary(t *testing.T) {
	jobs, states := newFakeJobs(), newFakeStates()
	seedConversation(states, "dm:1001:2002")
	jobs.enqueued = 1
	jobs.jobs["dm:1001:2002"] = &store.ConversationSummaryJob{
		ConversationID: "dm:1001:2002", TargetEventID: "e3", TurnCount: 6, Status: "pending",
	}
	llm := &fakeLLM{reply: "阿明问本鱼是否记得他，本鱼确认后在聊上次提到的鱼。"}
	var got string
	jobs.completeFn = func(_ store.ConversationSummaryJob, summary, _ string) error {
		got = summary
		return nil
	}
	stats, err := newTestWorker(t, jobs, states, llm).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summarized != 1 || llm.calls != 1 || got == "" {
		t.Fatalf("摘要未写回：%+v calls=%d summary=%q", stats, llm.calls, got)
	}
	if !strings.Contains(llm.prompts[0], "旧摘要") || !strings.Contains(llm.prompts[0], "那上次说的鱼呢") {
		t.Fatalf("提示词缺少旧摘要或最近轮次：%s", llm.prompts[0])
	}
}

// TestLLMFailureKeepsOldSummary 失败时保留旧摘要、任务回队，不写空摘要。
func TestLLMFailureKeepsOldSummary(t *testing.T) {
	jobs, states := newFakeJobs(), newFakeStates()
	seedConversation(states, "dm:1001:2002")
	jobs.jobs["dm:1001:2002"] = &store.ConversationSummaryJob{ConversationID: "dm:1001:2002", TargetEventID: "e3", TurnCount: 6, Status: "pending"}
	llm := &fakeLLM{err: errors.New("模型超时")}
	written := false
	jobs.completeFn = func(store.ConversationSummaryJob, string, string) error {
		written = true
		return nil
	}
	stats, err := newTestWorker(t, jobs, states, llm).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 1 || written || jobs.failCalls != 1 {
		t.Fatalf("失败路径错误：%+v written=%v fail=%d", stats, written, jobs.failCalls)
	}
	if states.states["dm:1001:2002"].Summary != "旧摘要：在聊鱼" {
		t.Fatal("旧摘要被覆盖")
	}
}

// TestExpiredConversationSkips 过期会话只推进任务，不调模型。
func TestExpiredConversationSkips(t *testing.T) {
	jobs, states := newFakeJobs(), newFakeStates()
	states.states["dm:1:2"] = store.ConversationState{
		ConversationID: "dm:1:2", Channel: "dm", TurnCount: 6,
		ExpiresAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
	jobs.jobs["dm:1:2"] = &store.ConversationSummaryJob{ConversationID: "dm:1:2", TargetEventID: "e9", TurnCount: 6, Status: "pending"}
	llm := &fakeLLM{reply: "不该被调用"}
	stats, err := newTestWorker(t, jobs, states, llm).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 1 || llm.calls != 0 {
		t.Fatalf("过期会话仍调用模型：%+v calls=%d", stats, llm.calls)
	}
}

// TestLeaseConflictDoesNotCountAsFailure 失租不算失败，交给新持有者重做。
func TestLeaseConflictDoesNotCountAsFailure(t *testing.T) {
	jobs, states := newFakeJobs(), newFakeStates()
	seedConversation(states, "dm:1:2")
	jobs.jobs["dm:1:2"] = &store.ConversationSummaryJob{ConversationID: "dm:1:2", TargetEventID: "e3", TurnCount: 6, Status: "pending"}
	jobs.completeFn = func(store.ConversationSummaryJob, string, string) error { return store.ErrConflict }
	stats, err := newTestWorker(t, jobs, states, &fakeLLM{reply: "新摘要"}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.LeaseLost != 1 || stats.Failed != 0 {
		t.Fatalf("失租被当成失败：%+v", stats)
	}
}

// TestNoLLMUsesExtractiveSummary 没有模型时也要有可读摘要。
func TestNoLLMUsesExtractiveSummary(t *testing.T) {
	jobs, states := newFakeJobs(), newFakeStates()
	seedConversation(states, "dm:1:2")
	jobs.jobs["dm:1:2"] = &store.ConversationSummaryJob{ConversationID: "dm:1:2", TargetEventID: "e3", TurnCount: 6, Status: "pending"}
	var got string
	jobs.completeFn = func(_ store.ConversationSummaryJob, summary, _ string) error {
		got = summary
		return nil
	}
	stats, err := newTestWorker(t, jobs, states, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summarized != 1 || !strings.Contains(got, "最近提到") {
		t.Fatalf("兜底摘要不符：%+v %q", stats, got)
	}
}

// TestSummaryClipAndOneLine 摘要被裁剪成一行，且不超过上限。
func TestSummaryClipAndOneLine(t *testing.T) {
	long := strings.Repeat("鱼", 300) + "\n第二行"
	got := clipRunes(cleanSummary(long), 100)
	if strings.Contains(got, "\n") || len([]rune(got)) != 100 {
		t.Fatalf("摘要清洗错误：%q", got)
	}
}

// TestNewRejectsMissingDeps 缺依赖直接报错，不静默降级。
func TestNewRejectsMissingDeps(t *testing.T) {
	if _, err := New(Options{States: newFakeStates()}); err == nil {
		t.Fatal("缺任务队列被接受")
	}
	if _, err := New(Options{Jobs: newFakeJobs()}); err == nil {
		t.Fatal("缺状态仓库被接受")
	}
	if _, err := New(Options{Jobs: newFakeJobs(), States: newFakeStates(), Idle: -time.Second}); err == nil {
		t.Fatal("负空闲阈值被接受")
	}
}
