package viewer

// 本文件实现观众侧写的定时总结（#63/#64、R05/R06/R09）。
//
// 输入只有查询层已排除私密/DM 的公开弹幕；模型失败、输出损坏或未配置时
// 回落到统计聚合（旧家效果），不阻塞其他观众，也不影响采集/消费主链路。
//
// 侧写分两层（R06）：
//   - 稳定层：观众明确自述（basis=self）的性格/偏好等，跨轮保留、按新证据修订；
//   - 近期层：观察/推断得到的近期话题与互动，带有效期，过期后自动撤回，
//     注入文本明确标注「近期观察，仅参考」。
//
// 每条派生记忆都带 derived_by（模型/规则）与逐字段证据（R05），
// 不再把模型推断写成观众本人断言。
//
// 落库分三处：
//   - viewer_profiles.profile_summary：注入 prompt 的简短侧写；
//   - memory_records（kind=viewer_profile）：可被记忆检索召回；
//   - knowledge（viewers.md）：公开条目，可被 RAG 召回。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

const (
	// summaryActiveWindow 是「近期活跃」的回看窗口（旧家按天总结）。
	summaryActiveWindow = 24 * time.Hour
	// summaryRecentLimit 是喂给模型的最近公开消息条数。
	summaryRecentLimit = 20
	// summaryMaxPerRun 是单轮最多侧写多少位观众，控制模型费用（R09 小批试跑）。
	summaryMaxPerRun = 10
	// summaryBackfillLimit 是单轮首次回填名额；0 表示不回填新人。
	summaryBackfillLimit = 2
	// summaryMinNewMessages 是重写已有侧写所需的新增公开消息条数（R06/R09）。
	summaryMinNewMessages = 3
	// summaryRecentTTL 是近期层字段的有效期（R06）。
	summaryRecentTTL = 7 * 24 * time.Hour
	// summaryDailyViewers 是每天最多侧写多少位观众（按摘要尝试时间计数，R09）。
	summaryDailyViewers = 50
	// summaryForegroundWindow 是「前台正在回复」的回看窗口（R09）。
	summaryForegroundWindow = 5 * time.Minute
	// summaryMaxRunes 是写入档案/记忆的侧写总长上限。
	summaryMaxRunes = 300
	// summaryTitleRunes 限制侧写标题里的昵称长度。
	summaryTitleRunes = 20
	// summaryImportance/Confidence 让侧写可召回但不压过明确事实。
	summaryImportance          = 0.45
	summaryConfidence          = 0.6
	summaryFallbackConfidence  = 0.4
	summaryMaxInputRunes       = 80
	summaryMaxCatchphraseRunes = 20
	summaryMaxCatchphrases     = 4
	// summaryRetryBase/Max 是失败补偿的指数退避基线/上限。
	summaryRetryBase = 10 * time.Minute
	summaryRetryMax  = 6 * time.Hour
	// summaryMaxFailures 是同一份来源状态的最大连续尝试次数；达到后等新发言再试。
	summaryMaxFailures = 5
	// summaryMaxEvidenceRunes 限制单条证据片段长度。
	summaryMaxEvidenceRunes = 30
)

// 侧写字段名（同时用于面板与记忆证据定位）。
const (
	summaryFieldPersonality  = "性格"
	summaryFieldPreferences  = "偏好"
	summaryFieldCatchphrases = "口头禅"
	summaryFieldRelationship = "关系"
	summaryFieldRecentTopics = "最近话题"
)

// summaryMemoryDerivedModel / Rule 是记忆行的派生来源（R05）。
const (
	summaryMemoryDerivedModel = "model:" + store.ViewerSummaryGenerator
	summaryMemoryDerivedRule  = "rule:stats"
)

// SummaryLLM 是侧写任务需要的最小模型能力（由 provider 适配，测试用 mock）。
type SummaryLLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// SummaryEventSource 提供观众最近公开消息；实现方必须在 SQL 层排除私密与 DM。
type SummaryEventSource interface {
	ViewerRecent(ctx context.Context, fromID string, limit int) ([]event.Event, error)
}

