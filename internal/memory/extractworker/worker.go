// Package extractworker 实现 0.9b 的记忆提炼 worker。
//
// 职责：定时扫描已完成的（status=done）事件 → 调 memory.Extract 提炼候选 →
// 经既有 store.MemoryRepo.InsertRecord 落 memory_records → 推进水位。
//
// 边界：
//   - 不调用模型、不做语义理解，规则全在 memory.Extract。
//   - 不碰 SQL：读写都走注入的接口（DoneSource / store.MemoryRepo）。
//   - 幂等靠记录主键：ID 由 event_id + kind + title 派生，配合
//     INSERT ... ON CONFLICT (id) DO NOTHING，同一事件重扫不会产生重复行。
//   - 不设计隐私策略：私密事件是否提炼由调用方 AllowPrivate 决定，默认拒绝。
package extractworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// 默认参数。
const (
	// defaultBatchSize 是单次拉取的事件条数。
	defaultBatchSize = 50
	// maxBatchSize 防止调用方传过大的 limit 把库里一次捞爆。
	maxBatchSize = 500
	// defaultScanInterval 是定时扫描间隔；phase-0 计划里写的是「先每 5 分钟扫一次」。
	defaultScanInterval = 5 * time.Minute
)

// Cursor 是水位：已处理到的「事件时间 + 事件 ID」位置。
//
// 排序键取 (At, ID)：At 相同时用 ID 兜底，保证扫描顺序稳定、不漏不重。
// 零值表示「从头开始」。
type Cursor struct {
	// At 事件时间。
	At time.Time
	// ID 事件幂等键。
	ID string
}

// IsZero 判断是否为零水位（还没处理过任何事件）。
func (c Cursor) IsZero() bool { return c.At.IsZero() && c.ID == "" }

// equal 判断两个游标是否指向同一位置。
func (c Cursor) equal(other Cursor) bool { return c.At.Equal(other.At) && c.ID == other.ID }

// DoneSource 拉取「已完成且排在游标之后」的事件，按 (At, ID) 升序返回。
//
// 实现方负责过滤 status=done 与排序；worker 不写 SQL、不猜平台字段。
// 返回空切片表示没有积压。
type DoneSource interface {
	ListDoneAfter(ctx context.Context, after Cursor, limit int) ([]event.Event, error)
}

// Watermark 保存与读取提炼水位。
type Watermark interface {
	Load(ctx context.Context) (Cursor, error)
	Save(ctx context.Context, c Cursor) error
}

// Options 是 worker 的行为参数。
type Options struct {
	// OwnerName 主人昵称，透传给提炼规则。
	OwnerName string
	// AllowPrivate 为 true 时才提炼私密事件；默认 false（fail-closed）。
	AllowPrivate bool
	// Extractor 是提炼器；nil 时用规则版（memory.NewRuleExtractor）。
	// 传 LLMExtractor 时按触发策略走模型，失败内部回落规则版。
	Extractor memory.Extractor
	// BatchSize 单次拉取条数，<=0 用默认值，超过上限会被夹到上限。
	BatchSize int
	// ScanInterval 定时扫描间隔，<=0 用 5 分钟。
	ScanInterval time.Duration
	// Source 覆盖 memory_sources 的来源 id；为空时按候选类型推导。
	Source string
	// Now 注入时钟，仅测试需要；为空时用 time.Now。
	Now func() time.Time
	// Profiles 是观众档案重建依赖；nil 表示不跑这个子任务。
	Profiles ProfileSource
	// ProfileInterval 是档案重建节流间隔，<=0 用 1 小时。
	ProfileInterval time.Duration
	// ProfileMinHits 建档门槛，<=0 用 2。
	ProfileMinHits int
	// ProfileRecentTexts / ProfileRecentReplies 是档案保留条数。
	ProfileRecentTexts   int
	ProfileRecentReplies int
	// ProfileActiveWindow 是「直播活跃」判定窗口，<=0 用 5 分钟。
	ProfileActiveWindow time.Duration
	// Recent 提供同 session 最近事件，用于给 LLM 提炼喂上下文；nil 则不喂。
	Recent RecentEventsSource
	// ContextLimit 是喂给提炼器的上下文条数，<=0 用 5。
	ContextLimit int
}

// RecentEventsSource 提供某会话最近的事件（database.EventRepo 满足）。
type RecentEventsSource interface {
	RecentEvents(ctx context.Context, ch event.Channel, session string, limit int) ([]event.Event, error)
}

