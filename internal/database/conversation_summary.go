package database

// 本文件实现 C5 的会话滚动摘要任务队列：
// 入队（按轮数阈值或空闲时间）、租约领取、完成写回、失败与续租。
//
// 边界：
//   - 只做任务队列语义，不做摘要生成（那是 conversationsummary 的事）；
//   - 摘要正文只写 conversation_state.summary，不复制消息正文；
//   - 所有 SQL 参数化，状态迁移用条件更新，不用进程内锁。

import (
	"context"
	"errors"
	"strings"
	"time"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

const (
	// summaryEnqueueLimitMax 是单次入队上限。
	summaryEnqueueLimitMax = 200
	// summaryClaimLimitMax 是单次领取上限。
	summaryClaimLimitMax = 20
)

// ConversationSummaryJobsRepo 是 store.ConversationSummaryRepo 的 PG 实现。
type ConversationSummaryJobsRepo struct{ db *gorm.DB }

// NewConversationSummaryJobsRepo 注入既有连接，不自动迁移。
func NewConversationSummaryJobsRepo(db *gorm.DB) (*ConversationSummaryJobsRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &ConversationSummaryJobsRepo{db: db}, nil
}

var _ store.ConversationSummaryRepo = (*ConversationSummaryJobsRepo)(nil)

type conversationSummaryJobRow struct {
	ConversationID  string
	SummarizedEvent *string `gorm:"column:summarized_event_id"`
	TurnCount       int
	Status          string
	Attempts        int
	LeaseOwner      *string
	LeaseUntil      *time.Time
	LastError       *string
}

func (row conversationSummaryJobRow) job() store.ConversationSummaryJob {
	job := store.ConversationSummaryJob{
		ConversationID: row.ConversationID,
		TurnCount:      row.TurnCount,
		Status:         row.Status,
		Attempts:       row.Attempts,
	}
	if row.SummarizedEvent != nil {
		job.TargetEventID = *row.SummarizedEvent
	}
	if row.LeaseOwner != nil {
		job.LeaseOwner = *row.LeaseOwner
	}
	if row.LeaseUntil != nil {
		job.LeaseUntil = *row.LeaseUntil
	}
	if row.LastError != nil {
		job.LastError = *row.LastError
	}
	return job
}

// EnqueueSummaries 为需要摘要的会话补任务。
//
// 「需要摘要」= 未过期、有最后事件、且满足任一条件：
//   - 自上次摘要以来新增轮数 >= everyTurns；
//   - 会话空闲超过 idle（idle<=0 表示不启用空闲触发）。
//
// 幂等：同一会话同一目标事件只保留一条任务；目标事件变化才重置为 pending。
func (r *ConversationSummaryJobsRepo) EnqueueSummaries(ctx context.Context, now time.Time, channels []string, everyTurns int, idle time.Duration, limit int) (int, error) {
	if ctx == nil || now.IsZero() || everyTurns < 2 || limit < 1 {
		return 0, store.ErrInvalid
	}
	if limit > summaryEnqueueLimitMax {
		limit = summaryEnqueueLimitMax
	}
	var idleMicros int64
	if idle > 0 {
		idleMicros = idle.Microseconds()
	}
	var inserted int64
	// 通道过滤在 SQL 层完成：关闭评论摘要时不能靠 worker 事后丢弃，
	// 否则任务表会被永远领不完的行占满。
	channelClause := ""
	args := []any{now, everyTurns, idleMicros, now, idleMicros}
	if len(channels) > 0 {
		channelClause = " AND cs.channel IN ?"
		args = append(args, channels)
	}
	args = append(args, limit)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`
INSERT INTO conversation_summary_jobs (conversation_id, summarized_event_id, turn_count, status)
SELECT cs.conversation_id, cs.last_event_id, cs.turn_count, 'pending'
  FROM conversation_state cs
 WHERE cs.last_event_id IS NOT NULL
   AND cs.turn_count > cs.summary_turn_count
   AND (cs.expires_at IS NULL OR cs.expires_at > ?)
   AND (
        cs.turn_count - cs.summary_turn_count >= ?
     OR (? > 0 AND cs.last_turn_at IS NOT NULL
         AND cs.last_turn_at <= ?::timestamptz - (? * interval '1 microsecond'))
   )`+channelClause+`
 ORDER BY cs.last_turn_at NULLS FIRST, cs.conversation_id
 LIMIT ?
ON CONFLICT (conversation_id) DO UPDATE SET
  summarized_event_id = EXCLUDED.summarized_event_id,
  turn_count = EXCLUDED.turn_count,
  status = 'pending',
  lease_owner = NULL,
  lease_until = NULL,
  last_error = NULL,
  updated_at = clock_timestamp()
WHERE conversation_summary_jobs.summarized_event_id IS DISTINCT FROM EXCLUDED.summarized_event_id
  AND (conversation_summary_jobs.status <> 'running'
       OR conversation_summary_jobs.lease_until IS NULL
       OR conversation_summary_jobs.lease_until <= clock_timestamp())`,
			args...)
		if res.Error != nil {
			return res.Error
		}
		inserted = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, &safeError{"入队会话摘要任务失败", err}
	}
	return int(inserted), nil
}

