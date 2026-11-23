// Package conversationsummary 实现 C5 的会话滚动摘要 worker。
//
// 职责：定时找出「该摘要了」的会话 → 读最近轮次与旧摘要 → 让模型写一份新摘要 →
// 在租约栅栏内写回 conversation_state。
//
// 边界：
//   - 不写 SQL：读写都走注入的 store 接口；
//   - 不碰公开知识库：摘要只进 conversation_state.summary；
//   - 失败保留旧摘要，绝不阻塞回复主链路（worker 独立循环）；
//   - 幂等靠 (conversation_id, summarized_event_id) 与 summary_turn_count，
//     同一批轮次重复投递不会生成两份摘要。
package conversationsummary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/store"
)

// 默认参数。
const (
	// defaultEveryTurns 是累计多少新轮次触发摘要。
	defaultEveryTurns = 12
	// defaultIdle 是会话空闲多久触发摘要。
	defaultIdle = 5 * time.Minute
	// defaultInterval 是扫描间隔。
	defaultInterval = time.Minute
	// defaultTimeout 是单次模型调用超时。
	defaultTimeout = 20 * time.Second
	// defaultMaxAttempts 是任务最大尝试次数（含首次）。
	defaultMaxAttempts = 3
	// defaultContextTurns 是喂给摘要器的最近轮数。
	defaultContextTurns = 12
	// defaultSummaryRunes 是摘要长度上限。
	defaultSummaryRunes = 300
	// defaultLeaseTTL 是单条任务租约时长。
	defaultLeaseTTL = 2 * time.Minute
	// defaultClaimSize 是单批领取条数。
	defaultClaimSize = 5
	// maxBatches 限制单轮领取批数，避免一轮长时间占住循环。
	maxBatches = 10
	// turnTextRunes 是单条轮次正文进入提示词的上限。
	turnTextRunes = 120
	// renewDivisor 决定续租频率：每 TTL/3 续一次。
	renewDivisor = 3
)

// LLM 是摘要器需要的最小模型能力（由 provider 适配，测试用 mock）。
type LLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Options 是 worker 的行为参数。
type Options struct {
	// Jobs 是摘要任务队列；必填。
	Jobs store.ConversationSummaryRepo
	// States 读取会话状态与最近轮次；必填。
	States store.ConversationRepo
	// LLM 非 nil 时用模型摘要；nil 时退回抽取式摘要（仍可用，只是质量低）。
	LLM LLM
	// EveryTurns 是轮数触发阈值，<=0 用 12。
	EveryTurns int
	// Channels 是允许摘要的通道（如 dm/comment）；空表示不限制。
	Channels []string
	// Idle 是空闲触发阈值；0 表示关闭空闲触发。
	Idle time.Duration
	// Interval 是扫描间隔，<=0 用 1 分钟。
	Interval time.Duration
	// Timeout 是单次模型调用超时，<=0 用 20 秒。
	Timeout time.Duration
	// MaxAttempts 是任务最大尝试次数，<=0 用 3。
	MaxAttempts int
	// ContextTurns 是喂给摘要器的最近轮数，<=0 用 12。
	ContextTurns int
	// SummaryMaxRunes 是摘要长度上限，<=0 用 300。
	SummaryMaxRunes int
	// Owner 是租约标识，为空用 "conversation-summary"。
	Owner string
	// LeaseTTL 是单条任务租约，<=0 用 2 分钟。
	LeaseTTL time.Duration
	// Now 注入时钟，仅测试需要。
	Now func() time.Time
}

// Stats 是一轮扫描的统计。
type Stats struct {
	// Enqueued 本轮新入队/重置的任务数。
	Enqueued int
	// Claimed 本轮领取的任务数。
	Claimed int
	// Summarized 成功写回摘要的会话数。
	Summarized int
	// Empty 模型返回空/无可用轮次、只推进覆盖位置的会话数。
	Empty int
	// Failed 失败并已记录原因的会话数。
	Failed int
	// LeaseLost 失租的会话数（结果已放弃，由新持有者重做）。
	LeaseLost int
	// Skipped 会话已过期/已无新轮次而跳过的数量。
	Skipped int
}

// Worker 是摘要 worker 本体。
type Worker struct {
	opts Options
}