// ProfileSource 是观众档案重建所需的最小能力（由 database.ViewerRepo 实现）。
type ProfileSource interface {
	// ActiveSince 判断 since 之后是否有入向事件。
	ActiveSince(ctx context.Context, since time.Time) (bool, error)
	// RebuildProfiles 从 events 全量重算档案。
	RebuildProfiles(ctx context.Context, opts store.ViewerRebuildOptions) (store.ViewerRebuildStats, error)
}

// Stats 是一轮扫描的统计，交给上层打日志/日报。
type Stats struct {
	// Scanned 扫描到的事件条数。
	Scanned int
	// Extracted 产出候选的条数（一条事件可能产出多条）。
	Extracted int
	// Inserted 真正写库成功的条数。
	Inserted int
	// Duplicated 命中主键冲突（幂等去重）的条数。
	Duplicated int
	// Skipped 没提炼出候选（含私密未授权、空文本、无规则命中、时间缺失）的事件条数。
	Skipped int
	// ProfilesRebuilt 本轮重建的档案数（-1 表示本轮跳过）。
	ProfilesRebuilt int
	// ProfileSkipped 是跳过重建的原因（throttled / idle / none）。
	ProfileSkipped string
}

// Worker 是提炼 worker 本体。
type Worker struct {
	source DoneSource
	repo   store.MemoryRepo
	wm     Watermark
	opts   Options
	// 档案重建节流状态（进程内，重启后立刻允许跑一次）
	lastProfile time.Time
}

// New 构造 worker，并规范化参数。
func New(source DoneSource, repo store.MemoryRepo, wm Watermark, opts Options) (*Worker, error) {
	if source == nil {
		return nil, errors.New("事件来源不能为 nil")
	}
	if repo == nil {
		return nil, errors.New("记忆仓库不能为 nil")
	}
	if wm == nil {
		return nil, errors.New("水位存储不能为 nil")
	}

	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}
	if opts.BatchSize > maxBatchSize {
		opts.BatchSize = maxBatchSize
	}
	if opts.ScanInterval <= 0 {
		opts.ScanInterval = defaultScanInterval
	}
	if opts.ProfileInterval <= 0 {
		opts.ProfileInterval = time.Hour
	}
	if opts.ProfileMinHits <= 0 {
		opts.ProfileMinHits = 2
	}
	if opts.ProfileRecentTexts <= 0 {
		opts.ProfileRecentTexts = 15
	}
	if opts.ProfileRecentReplies <= 0 {
		opts.ProfileRecentReplies = 10
	}
	if opts.ProfileActiveWindow <= 0 {
		opts.ProfileActiveWindow = 5 * time.Minute
	}
	if opts.Extractor == nil {
		opts.Extractor = memory.NewRuleExtractor()
	}
	if opts.ContextLimit <= 0 {
		opts.ContextLimit = 5
	}
	opts.Source = strings.TrimSpace(opts.Source)

	return &Worker{source: source, repo: repo, wm: wm, opts: opts}, nil
}

// Run 按 ScanInterval 定时扫描，直到 ctx 被取消。
//
// 取消属于正常停止，返回 nil；单轮出错则立刻返回错误，交给上层决定是否重启 worker。
func (w *Worker) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("ctx 不能为 nil")
	}
	ticker := time.NewTicker(w.opts.ScanInterval)
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

// RunOnce 跑一轮：从水位开始按批扫描直到没有积压，然后落一次水位。
//
// 出错时的水位策略：只保存「已成功处理到」的位置，绝不越过失败事件，
// 这样重启后能从那一条补上，而不是丢事件。
func (w *Worker) RunOnce(ctx context.Context) (Stats, error) {
	var stats Stats

	if ctx == nil {
		return stats, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}

	start, err := w.wm.Load(ctx)
	if err != nil {
		return stats, fmt.Errorf("读取提炼水位：%w", err)
	}

	cursor := start
	for {
		batch, err := w.source.ListDoneAfter(ctx, cursor, w.opts.BatchSize)
		if err != nil {
			// 已经处理过的部分照样落水位，避免整轮白干
			if saveErr := w.saveIfAdvanced(ctx, start, cursor); saveErr != nil {
				return stats, errors.Join(fmt.Errorf("扫描待提炼事件：%w", err), saveErr)
			}
			return stats, fmt.Errorf("扫描待提炼事件：%w", err)
		}
		if len(batch) == 0 {
			break
		}

		for _, ev := range batch {
			stats.Scanned++
			next, err := w.handleEvent(ctx, ev, &stats)
			if err != nil {
				// 不推进到失败事件，保留重试机会
				if saveErr := w.saveIfAdvanced(ctx, start, cursor); saveErr != nil {
					return stats, errors.Join(err, saveErr)
				}
				return stats, err
			}
			cursor = next
		}
	}

	if err := w.saveIfAdvanced(ctx, start, cursor); err != nil {
		return stats, err
	}
	w.rebuildProfiles(ctx, &stats)
	return stats, nil
}

