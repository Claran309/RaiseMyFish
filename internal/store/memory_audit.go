package store

import (
	"context"
	"time"
)

// MemoryAuditItem 是审计清单里的一条聚合项。
type MemoryAuditItem struct {
	ID      string
	Title   string
	Kind    string
	Summary string
	// Count 是重复条数或引用次数，含义由所在清单决定。
	Count int64
	// UpdatedAt 是最近一次变更时间（没有则为零值）。
	UpdatedAt time.Time
	// Source 是来源（memory_sources.id）。
	Source string
}

// MemoryAuditReport 是 `fishctl memory audit` 的只读报告。
//
// 「从未引用」依赖 memory_usage；升级前没有归因数据时这里会是全量，
// 报告里必须带 Since 让运维知道数据覆盖范围，不能把「没记录」当成「没用过」。
type MemoryAuditReport struct {
	// Since 是 memory_usage 最早一条记录的时间；零值表示没有归因数据。
	Since time.Time
	// Active 是当前有效记忆数。
	Active int64
	// Duplicates 是同 fact_key 存在多条 active 的组。
	Duplicates []MemoryAuditItem
	// Conflicts 是最近的事实改口审计。
	Conflicts []MemoryAuditItem
	// NeverReferenced 是从未被回复引用过的记忆。
	NeverReferenced []MemoryAuditItem
	// MissingFrom 是缺少 from_id 的「关于某人」记忆。
	MissingFrom []MemoryAuditItem
}

// MemoryAuditRepo 提供记忆健康度审计（database.MemoryAuditRepo 实现）。
type MemoryAuditRepo interface {
	AuditMemories(ctx context.Context, limit int) (MemoryAuditReport, error)
}