// New 构造 worker，并规范化参数。
func New(opts Options) (*Worker, error) {
	if opts.Jobs == nil {
		return nil, errors.New("摘要任务队列不能为 nil")
	}
	if opts.States == nil {
		return nil, errors.New("会话状态仓库不能为 nil")
	}
	if opts.EveryTurns <= 0 {
		opts.EveryTurns = defaultEveryTurns
	}
	if opts.Idle < 0 {
		return nil, errors.New("空闲阈值不能为负")
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultMaxAttempts
	}
	if opts.ContextTurns <= 0 {
		opts.ContextTurns = defaultContextTurns
	}
	if opts.SummaryMaxRunes <= 0 {
		opts.SummaryMaxRunes = defaultSummaryRunes
	}
	if strings.TrimSpace(opts.Owner) == "" {
		opts.Owner = "conversation-summary"
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = defaultLeaseTTL
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Worker{opts: opts}, nil
}

// Run 按 Interval 定时扫描，直到 ctx 被取消。
func (w *Worker) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("ctx 不能为 nil")
	}
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce 跑一轮：入队 → 领取 → 摘要 → 完成/失败。
func (w *Worker) RunOnce(ctx context.Context) (Stats, error) {
	var stats Stats
	if ctx == nil {
		return stats, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	now := w.now()
	enqueued, err := w.opts.Jobs.EnqueueSummaries(ctx, now, w.opts.Channels, w.opts.EveryTurns, w.opts.Idle, 200)
	if err != nil {
		return stats, fmt.Errorf("入队会话摘要任务：%w", err)
	}
	stats.Enqueued = enqueued

	for batch := 0; batch < maxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		jobs, err := w.opts.Jobs.ClaimSummaries(ctx, w.opts.Owner, w.opts.LeaseTTL, defaultClaimSize)
		if err != nil {
			return stats, fmt.Errorf("领取会话摘要任务：%w", err)
		}
		if len(jobs) == 0 {
			break
		}
		for _, job := range jobs {
			stats.Claimed++
			if err := w.handle(ctx, job, &stats); err != nil {
				return stats, err
			}
		}
	}
	return stats, nil
}

// handle 处理一条任务：读状态 → 摘要 → 租约栅栏内写回。
func (w *Worker) handle(ctx context.Context, job store.ConversationSummaryJob, stats *Stats) error {
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	lost, stopHeartbeat := w.startHeartbeat(ctx, job, cancelWork)
	summary, unanswered, skip, err := w.summarize(workCtx, job)
	stopHeartbeat()
	if leaseLost(lost) {
		stats.LeaseLost++
		return nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if failErr := w.fail(ctx, job, err.Error()); failErr != nil {
			return failErr
		}
		stats.Failed++
		return nil
	}
	if skip {
		// 会话已过期或没有新轮次：完成并推进覆盖位置，避免反复重算。
		if err := w.opts.Jobs.CompleteSummary(ctx, job, "", ""); err != nil {
			if errors.Is(err, store.ErrConflict) {
				stats.LeaseLost++
				return nil
			}
			return fmt.Errorf("完成空摘要任务：%w", err)
		}
		stats.Skipped++
		return nil
	}
	if err := w.opts.Jobs.CompleteSummary(ctx, job, summary, unanswered); err != nil {
		if errors.Is(err, store.ErrConflict) {
			stats.LeaseLost++
			return nil
		}
		if failErr := w.fail(ctx, job, err.Error()); failErr != nil {
			return failErr
		}
		stats.Failed++
		return nil
	}
	if summary == "" {
		stats.Empty++
	} else {
		stats.Summarized++
	}
	return nil
}

// summarize 读取会话并生成摘要；skip=true 表示无需生成（过期/无轮次）。
func (w *Worker) summarize(ctx context.Context, job store.ConversationSummaryJob) (string, string, bool, error) {
	state, ok, err := w.opts.States.LoadState(ctx, job.ConversationID, w.now())
	if err != nil {
		return "", "", false, fmt.Errorf("读取会话状态：%w", err)
	}
	if !ok {
		// 过期/已删除：只推进任务，不写摘要。
		return "", "", true, nil
	}
	turns, err := w.opts.States.RecentTurns(ctx, job.ConversationID, w.opts.ContextTurns)
	if err != nil {
		return "", "", false, fmt.Errorf("读取会话轮次：%w", err)
	}
	if len(turns) == 0 {
		return "", "", true, nil
	}
	if w.opts.LLM == nil {
		return clipRunes(extractiveSummary(turns), w.opts.SummaryMaxRunes), "", false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, w.opts.Timeout)
	defer cancel()
	text, err := w.opts.LLM.Complete(callCtx, SummarySystemPrompt, buildSummaryPrompt(state.Summary, turns, w.opts.SummaryMaxRunes))
	if err != nil {
		return "", "", false, fmt.Errorf("生成会话摘要：%w", err)
	}
	return clipRunes(cleanSummary(text), w.opts.SummaryMaxRunes), "", false, nil
}

