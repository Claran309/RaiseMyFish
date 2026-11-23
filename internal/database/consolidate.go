package database

import (
	"context"
	"time"

	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// ConsolidationRepo 提供 dry-run 巩固计划所需的只读聚合。
type ConsolidationRepo struct{ db *gorm.DB }

func NewConsolidationRepo(db *gorm.DB) (*ConsolidationRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &ConsolidationRepo{db: db}, nil
}

// Candidates 返回当前有效记忆 + memory_usage 聚合（引用次数/最近引用）。
func (r *ConsolidationRepo) Candidates(ctx context.Context, limit int) ([]memory.ConsolidationCandidate, error) {
	if ctx == nil || limit < 1 || limit > 5000 {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID             string
		Kind           string
		Title          string
		Summary        string
		Importance     float64
		Confidence     float64
		ValidFrom      time.Time
		Archived       bool
		FactKey        *string
		ReferenceCount int
		LastReferenced *time.Time
	}
	err := r.db.WithContext(ctx).Raw(`
SELECT m.id, m.kind, m.title, m.summary, m.importance, m.confidence, m.valid_from,
       m.archived, m.fact_key,
       COUNT(u.id) AS reference_count,
       MAX(u.created_at) AS last_referenced
  FROM memory_records m
  LEFT JOIN memory_usage u ON u.record_id = m.id AND u.injected
 WHERE m.valid_to IS NULL
 GROUP BY m.id
 ORDER BY m.valid_from DESC
 LIMIT ?`, limit).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"读取巩固候选失败", err}
	}
	out := make([]memory.ConsolidationCandidate, 0, len(rows))
	for _, row := range rows {
		candidate := memory.ConsolidationCandidate{
			ID: row.ID, Kind: row.Kind, Title: row.Title, Summary: row.Summary,
			Importance: row.Importance, Confidence: row.Confidence, ValidFrom: row.ValidFrom,
			Archived: row.Archived, ReferenceCount: row.ReferenceCount,
		}
		if row.FactKey != nil {
			candidate.FactKey = *row.FactKey
		}
		if row.LastReferenced != nil {
			candidate.LastReferencedAt = *row.LastReferenced
		}
		out = append(out, candidate)
	}
	return out, nil
}

// Apply 执行已确认的计划；只允许 archive/supersede 两类，且只动未归档行。
func (r *ConsolidationRepo) Apply(ctx context.Context, actions []memory.ConsolidationAction) (int, error) {
	if ctx == nil || len(actions) == 0 {
		return 0, store.ErrInvalid
	}
	applied := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, action := range actions {
			switch action.Action {
			case "archive":
				res := tx.Exec(`UPDATE memory_records SET archived=TRUE, status='archived' WHERE id=? AND valid_to IS NULL AND NOT archived`, action.ID)
				if res.Error != nil {
					return &safeError{"归档记忆失败", res.Error}
				}
				applied += int(res.RowsAffected)
			case "supersede":
				res := tx.Exec(`UPDATE memory_records SET status='superseded' WHERE id=? AND valid_to IS NULL AND NOT archived`, action.ID)
				if res.Error != nil {
					return &safeError{"标记重复事实失败", res.Error}
				}
				applied += int(res.RowsAffected)
			default:
				return store.ErrInvalid
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return applied, nil
}
