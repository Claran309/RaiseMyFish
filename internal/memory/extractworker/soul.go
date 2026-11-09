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

// soulOwnerKeyword 是「关于主人」的文本标记，只用于旧数据的回退判断：
// 文本提到主人 ≠ 主人本人说的，不能作为身份证据。
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
	generated := memory.NormalizeSoul(soulEntriesFrom(records, w.opts.OwnerUID))
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
// 提升为全局设定的策略是 fail-closed：
//   - 观众自述、观众对主人的转述一律不提升，只留在记忆库当带来源候选；
//   - 主人主体必须由可信 UID 证明是主人本人说的，昵称不计；
//   - 没有说话人标识的系统设定按本鱼处理，其余未知主体一律不提升。
func soulEntriesFrom(records []store.MemoryRecord, ownerUID string) []memory.SoulMemory {
	entries := make([]memory.SoulMemory, 0, len(records))
	for _, rec := range records {
		subject := soulSubjectFrom(rec, ownerUID)
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

// soulSubjectFrom 判定一条记录能否提升为全局设定，并返回提升后的主体。
//
// 「谁说的」和「说的是谁」分开看：
//  1. asserted_by 为空时回退 from_id——旧行里 from_id 存的就是发言人；
//  2. Subject=owner 只认「asserted_by == owner_uid」这一条证据；
//     观众转述（哪怕正文反复出现「主人」）只返回 viewer，不提升；
//  3. Subject=fish 只接受无发言人（系统设定）或主人本人，其余降级 viewer；
//  4. 旧行没有 subject 列时按发言人推导；无来源的「主人」旧数据按未知处理，
//     宁可让旧设定重新积累，也不把无法溯源的转述升成全局人格。
func soulSubjectFrom(rec store.MemoryRecord, ownerUID string) string {
	owner := strings.TrimSpace(ownerUID)
	assertedBy := strings.TrimSpace(rec.AssertedByID)
	if assertedBy == "" {
		assertedBy = strings.TrimSpace(rec.FromID)
	}
	subject := strings.ToLower(strings.TrimSpace(rec.Subject))
	if subject == "" {
		switch {
		case owner != "" && assertedBy == owner:
			subject = memory.SoulSubjectOwner
		case assertedBy != "" || strings.TrimSpace(rec.FromName) != "":
			subject = memory.SoulSubjectViewer
		case strings.Contains(rec.Title+"\n"+rec.Summary, soulOwnerKeyword):
			subject = memory.SoulSubjectViewer
		default:
			subject = memory.SoulSubjectFish
		}
	}
	switch subject {
	case memory.SoulSubjectOwner:
		if owner != "" && assertedBy == owner {
			return memory.SoulSubjectOwner
		}
		return memory.SoulSubjectViewer
	case memory.SoulSubjectFish:
		if assertedBy == "" || (owner != "" && assertedBy == owner) {
			return memory.SoulSubjectFish
		}
		return memory.SoulSubjectViewer
	case memory.SoulSubjectViewer:
		return memory.SoulSubjectViewer
	default:
		return memory.SoulSubjectViewer
	}
}

// soulVisibility 把「私密记录」映射成条目可见性；私密条目不会进全局注入。
func soulVisibility(private bool) string {
	if private {
		return memory.SoulVisibilityPrivate
	}
	return memory.SoulVisibilityPublic
}