// fail 记录失败；失租返回 nil（由新持有者重做）。
func (w *Worker) fail(ctx context.Context, job store.ConversationSummaryJob, reason string) error {
	err := w.opts.Jobs.FailSummary(ctx, job, reason, true, w.opts.MaxAttempts)
	if err == nil || errors.Is(err, store.ErrConflict) {
		return nil
	}
	return fmt.Errorf("记录会话摘要任务失败：%w", err)
}

// startHeartbeat 在摘要执行期间定期续租。
func (w *Worker) startHeartbeat(ctx context.Context, job store.ConversationSummaryJob, cancelWork context.CancelFunc) (<-chan struct{}, func()) {
	lost := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	interval := w.opts.LeaseTTL / renewDivisor
	if interval <= 0 {
		interval = w.opts.LeaseTTL
	}
	go func() {
		defer close(done)
		renew := func() bool {
			if _, err := w.opts.Jobs.RenewSummary(ctx, job, w.opts.LeaseTTL); err != nil {
				if errors.Is(err, store.ErrConflict) {
					close(lost)
					cancelWork()
					return false
				}
				return ctx.Err() == nil
			}
			return true
		}
		if !renew() {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if !renew() {
					return
				}
			}
		}
	}()
	var once sync.Once
	return lost, func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

func leaseLost(lost <-chan struct{}) bool {
	select {
	case <-lost:
		return true
	default:
		return false
	}
}

func (w *Worker) now() time.Time {
	return w.opts.Now()
}

// SummarySystemPrompt 是固定的摘要系统提示词。
const SummarySystemPrompt = `你是会话摘要器。把一段多轮对话压缩成给下一位回复者看的背景摘要。

要求：
1. 只写「谁在问什么、已经答到哪、还有什么没解决」，不写寒暄与情绪；
2. 保留稳定的称呼、对象与关键数字，不臆造没出现的信息；
3. 不记录密码、地址、手机号等敏感信息，遇到也只用「用户提到过隐私信息」概括；
4. 200 字以内，一行输出，不要 JSON、不要 markdown、不要标题。`

// buildSummaryPrompt 拼装摘要输入：旧摘要只作背景，最近轮次是事实来源。
func buildSummaryPrompt(previous string, turns []store.ConversationTurn, maxRunes int) string {
	var b strings.Builder
	if prev := strings.TrimSpace(previous); prev != "" {
		b.WriteString("【旧摘要（只作背景，可能与最新轮次冲突，以最近轮次为准）】\n")
		b.WriteString(clipRunes(prev, maxRunes))
		b.WriteString("\n\n")
	}
	b.WriteString("【最近轮次】\n")
	for _, turn := range turns {
		speaker := "用户"
		if turn.Dir == "out" {
			speaker = "本鱼"
		}
		if name := strings.TrimSpace(turn.FromName); name != "" && turn.Dir != "out" {
			speaker = name
		}
		b.WriteString(speaker)
		b.WriteString("：")
		b.WriteString(clipRunes(strings.TrimSpace(turn.Text), turnTextRunes))
		b.WriteString("\n")
	}
	b.WriteString("\n请输出新摘要。")
	return b.String()
}

// cleanSummary 去掉模型可能带的换行/前缀，压成一行。
func cleanSummary(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\r", "")
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, " ")
}

// extractiveSummary 是无模型时的兜底：按时间顺序摘取最近的用户发言。
//
// 它不追求质量，只保证「没有模型时也有可用的背景」，且绝不编造内容。
func extractiveSummary(turns []store.ConversationTurn) string {
	parts := make([]string, 0, 4)
	for i := len(turns) - 1; i >= 0 && len(parts) < 4; i-- {
		if turns[i].Dir == "out" {
			continue
		}
		text := strings.TrimSpace(turns[i].Text)
		if text == "" {
			continue
		}
		parts = append(parts, clipRunes(text, 40))
	}
	if len(parts) == 0 {
		return ""
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return "最近提到：" + strings.Join(parts, "；")
}

func clipRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}
