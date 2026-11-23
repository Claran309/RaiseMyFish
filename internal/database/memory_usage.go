package database

import (
	"context"
	"strings"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// MemoryUsageRepo 写入回复级记忆归因。
type MemoryUsageRepo struct{ db *gorm.DB }

func NewMemoryUsageRepo(db *gorm.DB) (*MemoryUsageRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &MemoryUsageRepo{db: db}, nil
}

var _ store.MemoryUsageRepo = (*MemoryUsageRepo)(nil)

// RecordMemoryUsage 批量写入；同一回复重复写入时按 (reply,record) 去重。
func (r *MemoryUsageRepo) RecordMemoryUsage(ctx context.Context, rows []store.MemoryUsage) error {
	if ctx == nil || len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			if strings.TrimSpace(row.ReplyEventID) == "" || strings.TrimSpace(row.RecordID) == "" {
				return store.ErrInvalid
			}
			if err := tx.Exec(`INSERT INTO memory_usage(reply_event_id,record_id,rank,score,injected,used)
VALUES(?,?,?,?,?,?)
ON CONFLICT (reply_event_id,record_id) DO NOTHING`, row.ReplyEventID, row.RecordID, row.Rank, row.Score, row.Injected, row.Used).Error; err != nil {
				return &safeError{"写入记忆使用记录失败", err}
			}
		}
		return nil
	})
}

// MarkMemoryUsed 标记某条回复引用的记忆是否被采用。
func (r *MemoryUsageRepo) MarkMemoryUsed(ctx context.Context, replyEventID string, recordIDs []string, used bool) error {
	if ctx == nil || strings.TrimSpace(replyEventID) == "" || len(recordIDs) == 0 {
		return store.ErrInvalid
	}
	if err := r.db.WithContext(ctx).Exec(`UPDATE memory_usage SET used=? WHERE reply_event_id=? AND record_id IN ?`,
		used, replyEventID, recordIDs).Error; err != nil {
		return &safeError{"更新记忆使用标记失败", err}
	}
	return nil
}
