package extractworker

// 本文件实现 #93 的重复度触发：窗口内同一实体被反复提及 → 提升为一条固定称呼事实。
//
// 边界：
//   - 只读公开入向事件；私密会话永远不参与称呼统计。
//   - 事实必须带 evidence（次数 + 代表事件 ID），无证据不写。
//   - 同一实体 24 小时最多触发一次；被主人纠正的实体只降权、不再写。
//   - 子任务失败只记 stats，不影响提炼主流程。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

const (
	// repeatDefaultWindow 是统计窗口：7 天。
	repeatDefaultWindow = memory.RepeatWindowDays * 24 * time.Hour
	// repeatDefaultDebounce 是同实体最短触发间隔。
	repeatDefaultDebounce = 24 * time.Hour
	// repeatDefaultMaxEvents / repeatMaxEvents 是窗口事件读取上限。
	repeatDefaultMaxEvents = 2000
	repeatMaxEvents        = 2000
	// repeatDefaultMaxNames / repeatMaxNames 是观众昵称读取上限。
	repeatDefaultMaxNames = 200
	repeatMaxNames        = 500
	// repeatFactImportance 是重复度事实的起步重要度：低于普通事实（0.6），
	// 只作为称呼线索，不该压过明确自述。
	repeatFactImportance = 0.5
	repeatFactConfidence = 0.8
	// repeatCorrectedImportance / repeatCorrectedConfidence 是主人纠正后的降权值。
	repeatCorrectedImportance = 0.2
	repeatCorrectedConfidence = 0.5
	// repeatSource 复用既有 content 来源：称呼事实来自公开内容本身。
	repeatSource = "content"
	// repeatFactIDPrefix 是重复度事实的主键前缀，与事件提炼事实区分。
	repeatFactIDPrefix = "fact:public:repeat:"
	// repeatDerivedBy 标注派生规则版本，面板可据此筛选。
	repeatDerivedBy = "rule:repeat/v1"
	// repeatGlobalSession 让事实在共享事实查询里可见：查询会丢弃空 channel/session。
	repeatGlobalSession = "global"
)

// RepeatEventSource 提供窗口内的公开入向事件（database.EventRepo 满足）。
type RepeatEventSource interface {
	PublicIncomingSince(ctx context.Context, since time.Time, limit int) ([]event.Event, error)
}

// RepeatAliasSource 提供人工确认的别名快照（由装配层适配 viewer.AliasSource）。
type RepeatAliasSource interface {
	ConfirmedAliases() []memory.RepeatAlias
}

// RepeatNameSource 提供窗口内活跃观众的昵称（database.ViewerRepo 满足）。
type RepeatNameSource interface {
	RepeatViewerNames(ctx context.Context, since time.Time, limit int) ([]store.ViewerName, error)
}

// RepeatPromotion 是重复度触发子任务的依赖与参数。
type RepeatPromotion struct {
	Events  RepeatEventSource
	Aliases RepeatAliasSource
	Names   RepeatNameSource
	Facts   store.RepeatFactStore
	// Threshold 是触发阈值；<2 视为关闭。
	Threshold int
	// Window 是统计窗口，<=0 用 7 天。
	Window time.Duration
	// Debounce 是同一实体最短触发间隔，<=0 用 24 小时。
	Debounce time.Duration
	// MaxEvents / MaxNames 是单轮读取上限，<=0 用默认值，超过硬上限会被夹住。
	MaxEvents int
	MaxNames  int
	// IgnoreNames 是不参与统计的名字（鱼自己的昵称/曾用名）。
	IgnoreNames []string
}

// promoteRepeats 跑一次重复度触发；失败只记 stats，不影响提炼主流程。
func (w *Worker) promoteRepeats(ctx context.Context, stats *Stats) {
	stats.RepeatScanned = -1
	stats.RepeatSkipped = "none"
	if w.opts.Repeats == nil || w.opts.Repeats.Threshold < 2 {
		return
	}
	if err := w.runRepeatPromotion(ctx, w.opts.Repeats, stats); err != nil {
		stats.RepeatSkipped = "failed"
	}
}