// rebuildProfiles 是档案重建子任务：每小时节流 + 只在直播活跃（最近 5 分钟有 in 事件）时跑。
//
// 失败只记在 stats 里，不影响提炼主流程（档案是增强，不是前置）。
func (w *Worker) rebuildProfiles(ctx context.Context, stats *Stats) {
	stats.ProfilesRebuilt = -1
	if w.opts.Profiles == nil {
		stats.ProfileSkipped = "none"
		return
	}
	now := w.now()
	if !w.lastProfile.IsZero() && now.Sub(w.lastProfile) < w.opts.ProfileInterval {
		stats.ProfileSkipped = "throttled"
		return
	}
	active, err := w.opts.Profiles.ActiveSince(ctx, now.Add(-w.opts.ProfileActiveWindow))
	if err != nil || !active {
		stats.ProfileSkipped = "idle"
		return
	}
	res, err := w.opts.Profiles.RebuildProfiles(ctx, store.ViewerRebuildOptions{
		MinHits:       w.opts.ProfileMinHits,
		RecentTexts:   w.opts.ProfileRecentTexts,
		RecentReplies: w.opts.ProfileRecentReplies,
	})
	// 无论成功失败都记节流时间：坏库时不要每 5 分钟重试一次
	w.lastProfile = now
	if err != nil {
		stats.ProfileSkipped = "failed"
		return
	}
	stats.ProfilesRebuilt = res.Profiles
	stats.ProfileSkipped = ""
}

// handleEvent 处理单条事件，返回本条事件对应的新水位。
func (w *Worker) handleEvent(ctx context.Context, ev event.Event, stats *Stats) (Cursor, error) {
	// 本条事件处理完后的位置
	next := Cursor{At: ev.At, ID: ev.ID}

	// 事件时间缺失：算不出有效期，也过不了持久化校验（valid_from 不能为零）
	if ev.At.IsZero() {
		stats.Skipped++
		return next, nil
	}

	extractOpts := memory.ExtractOptions{
		OwnerName: w.opts.OwnerName,
		// 首次出现需要「该 session 已见昵称集合」，属持久化能力，
		// 既有 repo 没有对应接口，本批不臆造存储，固定传 false。
		IsFirstNick:  false,
		AllowPrivate: w.opts.AllowPrivate,
	}
	// 上下文：取同 session 最近几条（去掉当前这条），失败降级为无上下文
	if w.opts.Recent != nil {
		if recent, err := w.opts.Recent.RecentEvents(ctx, ev.Channel, ev.Session, w.opts.ContextLimit+1); err == nil {
			extractOpts.Context = withoutCurrent(recent, ev.ID)
		}
	}
	extractor := w.opts.Extractor
	if extractor == nil {
		extractor = memory.NewRuleExtractor()
	}
	candidates, err := extractor.Extract(ctx, ev, extractOpts)
	if err != nil {
		// 提炼器异常兜底规则版；LLMExtractor 内部通常已降级，这里防未来实现。
		candidates = memory.Extract(ev, extractOpts)
	}
	if len(candidates) == 0 {
		stats.Skipped++
		return next, nil
	}

	for _, candidate := range candidates {
		stats.Extracted++
		inserted, err := w.repo.InsertRecord(ctx, w.buildRecord(ev, candidate))
		if err != nil {
			return next, fmt.Errorf("写入记忆记录：%w", err)
		}
		if inserted {
			stats.Inserted++
		} else {
			stats.Duplicated++
		}
	}
	return next, nil
}

