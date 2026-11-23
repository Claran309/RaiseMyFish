package database

// 本文件实现 M8 的记忆健康度审计：重复、改口、从未引用、缺 from_id。
//
// 只读：不改任何记忆，不自动合并；运维看完报告再用 consolidate/面板处理。

import (
	"context"
	"time"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// MemoryAuditRepo 是 store.MemoryAuditRepo 的 PG 实现。
type MemoryAuditRepo struct{ db *gorm.DB }

func NewMemoryAuditRepo(db *gorm.DB) (*MemoryAuditRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &MemoryAuditRepo{db: db}, nil
}

var _ store.MemoryAuditRepo = (*MemoryAuditRepo)(nil)

// AuditMemories 生成四类清单；limit 是每个清单的条数上限。
func (r *MemoryAuditRepo) AuditMemories(ctx context.Context, limit int) (store.MemoryAuditReport, error) {
	if ctx == nil || limit < 1 || limit > 200 {
		return store.MemoryAuditReport{}, store.ErrInvalid
	}
	var report store.MemoryAuditReport
	if err := r.db.WithContext(ctx).Raw(
		`SELECT count(*) FROM memory_records WHERE valid_to IS NULL AND NOT archived`,
	).Scan(&report.Active).Error; err != nil {
		return report, &safeError{"统计有效记忆失败", err}
	}
	// 用结构体承接 min()：直接 Scan 到 *time.Time 会变成 **time.Time，
	// 空表时 min 返回 NULL，扫描会报 unsupported Scan。
	var since struct{ At *time.Time }
	if err := r.db.WithContext(ctx).Raw(`SELECT min(created_at) AS at FROM memory_usage`).Scan(&since).Error; err != nil {
		return report, &safeError{"读取引用起点失败", err}
	}
	if since.At != nil {
		report.Since = *since.At
	}

	// 重复：同 fact_key 多条 active。fact_key 为空的旧行不参与（不是可判定的重复）。
	if err := r.db.WithContext(ctx).Raw(`
SELECT COALESCE(fact_key,'') AS id, min(title) AS title, min(kind) AS kind,
       min(summary) AS summary, count(*) AS count, max(created_at) AS updated_at,
       min(source) AS source
  FROM memory_records
 WHERE valid_to IS NULL AND NOT archived AND fact_key IS NOT NULL AND btrim(fact_key) <> ''
 GROUP BY fact_key
HAVING count(*) > 1
 ORDER BY count(*) DESC, max(created_at) DESC
 LIMIT ?`, limit).Scan(&report.Duplicates).Error; err != nil {
		return report, &safeError{"统计重复记忆失败", err}
	}

	// 改口：最近 memory_conflicts，按 fact_key 聚合次数。
	if err := r.db.WithContext(ctx).Raw(`
SELECT fact_key AS id, min(new_summary) AS title, '' AS kind, min(new_summary) AS summary,
       count(*) AS count, max(created_at) AS updated_at, '' AS source
  FROM memory_conflicts
 GROUP BY fact_key
 ORDER BY count(*) DESC, max(created_at) DESC
 LIMIT ?`, limit).Scan(&report.Conflicts).Error; err != nil {
		return report, &safeError{"统计改口记录失败", err}
	}

	// 从未引用：active 且没有 memory_usage 行。没有归因数据时这份清单没有意义，
	// 由调用方根据 Since 判断，不在这里伪造「全部未引用」。
	if !report.Since.IsZero() {
		if err := r.db.WithContext(ctx).Raw(`
SELECT m.id, m.title, m.kind, m.summary, 0 AS count, m.created_at AS updated_at, m.source
  FROM memory_records m
 WHERE m.valid_to IS NULL AND NOT m.archived
   AND NOT EXISTS (SELECT 1 FROM memory_usage u WHERE u.record_id = m.id)
 ORDER BY m.created_at DESC, m.id ASC
 LIMIT ?`, limit).Scan(&report.NeverReferenced).Error; err != nil {
			return report, &safeError{"统计未引用记忆失败", err}
		}
	}

	// 缺 from_id：偏好/事实/承诺这类「关于某人」的记录必须有来源 UID。
	if err := r.db.WithContext(ctx).Raw(`
SELECT id, title, kind, summary, 0 AS count, created_at AS updated_at, source
  FROM memory_records
 WHERE valid_to IS NULL AND NOT archived
   AND kind IN ('preference','fact','promise')
   AND (from_id IS NULL OR btrim(from_id) = '')
 ORDER BY created_at DESC, id ASC
 LIMIT ?`, limit).Scan(&report.MissingFrom).Error; err != nil {
		return report, &safeError{"统计缺来源记忆失败", err}
	}
	return report, nil
}