// SummaryStore 是侧写任务需要的档案读写能力。
type SummaryStore interface {
	// ActiveViewers 返回本轮待侧写的档案（新消息量达标/补偿到期/首次回填）。
	ActiveViewers(ctx context.Context, q store.ViewerSummaryQuery) ([]store.ViewerProfile, error)
	// ExpireProfileSummaryRecent 清掉过期近期层并同步记忆与投影任务。
	ExpireProfileSummaryRecent(ctx context.Context, now time.Time) ([]string, error)
	// ForegroundBusy 判断近期是否有前台回复正在生成（后台让路用）。
	ForegroundBusy(ctx context.Context, since time.Time) (bool, error)
	// SummaryAttemptsSince 统计 since 之后尝试过侧写的观众数（日预算）。
	SummaryAttemptsSince(ctx context.Context, since time.Time) (int, error)
	// GetProfile 按 UID 读档案；投影任务据此取最新侧写。
	GetProfile(ctx context.Context, uid string) (store.ViewerProfile, error)
	// SaveProfileSummary 同事务校验来源 CAS，写入档案侧写、记忆记录与投影任务。
	SaveProfileSummary(ctx context.Context, w store.ViewerSummaryWrite) error
	// DeferProfileSummary 保留旧版本，只推进失败与补偿状态。
	DeferProfileSummary(ctx context.Context, d store.ViewerSummaryDefer) error
	// RevokeInvalidSummaries 让来源已失效的侧写立即不可见，并登记清理任务。
	RevokeInvalidSummaries(ctx context.Context) ([]string, error)
	// PendingSummaryJobs 取到期的知识投影任务。
	PendingSummaryJobs(ctx context.Context, now time.Time, limit int) ([]store.ViewerSummaryJob, error)
	// FinishSummaryJob 删除已完成的投影任务。
	FinishSummaryJob(ctx context.Context, uid, action string) error
	// FailSummaryJob 记录投影失败并安排重试。
	FailSummaryJob(ctx context.Context, uid, action string, attempts int, nextRetry time.Time) error
}

// SummaryView 是一份可写入知识库的公开观众侧写。
type SummaryView struct {
	UID        string
	Name       string
	Summary    string
	Stats      string
	Keywords   []string
	UpdatedAt  time.Time
	SourceHash string
	Generator  string
}

// SummaryKnowledge 把侧写写成公开知识条目；nil 表示跳过知识库。
type SummaryKnowledge interface {
	UpsertViewer(ctx context.Context, view SummaryView) error
	// RemoveViewer 删除某位观众的自动知识条目；人工条目不受影响。
	RemoveViewer(ctx context.Context, uid string) error
}

// SummaryOptions 是侧写循环的参数；零值按默认值处理。
type SummaryOptions struct {
	// MinHits 与观众建档门槛一致（默认 2）。
	MinHits int
	// Window 是「近期活跃」回看窗口（默认 24h）。
	Window time.Duration
	// RecentLimit 是喂给模型的最近公开消息条数（默认 20）。
	RecentLimit int
	// MaxPerRun 是单轮最多处理多少位观众（默认 10）。
	MaxPerRun int
	// BackfillLimit 是单轮首次回填名额；0 表示不回填（装配层默认给 2）。
	BackfillLimit int
	// MinNewMessages 是重写已有侧写所需的新增公开消息条数（默认 3）。
	MinNewMessages int
	// RecentTTL 是近期层字段的有效期（默认 7 天）。
	RecentTTL time.Duration
	// DailyViewers 是每天最多侧写多少位观众（默认 50）。
	DailyViewers int
	// Now 可注入时钟（测试用）。
	Now func() time.Time
}

// SummaryStats 是一轮侧写的统计；不包含任何弹幕正文或模型原文。
type SummaryStats struct {
	Candidates int
	Summarized int
	Fallback   int
	Skipped    int
	Failed     int
	Knowledge  int
	// Revoked 是来源失效、侧写被撤回的观众数。
	Revoked int
	// Expired 是近期层过期被清理的观众数。
	Expired int
	// Retained 是模型失败但保留旧版本、只记录补偿的观众数。
	Retained int
	// SourceChanged 是生成后来源被撤回/改密，保存 CAS 拒绝的观众数。
	SourceChanged int
	// BusySkipped/BudgetSkipped 是因前台繁忙或日预算耗尽而跳过的候选数。
	BusySkipped   int
	BudgetSkipped int
	// Projections/Purged 是成功补齐/清理的知识条目数。
	Projections int
	Purged      int
	// ProjectionFailed 是知识投影失败次数（不重新调用模型）。
	ProjectionFailed int
	// PendingJobs 是本轮看到的到期投影任务数。
	PendingJobs int
}

// Summarizer 串行执行「撤回/过期 → 补投影 → 选人 → 模型侧写/统计降级 → 落库」。
type Summarizer struct {
	store     SummaryStore
	events    SummaryEventSource
	llm       SummaryLLM
	knowledge SummaryKnowledge
	opts      SummaryOptions
}

