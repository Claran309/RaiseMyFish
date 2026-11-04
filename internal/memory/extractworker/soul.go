package extractworker

// 本文件是「灵魂记忆」重建子任务：把高重要度的 fact/preference 提炼成长期设定文件。
//
// 节流：每小时最多跑一次（与观众档案同一个思路，但独立节流状态）；
// 写入：原子替换（临时文件 + rename），失败不影响主提炼流程。

import (
	"context"
	"time"

	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// SoulSource 是灵魂记忆重建所需的最小能力（由 database.MemoryRepo 实现）。
type SoulSource interface {
	SoulCandidates(ctx context.Context, minImportance float64, limit int) ([]store.MemoryRecord, error)
}

// SoulOptions 是重建参数。
type SoulOptions struct {
	// Path 是输出文件（personas/<pack>/knowledge/soul-memories.md）。
	Path string
	// MaxEntries 是条数上限（默认 20）。
	MaxEntries int
	// MinImportance 是入选门槛（默认 0.8）。
	MinImportance float64
	// Interval 是节流间隔（默认 1 小时）。
	Interval time.Duration
}

// rebuildSoul 跑一次灵魂记忆重建；返回写入条数（-1 表示跳过）。
func (w *Worker) rebuildSoul(ctx context.Context, stats *Stats) {
	stats.SoulEntries = -1
	if w.opts.Soul == nil || w.opts.SoulOpts.Path == "" {
		stats.SoulSkipped = "none"
		return
	}
	now := w.now()
	if !w.lastSoul.IsZero() && now.Sub(w.lastSoul) < w.opts.SoulOpts.Interval {
		stats.SoulSkipped = "throttled"
		return
	}
	w.lastSoul = now

	maxEntries := w.opts.SoulOpts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 20
	}
	minImportance := w.opts.SoulOpts.MinImportance
	if minImportance <= 0 {
		minImportance = 0.8
	}

	records, err := w.opts.Soul.SoulCandidates(ctx, minImportance, maxEntries*3)
	if err != nil {
		stats.SoulSkipped = "failed"
		return
	}
	entries := memory.NormalizeSoul(soulEntriesFrom(records))
	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}
	if err := memory.WriteSoulAtomic(ctx, w.opts.SoulOpts.Path, entries); err != nil {
		stats.SoulSkipped = "failed"
		return
	}
	stats.SoulEntries = len(entries)
	stats.SoulSkipped = ""
}

// soulEntriesFrom 把记录转成灵魂条目并去重（同标题取重要的那条）。
func soulEntriesFrom(records []store.MemoryRecord) []memory.SoulMemory {
	entries := make([]memory.SoulMemory, 0, len(records))
	for _, rec := range records {
		title := rec.Title
		if title == "" {
			title = rec.Summary
		}
		if title == "" {
			continue
		}
		entries = append(entries, memory.SoulMemory{
			Title:      title,
			Summary:    rec.Summary,
			Importance: rec.Importance,
			ValidFrom:  rec.ValidFrom,
		})
	}
	return entries
}
