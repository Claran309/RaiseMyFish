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
	// BatchSize 单次拉取条数，<=0 用默认值，超过上限会被夹到上限。
	BatchSize int
	// ScanInterval 定时扫描间隔，<=0 用 5 分钟。
	ScanInterval time.Duration
	// Source 覆盖 memory_sources 的来源 id；为空时按候选类型推导。
	Source string
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
}

// Worker 是提炼 worker 本体。
type Worker struct {
	source DoneSource
	repo   store.MemoryRepo
	wm     Watermark
	opts   Options
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
	return stats, nil
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

	candidates := memory.Extract(ev, memory.ExtractOptions{
		OwnerName: w.opts.OwnerName,
		// 首次出现需要「该 session 已见昵称集合」，属持久化能力，
		// 既有 repo 没有对应接口，本批不臆造存储，固定传 false。
		IsFirstNick:  false,
		AllowPrivate: w.opts.AllowPrivate,
	})
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

// buildRecord 把候选装配成持久化记录。
func (w *Worker) buildRecord(ev event.Event, c memory.Candidate) store.MemoryRecord {
	source := w.opts.Source
	if source == "" {
		source = defaultSource(c.Kind)
	}
	return store.MemoryRecord{
		Record: memory.Record{
			ID:         recordID(ev.ID, c.Kind, c.Title),
			Channel:    ev.Channel,
			Session:    ev.Session,
			Summary:    c.Summary,
			Importance: c.Importance,
			Confidence: c.Confidence,
			Private:    ev.Private,
			EventIDs:   []string{ev.ID},
			ValidFrom:  ev.At,
		},
		Source: source,
		Kind:   storeKind(c.Kind),
		Title:  c.Title,
		// Body 留空：摘要已经够召回用，不把完整正文写进记忆库
		Body: "",
	}
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