// runRepeatPromotion 读取窗口事件与实体来源，统计并逐条提升。
func (w *Worker) runRepeatPromotion(ctx context.Context, r *RepeatPromotion, stats *Stats) error {
	if r.Events == nil || r.Facts == nil {
		return errors.New("重复度触发缺少事件来源或事实仓储")
	}
	now := w.now()
	window := r.Window
	if window <= 0 {
		window = repeatDefaultWindow
	}
	since := now.Add(-window)

	events, err := r.Events.PublicIncomingSince(ctx, since, repeatLimit(r.MaxEvents, repeatDefaultMaxEvents, repeatMaxEvents))
	if err != nil {
		return fmt.Errorf("读取重复度窗口事件：%w", err)
	}
	stats.RepeatScanned = len(events)

	var aliases []memory.RepeatAlias
	if r.Aliases != nil {
		aliases = r.Aliases.ConfirmedAliases()
	}
	var names []store.ViewerName
	if r.Names != nil {
		names, err = r.Names.RepeatViewerNames(ctx, since, repeatLimit(r.MaxNames, repeatDefaultMaxNames, repeatMaxNames))
		if err != nil {
			return fmt.Errorf("读取观众昵称：%w", err)
		}
	}

	entities := memory.RepeatEntities(aliases, repeatViewerNames(names), memory.RepeatReplyNames(events))
	entities = memory.FilterRepeatEntities(entities, r.IgnoreNames)
	candidates := memory.DedupeRepeatCandidates(memory.CountRepeatMentions(entities, events, since, r.Threshold))
	stats.RepeatCandidates = len(candidates)
	if len(candidates) == 0 {
		return nil
	}

	corrected := memory.CorrectedRepeatAliases(events, entities, w.opts.OwnerUID)
	debounce := r.Debounce
	if debounce <= 0 {
		debounce = repeatDefaultDebounce
	}
	days := int(window / (24 * time.Hour))
	if days < 1 {
		days = memory.RepeatWindowDays
	}
	for _, candidate := range candidates {
		if err := w.promoteRepeat(ctx, r.Facts, candidate, corrected, days, debounce, now, stats); err != nil {
			return err
		}
	}
	return nil
}

// promoteRepeat 处理单个候选：纠正降权 / 归档与防抖跳过 / 写入或修订事实。
func (w *Worker) promoteRepeat(ctx context.Context, facts store.RepeatFactStore, c memory.RepeatCandidate, corrected map[string]bool, days int, debounce time.Duration, now time.Time, stats *Stats) error {
	// 护栏：没有标题或事件证据一律不写。
	title := memory.RepeatFactTitle(c.Entity.Alias, c.Entity.Target)
	if title == "" || len(c.EventIDs) == 0 {
		return nil
	}
	state, found, err := facts.RepeatFactByTitle(ctx, title)
	if err != nil {
		return fmt.Errorf("查询重复度事实：%w", err)
	}
	if corrected[memory.RepeatEntityKey(c.Entity)] {
		// 被主人纠正：只降权既有事实，绝不重新写回。
		if found {
			if _, err := facts.DownweightFact(ctx, state.ID, repeatCorrectedImportance, repeatCorrectedConfidence); err != nil {
				return fmt.Errorf("降低被纠正称呼权重：%w", err)
			}
		}
		stats.RepeatCorrected++
		return nil
	}
	if found {
		if state.Archived {
			stats.RepeatArchived++
			return nil
		}
		// 防抖按事实行的 valid_from：冲突更新会推进它，跨进程也生效。
		if now.Sub(state.ValidFrom) < debounce {
			stats.RepeatDebounced++
			return nil
		}
	}

	summary := memory.RepeatFactSummary(c.Entity.Alias, c.Entity.Target, c.Count, days)
	if summary == "" {
		return nil
	}
	id := repeatFactIDPrefix + shortHash(memory.RepeatFactKey(c.Entity))
	if found {
		// 复用既有主键：改名会生成新 key，旧事实不会被改得标题与摘要不一致。
		id = state.ID
	}
	record := store.MemoryRecord{
		Record: memory.Record{
			ID:         id,
			Channel:    event.ChLive,
			Session:    repeatGlobalSession,
			Summary:    summary,
			Importance: repeatFactImportance,
			Confidence: repeatFactConfidence,
			EventIDs:   append([]string(nil), c.EventIDs...),
			ValidFrom:  now,
		},
		Source:    repeatSource,
		Kind:      "fact",
		Title:     title,
		Subject:   memory.SoulSubjectViewer,
		DerivedBy: repeatDerivedBy,
		Evidence: []store.MemoryEvidence{
			{Field: "mention_count", Type: store.ViewerSummaryBasisObserved, Text: fmt.Sprintf("%d 天内公开提及 %d 次", days, c.Count)},
			{Field: "event_ids", Type: store.ViewerSummaryBasisObserved, Text: clipRunes(strings.Join(c.EventIDs, ","), evidenceTextRunes)},
		},
	}
	inserted, err := facts.InsertRecord(ctx, record)
	if err != nil {
		return fmt.Errorf("写入重复度事实：%w", err)
	}
	if inserted {
		stats.RepeatInserted++
	} else {
		stats.RepeatDuplicated++
	}
	return nil
}

// repeatViewerNames 把 store 投影转成 memory 的纯函数入参。
func repeatViewerNames(names []store.ViewerName) []memory.RepeatName {
	out := make([]memory.RepeatName, 0, len(names))
	for _, n := range names {
		out = append(out, memory.RepeatName{UID: n.UID, Name: n.Name})
	}
	return out
}

// repeatLimit 归一化读取上限：<=0 用默认值，超过硬上限夹住。
func repeatLimit(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}