// NewSummarizer 装配侧写器；store 与 events 必填，llm/knowledge 可为 nil。
func NewSummarizer(store SummaryStore, events SummaryEventSource, llm SummaryLLM, knowledge SummaryKnowledge, opts SummaryOptions) (*Summarizer, error) {
	if store == nil || events == nil {
		return nil, errors.New("观众侧写依赖不能为空")
	}
	if opts.MinHits == 0 {
		opts.MinHits = 2
	}
	if opts.MinHits < 1 || opts.MinHits > 100 {
		return nil, errors.New("观众侧写建档门槛必须在1至100之间")
	}
	if opts.Window == 0 {
		opts.Window = summaryActiveWindow
	}
	if opts.Window < time.Hour || opts.Window > 7*24*time.Hour {
		return nil, errors.New("观众侧写回看窗口必须在1小时至7天之间")
	}
	if opts.RecentLimit == 0 {
		opts.RecentLimit = summaryRecentLimit
	}
	if opts.RecentLimit < 1 || opts.RecentLimit > 50 {
		return nil, errors.New("观众侧写最近消息条数必须在1至50之间")
	}
	if opts.MaxPerRun == 0 {
		opts.MaxPerRun = summaryMaxPerRun
	}
	if opts.MaxPerRun < 1 || opts.MaxPerRun > 500 {
		return nil, errors.New("观众侧写单轮人数上限必须在1至500之间")
	}
	if opts.BackfillLimit < 0 || opts.BackfillLimit > 500 {
		return nil, errors.New("观众侧写首次回填上限必须在0至500之间")
	}
	if opts.MinNewMessages == 0 {
		opts.MinNewMessages = summaryMinNewMessages
	}
	if opts.MinNewMessages < 1 || opts.MinNewMessages > 100 {
		return nil, errors.New("观众侧写重写消息门槛必须在1至100之间")
	}
	if opts.RecentTTL == 0 {
		opts.RecentTTL = summaryRecentTTL
	}
	if opts.RecentTTL < time.Hour || opts.RecentTTL > 30*24*time.Hour {
		return nil, errors.New("观众侧写近期层有效期必须在1小时至30天之间")
	}
	if opts.DailyViewers == 0 {
		opts.DailyViewers = summaryDailyViewers
	}
	if opts.DailyViewers < 1 || opts.DailyViewers > 500 {
		return nil, errors.New("观众侧写每日人数上限必须在1至500之间")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Summarizer{store: store, events: events, llm: llm, knowledge: knowledge, opts: opts}, nil
}

// RunOnce 执行一轮侧写。
//
// 单个观众失败只计数并继续；只有「读取候选」失败才让整轮返回错误。
// 有部分失败时仍返回已完成的统计，并把安全错误聚合给调用方记日志。
func (s *Summarizer) RunOnce(ctx context.Context) (SummaryStats, error) {
	var stats SummaryStats
	if ctx == nil {
		return stats, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	now := s.opts.Now().UTC()

	// 撤回优先：来源失效的侧写必须在同一轮先不可见，再处理任何投影/生成。
	revoked, err := s.store.RevokeInvalidSummaries(ctx)
	if err != nil {
		return stats, fmt.Errorf("撤回失效观众侧写失败：%w", err)
	}
	stats.Revoked = len(revoked)

	// 过期清理只让近期层失效，稳定自述保留；之后的投影会把新文本同步到知识库。
	expired, err := s.store.ExpireProfileSummaryRecent(ctx, now)
	if err != nil {
		return stats, fmt.Errorf("清理过期观众侧写失败：%w", err)
	}
	stats.Expired = len(expired)

	// 投影补投只读写数据库与知识文件，不调用模型；失败留待下轮，不阻塞生成。
	var failures []error
	if err := s.runProjections(ctx, now, &stats); err != nil {
		failures = append(failures, err)
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}

	candidates, err := s.store.ActiveViewers(ctx, store.ViewerSummaryQuery{
		Since:          now.Add(-s.opts.Window),
		Now:            now,
		MinHits:        s.opts.MinHits,
		MinNewMessages: s.opts.MinNewMessages,
		Limit:          s.opts.MaxPerRun,
		BackfillLimit:  s.opts.BackfillLimit,
	})
	if err != nil {
		return stats, fmt.Errorf("读取待侧写观众失败：%w", err)
	}
	stats.Candidates = len(candidates)
	if len(candidates) == 0 {
		return stats, errors.Join(failures...)
	}

	// 日预算按「今天尝试过的观众数」计数；只剩多少名额就最多处理多少位。
	budget := s.opts.DailyViewers
	if used, err := s.store.SummaryAttemptsSince(ctx, summaryDayStart(now)); err != nil {
		failures = append(failures, fmt.Errorf("读取观众侧写日预算失败：%w", err))
		budget = 0
	} else if used >= budget {
		budget = 0
	} else {
		budget -= used
	}

	for i, profile := range candidates {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if budget < 1 {
			stats.BudgetSkipped = len(candidates) - i
			break
		}
		// 后台让路：前台回复正在进行时，本轮剩余候选全部推迟（R09）。
		busy, err := s.store.ForegroundBusy(ctx, now.Add(-summaryForegroundWindow))
		if err != nil {
			failures = append(failures, fmt.Errorf("检查前台回复忙碌状态失败：%w", err))
			stats.BusySkipped = len(candidates) - i
			break
		}
		if busy {
			stats.BusySkipped = len(candidates) - i
			break
		}
		budget--
		if err := s.summarizeOne(ctx, profile, now, &stats); err != nil {
			stats.Failed++
			failures = append(failures, err)
		}
	}
	return stats, errors.Join(failures...)
}

// summaryDayStart 返回 UTC 当日零点；日预算按 UTC 日切分，便于与库内时间直接比较。
func summaryDayStart(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// runProjections 处理到期的知识投影任务；失败只推迟重试，不重新生成侧写。
func (s *Summarizer) runProjections(ctx context.Context, now time.Time, stats *SummaryStats) error {
	if s.knowledge == nil {
		return nil
	}
	jobs, err := s.store.PendingSummaryJobs(ctx, now, s.opts.MaxPerRun)
	if err != nil {
		return fmt.Errorf("读取观众侧写投影任务失败：%w", err)
	}
	stats.PendingJobs = len(jobs)
	var failures []error
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := job.Validate(); err != nil {
			failures = append(failures, fmt.Errorf("投影任务无效：%w", err))
			continue
		}
		var jobErr error
		switch job.Action {
		case store.ViewerSummaryActionPurge:
			jobErr = s.knowledge.RemoveViewer(ctx, job.UID)
			if jobErr == nil {
				stats.Purged++
			}
		case store.ViewerSummaryActionPublish:
			profile, loadErr := s.store.GetProfile(ctx, job.UID)
			switch {
			case errors.Is(loadErr, store.ErrViewerNotFound):
				jobErr = s.knowledge.RemoveViewer(ctx, job.UID)
				if jobErr == nil {
					stats.Purged++
				}
			case loadErr != nil:
				jobErr = loadErr
			case strings.TrimSpace(profile.ProfileSummary) == "":
				// 侧写已被撤回：旧任务必须转成清理，不能把过期内容写回。
				jobErr = s.knowledge.RemoveViewer(ctx, job.UID)
				if jobErr == nil {
					stats.Purged++
				}
			default:
				jobErr = s.knowledge.UpsertViewer(ctx, summaryViewFromProfile(profile))
				if jobErr == nil {
					stats.Projections++
					stats.Knowledge++
				}
			}
		}
		if jobErr != nil {
			stats.ProjectionFailed++
			failures = append(failures, fmt.Errorf("观众侧写投影失败：%w", jobErr))
			attempts := job.Attempts + 1
			if err := s.store.FailSummaryJob(ctx, job.UID, job.Action, attempts, now.Add(summaryRetryDelay(attempts))); err != nil {
				failures = append(failures, fmt.Errorf("记录观众侧写投影失败：%w", err))
			}
			continue
		}
		if err := s.store.FinishSummaryJob(ctx, job.UID, job.Action); err != nil {
			failures = append(failures, fmt.Errorf("完成观众侧写投影任务失败：%w", err))
		}
	}
	return errors.Join(failures...)
}

// summarizeOne 侧写一位观众；失败只影响这一位。
func (s *Summarizer) summarizeOne(ctx context.Context, profile store.ViewerProfile, now time.Time, stats *SummaryStats) error {
	msgs, err := s.events.ViewerRecent(ctx, profile.UID, s.opts.RecentLimit)
	if err != nil {
		return fmt.Errorf("读取观众最近发言失败：%w", err)
	}
	if len(msgs) == 0 {
		stats.Skipped++
		return nil
	}
	sources := summarySources(msgs)
	sourceHash := store.ViewerSummarySourceHash(profile.UID, store.ViewerSummaryGenerator, sources)
	result := s.buildSummary(ctx, profile, msgs, now)
	at := now.UTC()

	// 已有内容时失败不降级覆盖：保留好版本（或首次降级版），只记补偿状态。
	if result.fallback && strings.TrimSpace(profile.ProfileSummary) != "" {
		failures := profile.SummaryFailures + 1
		var nextRetry time.Time
		if result.retryable {
			nextRetry = summaryNextRetry(at, failures)
		}
		if err := s.store.DeferProfileSummary(ctx, store.ViewerSummaryDefer{
			UID: profile.UID, At: at, Failures: failures, NextRetryAt: nextRetry,
		}); err != nil {
			return fmt.Errorf("记录观众侧写失败状态：%w", err)
		}
		stats.Retained++
		return nil
	}

	failures := 0
	var nextRetry time.Time
	if result.fallback {
		failures = 1
		if result.retryable {
			nextRetry = summaryNextRetry(at, failures)
		}
	}
	record := summaryMemoryRecord(profile, result.text, at, msgs, result.fields, result.fallback)
	err = s.store.SaveProfileSummary(ctx, store.ViewerSummaryWrite{
		UID: profile.UID, Summary: result.text, At: at, Record: record,
		SourceHash: sourceHash, Sources: sources, Generator: store.ViewerSummaryGenerator,
		Keywords: result.keywords, Stable: result.stable, Recent: result.recent,
		RecentUntil: result.recentUntil, Fields: result.fields,
		Fallback: result.fallback, Failures: failures, NextRetryAt: nextRetry,
	})
	if err != nil {
		// 来源在读取与保存之间被撤回/改密：放弃这次派生结果，不重试、不覆盖。
		if errors.Is(err, store.ErrViewerSourceChanged) {
			stats.SourceChanged++
			return nil
		}
		return fmt.Errorf("保存观众侧写失败：%w", err)
	}
	if result.fallback {
		stats.Fallback++
	} else {
		stats.Summarized++
	}
	if s.knowledge != nil {
		if err := s.knowledge.UpsertViewer(ctx, SummaryView{
			UID: profile.UID, Name: profile.Name, Summary: result.text, Stats: result.statsText,
			Keywords: result.keywords, UpdatedAt: at, SourceHash: sourceHash, Generator: store.ViewerSummaryGenerator,
		}); err != nil {
			// 知识失败不回滚档案：投影任务留在库里，下轮只补投影不重付费。
			stats.ProjectionFailed++
			return nil
		}
		stats.Knowledge++
		if err := s.store.FinishSummaryJob(ctx, profile.UID, store.ViewerSummaryActionPublish); err != nil {
			return fmt.Errorf("完成观众知识投影任务失败：%w", err)
		}
	}
	return nil
}

// summarySources 把最近发言转成来源快照；顺序与模型输入一致。
func summarySources(msgs []event.Event) []store.ViewerSummarySource {
	out := make([]store.ViewerSummarySource, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, store.ViewerSummarySource{ID: msg.ID, Text: msg.Text, At: msg.At})
	}
	return out
}

// summaryViewFromProfile 用档案里的最新侧写补投影，不重新生成。
func summaryViewFromProfile(profile store.ViewerProfile) SummaryView {
	statsText := fmt.Sprintf("说过 %d 次 / 鱼回过 %d 次 / 最后出现 %s",
		profile.Hits, profile.Replies, profile.LastSeen.Format("2006-01-02"))
	return SummaryView{
		UID: profile.UID, Name: profile.Name, Summary: profile.ProfileSummary,
		Stats: statsText, Keywords: profile.SummaryKeywords,
		UpdatedAt: profile.SummaryUpdatedAt, SourceHash: profile.SummarySourceHash,
		Generator: profile.SummaryGenerator,
	}
}

// summaryRetryDelay 是指数退避；超过上限后固定为上限。
func summaryRetryDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	shift := failures - 1
	if shift > 6 {
		shift = 6
	}
	delay := summaryRetryBase << shift
	if delay <= 0 || delay > summaryRetryMax {
		return summaryRetryMax
	}
	return delay
}

