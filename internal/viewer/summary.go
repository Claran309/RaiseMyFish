package viewer

// 本文件实现观众侧写的定时总结（#63/#64）。
//
// 输入只有查询层已排除私密/DM 的公开弹幕；模型失败、输出损坏或未配置时
// 回落到统计聚合（旧家效果），不阻塞其他观众，也不影响采集/消费主链路。
//
// 落库分三处：
//   - viewer_profiles.profile_summary：注入 prompt 的简短侧写；
//   - memory_records（kind=viewer_profile）：可被记忆检索召回；
//   - knowledge（viewers.md）：公开条目，可被 RAG 召回。
//
// 模型调用通过 llmmeta 标注用途与安全关联 ID；正文/昵称不进错误与账本。

import (
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
	// summaryMaxPerRun 是单轮最多侧写多少位观众，控制模型费用。
	summaryMaxPerRun = 100
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
	// ActiveViewers 返回本轮待侧写的档案（近期活跃、有新发言或补偿重试到期）。
	ActiveViewers(ctx context.Context, since, now time.Time, minHits, limit int) ([]store.ViewerProfile, error)
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
	// MaxPerRun 是单轮最多处理多少位观众（默认 100）。
	MaxPerRun int
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
	// Retained 是模型失败但保留旧版本、只记录补偿的观众数。
	Retained int
	// SourceChanged 是生成后来源被撤回/改密，保存 CAS 拒绝的观众数。
	SourceChanged int
	// Projections/Purged 是成功补齐/清理的知识条目数。
	Projections int
	Purged      int
	// ProjectionFailed 是知识投影失败次数（不重新调用模型）。
	ProjectionFailed int
	// PendingJobs 是本轮看到的到期投影任务数。
	PendingJobs int
}