// ClaimSummaries 领取一批到期任务：pending，或 running 但租约已过期。
func (r *ConversationSummaryJobsRepo) ClaimSummaries(ctx context.Context, owner string, ttl time.Duration, limit int) ([]store.ConversationSummaryJob, error) {
	if ctx == nil || strings.TrimSpace(owner) == "" || len(owner) > 128 || !validTTL(ttl) || limit < 1 {
		return nil, store.ErrInvalid
	}
	if limit > summaryClaimLimitMax {
		limit = summaryClaimLimitMax
	}
	var rows []conversationSummaryJobRow
	err := r.db.WithContext(ctx).Raw(`
WITH picked AS (
    SELECT conversation_id FROM conversation_summary_jobs
    WHERE status = 'pending'
       OR (status = 'running' AND lease_until <= clock_timestamp())
    ORDER BY created_at, conversation_id
    LIMIT ? FOR UPDATE SKIP LOCKED
)
UPDATE conversation_summary_jobs j
SET status = 'running',
    lease_owner = ?,
    lease_until = clock_timestamp() + (? * interval '1 microsecond'),
    attempts = j.attempts + 1,
    updated_at = clock_timestamp()
FROM picked p
WHERE j.conversation_id = p.conversation_id
RETURNING j.conversation_id, j.summarized_event_id, j.turn_count, j.status,
          j.attempts, j.lease_owner, j.lease_until, j.last_error`,
		limit, owner, ttl.Microseconds()).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"领取会话摘要任务失败", err}
	}
	out := make([]store.ConversationSummaryJob, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.job())
	}
	return out, nil
}

// CompleteSummary 在租约栅栏内写回摘要。
//
// 顺序固定：先条件更新任务表（0 行说明租约已被抢占/过期，直接冲突返回），
// 再更新会话状态；同一事务提交，不会出现「任务已完成、摘要没写」。
func (r *ConversationSummaryJobsRepo) CompleteSummary(ctx context.Context, job store.ConversationSummaryJob, summary, unanswered string) error {
	if ctx == nil || strings.TrimSpace(job.ConversationID) == "" || strings.TrimSpace(job.LeaseOwner) == "" {
		return store.ErrInvalid
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var fenced []struct{ TurnCount int }
		if err := tx.Raw(`
UPDATE conversation_summary_jobs
SET status = 'done', lease_owner = NULL, lease_until = NULL,
    last_error = NULL, updated_at = clock_timestamp()
WHERE conversation_id = ? AND status = 'running' AND lease_owner = ?
  AND lease_until > clock_timestamp()
  AND summarized_event_id IS NOT DISTINCT FROM NULLIF(?, '')
RETURNING turn_count`, job.ConversationID, job.LeaseOwner, job.TargetEventID).Scan(&fenced).Error; err != nil {
			return err
		}
		if len(fenced) == 0 {
			return store.ErrConflict
		}
		covered := fenced[0].TurnCount
		if job.TurnCount > covered {
			covered = job.TurnCount
		}
		res := tx.Exec(`
UPDATE conversation_state
SET summary = CASE WHEN ? <> '' THEN ? ELSE summary END,
    unanswered = CASE WHEN ? <> '' THEN ? ELSE unanswered END,
    summary_event_id = ?,
    summary_turn_count = GREATEST(summary_turn_count, ?),
    summary_updated_at = CASE WHEN ? <> '' THEN clock_timestamp() ELSE summary_updated_at END,
    updated_at = now()
WHERE conversation_id = ?`,
			summary, summary, unanswered, unanswered, job.TargetEventID, covered, summary, job.ConversationID)
		if res.Error != nil {
			return res.Error
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return store.ErrConflict
		}
		return &safeError{"写回会话摘要失败", err}
	}
	return nil
}

// FailSummary 记录失败：可重试且未超上限回 pending，否则置 failed。
func (r *ConversationSummaryJobsRepo) FailSummary(ctx context.Context, job store.ConversationSummaryJob, reason string, retryable bool, maxAttempts int) error {
	if ctx == nil || strings.TrimSpace(job.ConversationID) == "" || strings.TrimSpace(job.LeaseOwner) == "" {
		return store.ErrInvalid
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	next := "failed"
	if retryable && job.Attempts < maxAttempts {
		next = "pending"
	}
	res := r.db.WithContext(ctx).Exec(`
UPDATE conversation_summary_jobs
SET status = ?, lease_owner = NULL, lease_until = NULL,
    last_error = ?, updated_at = clock_timestamp()
WHERE conversation_id = ? AND status = 'running' AND lease_owner = ?`,
		next, truncateError(reason), job.ConversationID, job.LeaseOwner)
	if res.Error != nil {
		return &safeError{"记录会话摘要任务失败", res.Error}
	}
	if res.RowsAffected == 0 {
		return store.ErrConflict
	}
	return nil
}

// RenewSummary 续租；摘要可能因模型较慢而超过初始 TTL。
func (r *ConversationSummaryJobsRepo) RenewSummary(ctx context.Context, job store.ConversationSummaryJob, ttl time.Duration) (store.ConversationSummaryJob, error) {
	if ctx == nil || strings.TrimSpace(job.ConversationID) == "" || strings.TrimSpace(job.LeaseOwner) == "" || !validTTL(ttl) {
		return store.ConversationSummaryJob{}, store.ErrInvalid
	}
	var rows []conversationSummaryJobRow
	err := r.db.WithContext(ctx).Raw(`
UPDATE conversation_summary_jobs
SET lease_until = clock_timestamp() + (? * interval '1 microsecond'), updated_at = clock_timestamp()
WHERE conversation_id = ? AND status = 'running' AND lease_owner = ?
  AND lease_until > clock_timestamp()
RETURNING conversation_id, summarized_event_id, turn_count, status,
          attempts, lease_owner, lease_until, last_error`,
		ttl.Microseconds(), job.ConversationID, job.LeaseOwner).Scan(&rows).Error
	if err != nil {
		return store.ConversationSummaryJob{}, &safeError{"续租会话摘要任务失败", err}
	}
	if len(rows) == 0 {
		return store.ConversationSummaryJob{}, store.ErrConflict
	}
	return rows[0].job(), nil
}
