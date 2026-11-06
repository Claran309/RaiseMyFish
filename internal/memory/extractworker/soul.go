package extractworker

// 本文件是「灵魂记忆」重建子任务：把高重要度的 fact/preference 提炼成长期设定文件。
//
// 节流：每小时最多跑一次（与观众档案同一个思路，但独立节流状态）；
// 写入：原子替换（临时文件 + rename），失败不影响主提炼流程。

import (
	"context"
	"strings"
	"time"

	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// soulOwnerKeyword 是「关于主人」的文本标记：带这个标记的记录归主人主体，
// 即使发言人是观众（观众也可能提供主人的设定）。
const soulOwnerKeyword = "主人"

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
	// 先读旧文件：手写条目要保留，自动条目整体替换
	existing, err := memory.LoadSoul(ctx, w.opts.SoulOpts.Path)
	if err != nil {
		stats.SoulSkipped = "failed"
		return
	}
	generated := memory.NormalizeSoul(soulEntriesFrom(records, w.opts.OwnerName))
	entries := memory.MergeSoul(existing, generated)
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

// soulEntriesFrom 把记录转成灵魂条目，保留主体与来源。
//
// 观众自述（from_id/from_name 指向非主人，且正文不是关于主人）一律不写全局设定；
// 主人本人的设定、关于主人的陈述、以及没有主体标识的系统设定才入选。
func soulEntriesFrom(records []store.MemoryRecord, ownerName string) []memory.SoulMemory {
	entries := make([]memory.SoulMemory, 0, len(records))
	for _, rec := range records {
		subject := soulSubjectFrom(rec, ownerName)
		if subject == memory.SoulSubjectViewer {
			continue
		}
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
			Subject:    subject,
			Source:     rec.Source,
			Visibility: soulVisibility(rec.Private),
			FromID:     rec.FromID,
			FromName:   rec.FromName,
		})
	}
	return entries
}

// soulSubjectFrom 推导条目主体。
//
// 顺序很重要：
//  1. 发言人就是主人 → owner；
//  2. 正文明确提到主人 → owner（观众提供的主人设定也有效）；
//  3. 其余带发送者标识的记录是观众自述 → viewer，不进全局；
//  4. 没有任何主体标识时，preference 按主人相关处理，其余按本鱼设定。
func soulSubjectFrom(rec store.MemoryRecord, ownerName string) string {
	name := strings.TrimSpace(rec.FromName)
	owner := strings.TrimSpace(ownerName)
	if owner != "" && name == owner {
		return memory.SoulSubjectOwner
	}
	if strings.Contains(rec.Title+"\n"+rec.Summary, soulOwnerKeyword) {
		return memory.SoulSubjectOwner
	}
	if strings.TrimSpace(rec.FromID) != "" || name != "" {
		return memory.SoulSubjectViewer
	}
	if rec.Kind == memory.KindPreference {
		return memory.SoulSubjectOwner
	}
	return memory.SoulSubjectFish
}

// soulVisibility 把「私密记录」映射成条目可见性；私密条目不会进全局注入。
func soulVisibility(private bool) string {
	if private {
		return memory.SoulVisibilityPrivate
	}
	return memory.SoulVisibilityPublic
}
