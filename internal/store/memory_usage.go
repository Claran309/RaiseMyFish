package store

import (
	"context"
	"time"
)

// MemoryUsage 是一次回复注入的一条记忆；只存 ID/排名/分数，不存正文。
type MemoryUsage struct {
	ReplyEventID string
	RecordID     string
	Rank         int
	Score        *float64
	Injected     bool
	Used         *bool
}

// MemoryUsageRepo 记录「这条回复参考了哪些记忆」。
type MemoryUsageRepo interface {
	RecordMemoryUsage(context.Context, []MemoryUsage) error
	MarkMemoryUsed(context.Context, string, []string, bool) error
}

// MemoryHit 是记忆评测的单条结果。
type MemoryHit struct {
	ReplyEventID string
	RecordIDs    []string
	At           time.Time
}