// summaryNextRetry 返回下一次补偿时间；达到尝试上限返回零值，等新发言再试。
func summaryNextRetry(now time.Time, failures int) time.Time {
	if failures >= summaryMaxFailures {
		return time.Time{}
	}
	return now.Add(summaryRetryDelay(failures))
}

// summaryResult 是一次生成结果：文本、分层字段与降级状态。
type summaryResult struct {
	text        string
	statsText   string
	stable      string
	recent      string
	recentUntil time.Time
	fields      []store.ViewerSummaryField
	keywords    []string
	fallback    bool
	retryable   bool
}

// buildSummary 优先走模型；未配置、调用失败或输出无效时退回统计聚合。
// retryable 表示降级是否由可恢复的模型/输出问题导致：未配置模型时重试没有意义。
func (s *Summarizer) buildSummary(ctx context.Context, profile store.ViewerProfile, msgs []event.Event, now time.Time) summaryResult {
	statsText := fmt.Sprintf("说过 %d 次 / 鱼回过 %d 次 / 最后出现 %s",
		profile.Hits, profile.Replies, profile.LastSeen.Format("2006-01-02"))
	fallback := func(retryable bool) summaryResult {
		return summaryResult{
			text:      fallbackSummary(msgs, statsText),
			statsText: statsText,
			keywords:  []string{profile.Name},
			fallback:  true,
			retryable: retryable,
		}
	}
	if s.llm == nil {
		return fallback(false)
	}
	callCtx := llmmeta.WithCall(ctx, llmmeta.Call{
		Purpose:       llmmeta.PurposeViewerSummary,
		CorrelationID: "viewer:" + profile.UID,
	})
	raw, err := s.llm.Complete(callCtx, summaryPrompt, buildSummaryInput(profile, msgs))
	if err != nil {
		return fallback(true)
	}
	reply, err := parseSummaryReply(raw)
	if err != nil {
		return fallback(true)
	}
	fields := mergeSummaryFields(profile.SummaryFields, reply, now.UTC(), s.opts.RecentTTL)
	if len(fields) == 0 {
		return fallback(true)
	}
	coverage := fmt.Sprintf("依据最近 %d 条公开发言，截至 %s", len(msgs), now.UTC().Format("2006-01-02 15:04"))
	summary, stable, recent, recentUntil := composeSummary(fields, coverage)
	keywords := append([]string{profile.Name}, reply.catchphraseKeywords()...)
	return summaryResult{
		text: summary, statsText: statsText, stable: stable, recent: recent,
		recentUntil: recentUntil, fields: fields, keywords: keywords,
	}
}