// withoutCurrent 去掉当前这条事件，保留旧→新顺序。
func withoutCurrent(rows []event.Event, currentID string) []event.Event {
	out := make([]event.Event, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- { // 查询是倒序，翻转成旧→新
		if rows[i].ID == currentID {
			continue
		}
		out = append(out, rows[i])
	}
	return out
}

// buildRecord 把候选装配成持久化记录。
func (w *Worker) buildRecord(ev event.Event, c memory.Candidate) store.MemoryRecord {
	source := w.opts.Source
	if source == "" {
		source = defaultSource(c.Kind)
	}
	return store.MemoryRecord{
		Record: memory.Record{
			ID:         recordIDFor(ev, c),
			Channel:    ev.Channel,
			Session:    ev.Session,
			Summary:    c.Summary,
			Importance: c.Importance,
			Confidence: c.Confidence,
			Private:    ev.Private,
			EventIDs:   []string{ev.ID},
			ValidFrom:  w.validFrom(ev.At),
		},
		Source:   source,
		Kind:     storeKind(c.Kind),
		Title:    c.Title,
		FromID:   strings.TrimSpace(c.FromID),
		FromName: strings.TrimSpace(c.FromName),
		// Body 留空：摘要已经够召回用，不把完整正文写进记忆库
		Body: "",
	}
}

// validFrom 返回记录的有效期起点：取事件时间，但**绝不晚于当前时间**。
//
// 为什么要兜底：memory_records 的有效性查询是 `valid_from <= now()`，
// 一旦事件时间在未来（时钟漂移、预置数据、跨机同步），写进去的记录会立刻不可见，
// 表现成「提炼明明成功、召回却永远为空」这种很难查的静默故障。
func (w *Worker) validFrom(at time.Time) time.Time {
	now := w.now()
	if at.After(now) {
		return now
	}
	return at
}

// now 返回当前时间；测试可注入 clock 让断言稳定。
func (w *Worker) now() time.Time {
	if w.opts.Now != nil {
		return w.opts.Now()
	}
	return time.Now()
}

// saveIfAdvanced 只在水位真的前进时才写盘，避免空转时反复覆盖文件。
func (w *Worker) saveIfAdvanced(ctx context.Context, from, to Cursor) error {
	if to.equal(from) {
		return nil
	}
	if err := w.wm.Save(ctx, to); err != nil {
		return fmt.Errorf("保存提炼水位：%w", err)
	}
	return nil
}

// recordID 由 event_id + kind + title 派生稳定的主键，实现幂等。
//
// 用 sha256 前 12 字节（96 位）足够避免碰撞，且长度可控；
// 分隔符用 \x1f 防止「ab+c」与「a+bc」拼接后撞车。
// recordIDFor 生成记录主键。
//
// 事实类按「同一个人 + 同一句事实」去重（而不是按事件）：同一个人在不同时间
// 重复说「我是学生」，只该留一条。其他类别仍按事件去重。
func recordIDFor(ev event.Event, c memory.Candidate) string {
	if c.Kind == memory.KindFact && strings.TrimSpace(ev.FromID) != "" {
		return "fact:" + strings.TrimSpace(ev.FromID) + ":" + shortHash(c.Title)
	}
	return recordID(ev.ID, c.Kind, c.Title)
}

// shortHash 取摘要前 12 个十六进制字符，足够在同一人下区分不同事实。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func recordID(eventID, kind, title string) string {
	sum := sha256.Sum256([]byte(eventID + "\x1f" + kind + "\x1f" + title))
	return "mem_" + hex.EncodeToString(sum[:12])
}

// storeKind 把提炼候选的类型映射到 store 允许的记忆类型。
//
// store.MemoryRecord.Validate 只接受 talk/preference/promise/fact/summary/note，
// 其中 first_seen 没有对应项，落到最贴近的 note（观察记录）。
// 这是映射而不是改契约：要新增 kind 必须先改 store 校验。
func storeKind(kind string) string {
	switch kind {
	case memory.KindPromise:
		return "promise"
	case memory.KindPreference:
		return "preference"
	case memory.KindFact:
		return "fact"
	case memory.KindFirstSeen:
		return "note"
	default:
		return "note"
	}
}

// defaultSource 按候选类型挑 memory_sources 里的来源字典项。
//
// 现有种子只有 owner_direct / owner_interaction / persona / expression / content / reading，
// 首次出现最贴近 owner_interaction（互动信号）。
func defaultSource(kind string) string {
	switch kind {
	case memory.KindPromise, memory.KindPreference:
		return "owner_direct"
	case memory.KindFirstSeen:
		return "owner_interaction"
	default:
		return "content"
	}
}