// Summarizer 串行执行「选人 → 取最近公开弹幕 → 模型侧写/统计降级 → 落库」。
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

	// 投影补投只读写数据库与知识文件，不调用模型；失败留待下轮，不阻塞生成。
	var failures []error
	if err := s.runProjections(ctx, now, &stats); err != nil {
		failures = append(failures, err)
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}

	candidates, err := s.store.ActiveViewers(ctx, now.Add(-s.opts.Window), now, s.opts.MinHits, s.opts.MaxPerRun)
	if err != nil {
		return stats, fmt.Errorf("读取待侧写观众失败：%w", err)
	}
	stats.Candidates = len(candidates)
	for _, profile := range candidates {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if err := s.summarizeOne(ctx, profile, now, &stats); err != nil {
			stats.Failed++
			failures = append(failures, err)
		}
	}
	return stats, errors.Join(failures...)
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
	text, statsText, keywords, fallback, retryable := s.buildSummary(ctx, profile, msgs)
	at := now.UTC()

	// 已有内容时模型失败不降级覆盖：保留好版本（或首次降级版），只记补偿状态。
	if fallback && retryable && strings.TrimSpace(profile.ProfileSummary) != "" {
		failures := profile.SummaryFailures + 1
		if err := s.store.DeferProfileSummary(ctx, store.ViewerSummaryDefer{
			UID: profile.UID, At: at, Failures: failures,
			NextRetryAt: summaryNextRetry(at, failures),
		}); err != nil {
			return fmt.Errorf("记录观众侧写失败状态：%w", err)
		}
		stats.Retained++
		return nil
	}

	failures := 0
	var nextRetry time.Time
	if fallback {
		failures = 1
		if retryable {
			nextRetry = summaryNextRetry(at, failures)
		}
	}
	record := summaryMemoryRecord(profile, text, at, msgs, fallback)
	err = s.store.SaveProfileSummary(ctx, store.ViewerSummaryWrite{
		UID: profile.UID, Summary: text, At: at, Record: record,
		SourceHash: sourceHash, Sources: sources, Generator: store.ViewerSummaryGenerator,
		Keywords: keywords, Fallback: fallback, Failures: failures, NextRetryAt: nextRetry,
	})
	if err != nil {
		// 来源在读取与保存之间被撤回/改密：放弃这次派生结果，不重试、不覆盖。
		if errors.Is(err, store.ErrViewerSourceChanged) {
			stats.SourceChanged++
			return nil
		}
		return fmt.Errorf("保存观众侧写失败：%w", err)
	}
	if fallback {
		stats.Fallback++
	} else {
		stats.Summarized++
	}
	if s.knowledge != nil {
		if err := s.knowledge.UpsertViewer(ctx, SummaryView{
			UID: profile.UID, Name: profile.Name, Summary: text, Stats: statsText,
			Keywords: keywords, UpdatedAt: at, SourceHash: sourceHash, Generator: store.ViewerSummaryGenerator,
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

// buildSummary 优先走模型；未配置、调用失败或输出无效时退回统计聚合。
// retryable 表示降级是否由可恢复的模型/输出问题导致：未配置模型时重试没有意义。
func (s *Summarizer) buildSummary(ctx context.Context, profile store.ViewerProfile, msgs []event.Event) (text, statsText string, keywords []string, fallback, retryable bool) {
	statsText = fmt.Sprintf("说过 %d 次 / 鱼回过 %d 次 / 最后出现 %s",
		profile.Hits, profile.Replies, profile.LastSeen.Format("2006-01-02"))
	if s.llm == nil {
		return fallbackSummary(msgs, statsText), statsText, []string{profile.Name}, true, false
	}
	callCtx := llmmeta.WithCall(ctx, llmmeta.Call{
		Purpose:       llmmeta.PurposeViewerSummary,
		CorrelationID: "viewer:" + profile.UID,
	})
	raw, err := s.llm.Complete(callCtx, summaryPrompt, buildSummaryInput(profile, msgs))
	if err != nil {
		return fallbackSummary(msgs, statsText), statsText, []string{profile.Name}, true, true
	}
	content, err := parseSummaryReply(raw)
	if err != nil {
		return fallbackSummary(msgs, statsText), statsText, []string{profile.Name}, true, true
	}
	text = content.text()
	if text == "" {
		return fallbackSummary(msgs, statsText), statsText, []string{profile.Name}, true, true
	}
	return text, statsText, append([]string{profile.Name}, content.catchphraseKeywords()...), false, false
}

// summaryPrompt 只要求一行 JSON；身份与隐私判断交给输入侧（只喂公开弹幕）。
const summaryPrompt = `你是直播间的观众档案整理员。根据观众最近公开发言，写一份供主播聊天时认人用的简短侧写。
只输出一行 JSON，不要多余文字：
{"personality":"性格，不超过40字","preferences":"偏好/感兴趣的话题，不超过40字","catchphrases":"口头禅或常用语，没有就空串","relationship":"和本鱼的关系/互动习惯，不超过40字","recent_topics":"最近聊的话题，不超过60字"}
只依据给出的发言；不编造现实身份、住址、联系方式等隐私，也不要把主播自己的话当成观众特点。信息不足的字段输出空串。`

// buildSummaryInput 把公开弹幕压成模型输入；旧→新便于理解话题走向。
func buildSummaryInput(profile store.ViewerProfile, msgs []event.Event) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "观众昵称：%s\n", clipSummaryRunes(strings.TrimSpace(profile.Name), summaryTitleRunes))
	fmt.Fprintf(&sb, "统计：说过 %d 次 / 鱼回过 %d 次 / 最后出现 %s\n",
		profile.Hits, profile.Replies, profile.LastSeen.Format("2006-01-02"))
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

type summaryReply struct {
	Personality  string `json:"personality"`
	Preferences  string `json:"preferences"`
	Catchphrases string `json:"catchphrases"`
	Relationship string `json:"relationship"`
	RecentTopics string `json:"recent_topics"`
}

// parseSummaryReply 容忍代码块包裹；所有字段清洗后为空视为输出无效。
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
	var reply summaryReply
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		return summaryReply{}, errors.New("模型输出不是JSON")
	}
	reply.Personality = cleanSummaryField(reply.Personality, 40)
	reply.Preferences = cleanSummaryField(reply.Preferences, 40)
	reply.Catchphrases = cleanSummaryField(reply.Catchphrases, 40)
	reply.Relationship = cleanSummaryField(reply.Relationship, 40)
	reply.RecentTopics = cleanSummaryField(reply.RecentTopics, 60)
	if reply.text() == "" {
		return summaryReply{}, errors.New("模型输出的侧写为空")
	}
	return reply, nil
}

// text 把五字段拼成注入/检索用的一行；空字段不占位。
func (r summaryReply) text() string {
	parts := make([]string, 0, 5)
	add := func(label, value string) {
		if value != "" {
			parts = append(parts, label+"："+value)
		}
	}
	add("性格", r.Personality)
	add("偏好", r.Preferences)
	add("口头禅", r.Catchphrases)
	add("关系", r.Relationship)
	add("最近话题", r.RecentTopics)
	return clipSummaryRunes(strings.Join(parts, "；"), summaryMaxRunes)
}

// catchphraseKeywords 把口头禅拆成检索关键词；无有效词时返回空。
func (r summaryReply) catchphraseKeywords() []string {
	if r.Catchphrases == "" {
		return nil
	}
	words := strings.FieldsFunc(r.Catchphrases, func(c rune) bool {
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
func summaryMemoryRecord(profile store.ViewerProfile, text string, at time.Time, msgs []event.Event, fallback bool) store.MemoryRecord {
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		if strings.TrimSpace(msg.ID) != "" {
			ids = append(ids, msg.ID)
		}
	}
	confidence := summaryConfidence
	if fallback {
		confidence = summaryFallbackConfidence
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
		Source:         "content",
		Kind:           "viewer_profile",
		Title:          "观众侧写 · " + clipSummaryRunes(profile.Name, summaryTitleRunes),
		FromID:         profile.UID,
		FromName:       profile.Name,
		AssertedByID:   profile.UID,
		AssertedByName: profile.Name,
		Subject:        memory.SoulSubjectViewer,
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