// summaryPrompt 只要求一行 JSON；身份与隐私判断交给输入侧（只喂公开弹幕）。
// 每个字段带 basis 与 evidence：模型必须区分自述/观察/推断，不能把猜测写成自述（R05）。
const summaryPrompt = `你是直播间的观众档案整理员。根据观众最近公开发言，更新一份供主播聊天时认人用的简短侧写。
只输出一行 JSON，不要多余文字。每个字段是对象：
{"personality":{"value":"性格，不超过40字","basis":"self|observed|inferred","evidence":"观众原话片段，不超过30字"},"preferences":{"value":"偏好，不超过40字","basis":"...","evidence":"..."},"catchphrases":{"value":"口头禅，不超过40字","basis":"...","evidence":"..."},"relationship":{"value":"和本鱼的关系/互动习惯，不超过40字","basis":"...","evidence":"..."},"recent_topics":{"value":"最近聊的话题，不超过60字","basis":"...","evidence":"..."}}
basis 规则：
- self：观众原话里明确的自述（如「我是学生」「我喜欢吃白饭」）
- observed：发言中可直接观察到的互动事实（如反复问同一类问题）
- inferred：你根据语气/内容做的推测
只依据给出的发言；不编造现实身份、住址、联系方式等隐私，也不要把主播自己的话当成观众特点。
recent_topics 只放最近几天聊的事；信息不足的字段输出空串或省略。evidence 只摘录观众原话，没有就留空串。`

// buildSummaryInput 把公开弹幕压成模型输入；旧→新便于理解话题走向。
func buildSummaryInput(profile store.ViewerProfile, msgs []event.Event) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "观众昵称：%s\n", clipSummaryRunes(strings.TrimSpace(profile.Name), summaryTitleRunes))
	fmt.Fprintf(&sb, "统计：说过 %d 次 / 鱼回过 %d 次 / 最后出现 %s\n",
		profile.Hits, profile.Replies, profile.LastSeen.Format("2006-01-02"))
	// 稳定自述给模型当上下文，避免它把已确认的长期偏好当成这轮的新观察。
	if strings.TrimSpace(profile.SummaryStable) != "" {
		fmt.Fprintf(&sb, "已有稳定自述：%s\n", clipSummaryRunes(profile.SummaryStable, summaryMaxRunes))
	}
	sb.WriteString("最近公开发言（旧→新）：\n")
	for i := len(msgs) - 1; i >= 0; i-- {
		text := strings.TrimSpace(msgs[i].Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "- %s\n", clipSummaryRunes(text, summaryMaxInputRunes))
	}
	return sb.String()
}

// summaryReplyField 是模型输出的单字段：值、依据类型与证据。
type summaryReplyField struct {
	Value    string
	Basis    string
	Evidence string
}

// summaryReply 是清洗后的五字段输出。
type summaryReply struct {
	Personality  summaryReplyField
	Preferences  summaryReplyField
	Catchphrases summaryReplyField
	Relationship summaryReplyField
	RecentTopics summaryReplyField
}

// parseSummaryReply 容忍代码块包裹与旧的纯字符串格式；
// 所有字段清洗后为空视为输出无效。旧格式按 inferred 处理，不冒充自述。
func parseSummaryReply(raw string) (summaryReply, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return summaryReply{}, errors.New("模型输出为空")
	}
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimPrefix(text, "json")
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			text = text[start : end+1]
		}
	}
	var objects map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &objects); err != nil {
		return summaryReply{}, errors.New("模型输出不是JSON")
	}
	parse := func(key string, limit int) summaryReplyField {
		field, err := parseSummaryField(objects[key], limit)
		if err != nil {
			return summaryReplyField{}
		}
		return field
	}
	reply := summaryReply{
		Personality:  parse("personality", 40),
		Preferences:  parse("preferences", 40),
		Catchphrases: parse("catchphrases", 40),
		Relationship: parse("relationship", 40),
		RecentTopics: parse("recent_topics", 60),
	}
	if reply.empty() {
		return summaryReply{}, errors.New("模型输出的侧写为空")
	}
	return reply, nil
}

// parseSummaryField 兼容 `"value"` 与 `{"value":...}` 两种形状。
func parseSummaryField(raw json.RawMessage, limit int) (summaryReplyField, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return summaryReplyField{}, nil
	}
	switch raw[0] {
	case '"':
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return summaryReplyField{}, err
		}
		return summaryReplyField{Value: cleanSummaryField(value, limit), Basis: store.ViewerSummaryBasisInferred}, nil
	case '{':
		var obj struct {
			Value    string `json:"value"`
			Basis    string `json:"basis"`
			Evidence string `json:"evidence"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return summaryReplyField{}, err
		}
		basis := strings.ToLower(strings.TrimSpace(obj.Basis))
		switch basis {
		case store.ViewerSummaryBasisSelf, store.ViewerSummaryBasisObserved, store.ViewerSummaryBasisInferred:
		default:
			basis = store.ViewerSummaryBasisInferred
		}
		return summaryReplyField{
			Value:    cleanSummaryField(obj.Value, limit),
			Basis:    basis,
			Evidence: cleanSummaryField(obj.Evidence, summaryMaxEvidenceRunes),
		}, nil
	}
	return summaryReplyField{}, errors.New("字段形状无效")
}

// empty 判断五个字段是否全空。
func (r summaryReply) empty() bool {
	return r.Personality.Value == "" && r.Preferences.Value == "" && r.Catchphrases.Value == "" &&
		r.Relationship.Value == "" && r.RecentTopics.Value == ""
}

// catchphraseKeywords 把口头禅拆成检索关键词；无有效词时返回空。
func (r summaryReply) catchphraseKeywords() []string {
	value := r.Catchphrases.Value
	if value == "" {
		return nil
	}
	words := strings.FieldsFunc(value, func(c rune) bool {
		return unicode.IsSpace(c) || strings.ContainsRune("、，,/；;｜|", c)
	})
	out := make([]string, 0, summaryMaxCatchphrases)
	seen := make(map[string]bool, len(words))
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word == "" || utf8.RuneCountInString(word) > summaryMaxCatchphraseRunes || seen[word] {
			continue
		}
		seen[word] = true
		out = append(out, word)
		if len(out) == summaryMaxCatchphrases {
			break
		}
	}
	return out
}

// summaryReplyItems 按固定顺序把五字段列出来，并标记哪些字段可进入稳定层。
func (r summaryReply) items() []struct {
	name   string
	field  summaryReplyField
	stable bool
} {
	return []struct {
		name   string
		field  summaryReplyField
		stable bool
	}{
		{summaryFieldPersonality, r.Personality, true},
		{summaryFieldPreferences, r.Preferences, true},
		{summaryFieldCatchphrases, r.Catchphrases, true},
		{summaryFieldRelationship, r.Relationship, true},
		// 最近话题天然是近期层，即使模型标了 self 也不长期保留。
		{summaryFieldRecentTopics, r.RecentTopics, false},
	}
}

// mergeSummaryFields 按新证据修订字段（R06）：
//   - 明确自述（basis=self）进入稳定层；同名旧稳定字段被新自述覆盖；
//   - 观察/推断进入近期层，带 TTL；
//   - 本轮没提到的旧稳定字段原样保留，不会被「重写」掉；
//   - 旧稳定字段与本轮非自述观察同名时，稳定自述优先，避免一轮情绪覆盖长期偏好。
func mergeSummaryFields(previous []store.ViewerSummaryField, reply summaryReply, now time.Time, ttl time.Duration) []store.ViewerSummaryField {
	previousStable := make(map[string]store.ViewerSummaryField, len(previous))
	for _, field := range previous {
		if field.Layer == store.ViewerSummaryLayerStable {
			previousStable[field.Name] = field
		}
	}
	items := reply.items()
	replied := make(map[string]bool, len(items))
	stable := make([]store.ViewerSummaryField, 0, len(items))
	recent := make([]store.ViewerSummaryField, 0, len(items))
	for _, item := range items {
		if item.field.Value == "" {
			continue
		}
		replied[item.name] = true
		if item.stable && item.field.Basis == store.ViewerSummaryBasisSelf {
			stable = append(stable, store.ViewerSummaryField{
				Name: item.name, Value: item.field.Value, Basis: item.field.Basis,
				Evidence: item.field.Evidence, Layer: store.ViewerSummaryLayerStable, UpdatedAt: now,
			})
			continue
		}
		// 稳定自述优先：本轮同名观察不覆盖已经确认的自述。
		if old, ok := previousStable[item.name]; ok {
			stable = append(stable, old)
			continue
		}
		recent = append(recent, store.ViewerSummaryField{
			Name: item.name, Value: item.field.Value, Basis: item.field.Basis,
			Evidence: item.field.Evidence, Layer: store.ViewerSummaryLayerRecent,
			UpdatedAt: now, ExpiresAt: now.Add(ttl),
		})
	}
	// 本轮没提到的旧稳定字段继续保留（修订而非重写）。
	for _, item := range items {
		if replied[item.name] {
			continue
		}
		if old, ok := previousStable[item.name]; ok {
			stable = append(stable, old)
		}
	}
	return append(stable, recent...)
}

// composeSummary 把分层字段拼成档案文本；stable/recent 单独返回供过期清理使用。
func composeSummary(fields []store.ViewerSummaryField, coverage string) (summary, stable, recent string, recentUntil time.Time) {
	stableParts := make([]string, 0, len(fields))
	recentParts := make([]string, 0, len(fields))
	for _, field := range fields {
		part := field.Name + "：" + field.Value + "（" + summaryBasisLabel(field.Basis) + "）"
		switch field.Layer {
		case store.ViewerSummaryLayerStable:
			stableParts = append(stableParts, part)
		case store.ViewerSummaryLayerRecent:
			recentParts = append(recentParts, part)
			if field.ExpiresAt.After(recentUntil) {
				recentUntil = field.ExpiresAt
			}
		}
	}
	stable = strings.Join(stableParts, "；")
	if len(recentParts) > 0 {
		date := recentUntil.UTC().Format("2006-01-02")
		recent = "近期观察，仅参考：" + strings.Join(recentParts, "；") + "（有效期至 " + date + "）"
	}
	parts := make([]string, 0, 3)
	if stable != "" {
		parts = append(parts, stable)
	}
	if recent != "" {
		parts = append(parts, recent)
	}
	if coverage != "" {
		parts = append(parts, coverage)
	}
	summary = clipSummaryRunes(strings.Join(parts, "；"), summaryMaxRunes)
	return summary, stable, recent, recentUntil
}

// summaryBasisLabel 把依据类型转成注入文本里的标签。
func summaryBasisLabel(basis string) string {
	switch basis {
	case store.ViewerSummaryBasisSelf:
		return "自述"
	case store.ViewerSummaryBasisObserved:
		return "观察"
	default:
		return "推断"
	}
}

// fallbackSummary 是旧家效果：统计 + 最近几条公开发言，明确标注模型未生成侧写。
func fallbackSummary(msgs []event.Event, statsText string) string {
	var sb strings.Builder
	sb.WriteString("（模型未生成侧写，仅统计）")
	sb.WriteString(statsText)
	recent := make([]string, 0, 3)
	for _, msg := range msgs {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
		}
		recent = append(recent, clipSummaryRunes(text, 30))
		if len(recent) == 3 {
			break
		}
	}
	if len(recent) > 0 {
		sb.WriteString("；最近说过：")
		sb.WriteString(strings.Join(recent, " / "))
	}
	return clipSummaryRunes(sb.String(), summaryMaxRunes)
}

// summaryMemoryRecord 生成可召回的记忆行；ID 稳定，同一观众只保留最新侧写。
//
// R05：侧写是模型/规则派生内容——subject=观众、derived_by 标明模型或规则，
// asserted_by 留空（只有明确自述才能写成观众本人断言）；
// 每字段的证据与依据类型写进 evidence，供面板与检索核对。
func summaryMemoryRecord(profile store.ViewerProfile, text string, at time.Time, msgs []event.Event, fields []store.ViewerSummaryField, fallback bool) store.MemoryRecord {
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		if strings.TrimSpace(msg.ID) != "" {
			ids = append(ids, msg.ID)
		}
	}
	confidence := summaryConfidence
	derivedBy := summaryMemoryDerivedModel
	if fallback {
		confidence = summaryFallbackConfidence
		derivedBy = summaryMemoryDerivedRule
	}
	evidence := make([]store.MemoryEvidence, 0, len(fields))
	for _, field := range fields {
		if strings.TrimSpace(field.Evidence) == "" {
			continue
		}
		evidence = append(evidence, store.MemoryEvidence{
			Field: field.Name, Type: field.Basis, Text: field.Evidence,
		})
	}
	return store.MemoryRecord{
		Record: memory.Record{
			ID:         "viewer-profile:" + profile.UID,
			Summary:    text,
			Importance: summaryImportance,
			Confidence: confidence,
			Private:    false,
			EventIDs:   ids,
			ValidFrom:  at,
		},
		Source:    "content",
		Kind:      "viewer_profile",
		Title:     "观众侧写 · " + clipSummaryRunes(profile.Name, summaryTitleRunes),
		FromID:    profile.UID,
		FromName:  profile.Name,
		Subject:   memory.SoulSubjectViewer,
		DerivedBy: derivedBy,
		Evidence:  evidence,
	}
}

// cleanSummaryField 去掉换行/控制字符并折叠空白，再按 rune 截断。
func cleanSummaryField(raw string, limit int) string {
	var sb strings.Builder
	space := false
	for _, r := range strings.TrimSpace(raw) {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = true
			continue
		}
		if space && sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		space = false
		sb.WriteRune(r)
	}
	return clipSummaryRunes(sb.String(), limit)
}

func clipSummaryRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
