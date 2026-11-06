package database

// 本文件实现 M05 的提炼任务表（extraction_jobs）读写：
// 入队（事件 → pending 任务）、租约领取、完成、失败与续租。
//
// 边界：
//   - 只做任务队列语义，不做提炼规则、不做隐私判断（那是 extractworker 的事）；
//   - 私密事件同样入队：是否提炼由 worker 的 AllowPrivate 决定（隐私判断集中一处）；
//   - 所有 SQL 参数化，状态迁移用条件更新，不用进程内锁。

import (
	"context"
	"strings"
	"time"

	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

const (
	// extractionEnqueueLimitMax 是单次入队上限。
	extractionEnqueueLimitMax = 500
	// extractionClaimLimitMax 是单次领取上限。
	extractionClaimLimitMax = 200
)

// ExtractionJobsRepo 是 extractworker.JobSource 的 PG 实现。
type ExtractionJobsRepo struct{ db *gorm.DB }

// NewExtractionJobsRepo 注入既有连接，不自动迁移。
func NewExtractionJobsRepo(db *gorm.DB) (*ExtractionJobsRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &ExtractionJobsRepo{db: db}, nil
}

// 编译期断言：必须满足 worker 依赖的队列接口。
var _ extractworker.JobSource = (*ExtractionJobsRepo)(nil)

// extractionJobRow 是任务表的行投影。
type extractionJobRow struct {
	EventID    string
	Version    int
	Status     string
	Attempts   int
	LeaseOwner *string
	LeaseUntil *time.Time
	LastError  *string
}

func (row extractionJobRow) job() extractworker.Job {
	job := extractworker.Job{
		EventID:  row.EventID,
		Version:  row.Version,
		Status:   extractworker.JobStatus(row.Status),
		Attempts: row.Attempts,
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

// Enqueue 把「还没入队的有价值入向事件」补成 pending 任务。
//
// 「有价值」= 入向且正文非空。**刻意不看 status**：
// 未回复的 pending、被调度跳过的 skipped、回复失败的 dead 都可能有记忆价值；
// 晚完成的事件也不会因为时间早于水位而漏掉——任务表没有事件时间游标。
//
// 幂等：event_id 主键 + NOT EXISTS + ON CONFLICT DO NOTHING，重复调用不会多出行。
func (r *ExtractionJobsRepo) Enqueue(ctx context.Context, limit int) (int, error) {
	if ctx == nil || limit < 1 {
		return 0, store.ErrInvalid
	}
	if limit > extractionEnqueueLimitMax {
		limit = extractionEnqueueLimitMax
	}
	var inserted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`
INSERT INTO extraction_jobs (event_id, extraction_version, status)
SELECT e.id, ?, 'pending'
FROM events e
WHERE e.dir = 'in'
  AND btrim(e.text) <> ''
  AND NOT EXISTS (SELECT 1 FROM extraction_jobs j WHERE j.event_id = e.id)
ORDER BY e.created_at, e.id
LIMIT ?
ON CONFLICT (event_id) DO NOTHING`, extractworker.ExtractionVersion, limit)
		if res.Error != nil {
			return res.Error
		}
		inserted = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, &safeError{"入队提炼任务失败", err}
	}
	return int(inserted), nil
}

// Claim 领取一批到期任务：pending，或 running 但租约已过期（被遗弃）。
//
// 用 CTE + FOR UPDATE SKIP LOCKED 完成有界抢占，不依赖进程内去重；
// attempts 在领取时 +1，反映「这条任务被尝试处理过几次」。
func (r *ExtractionJobsRepo) Claim(ctx context.Context, owner string, ttl time.Duration, limit int) ([]extractworker.Job, error) {
	if ctx == nil || strings.TrimSpace(owner) == "" || len(owner) > 128 || !validTTL(ttl) || limit < 1 {
		return nil, store.ErrInvalid
	}
	if limit > extractionClaimLimitMax {
		limit = extractionClaimLimitMax
	}
	var rows []extractionJobRow
	err := r.db.WithContext(ctx).Raw(`
WITH picked AS (
    SELECT event_id FROM extraction_jobs
    WHERE status = 'pending'
       OR (status = 'running' AND lease_until <= clock_timestamp())
    ORDER BY created_at, event_id
    LIMIT ? FOR UPDATE SKIP LOCKED
)
UPDATE extraction_jobs j
SET status = 'running',
    lease_owner = ?,
    lease_until = clock_timestamp() + (? * interval '1 microsecond'),
    attempts = j.attempts + 1,
    updated_at = clock_timestamp()
FROM picked p
WHERE j.event_id = p.event_id
RETURNING j.event_id, j.extraction_version, j.status, j.attempts, j.lease_owner, j.lease_until, j.last_error`,
		limit, owner, ttl.Microseconds()).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"领取提炼任务失败", err}
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// 一次性把事件正文带回来：worker 不需要再按 id 单查。
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EventID)
	}
	var evRows []eventRow
	if err := r.db.WithContext(ctx).Table("events").
		Select("id,channel,dir,session,from_id,from_name,text,private,reply_to,at,status,meta").
		Where("id IN ?", ids).
		Find(&evRows).Error; err != nil {
		return nil, &safeError{"读取提炼任务事件失败", err}
	}
	events := make(map[string]extractworker.Job, len(evRows))
	for _, row := range evRows {
		events[row.ID] = extractworker.Job{Event: row.event()}
	}

	jobs := make([]extractworker.Job, 0, len(rows))
	for _, row := range rows {
		job := row.job()
		if withEvent, ok := events[job.EventID]; ok {
			job.Event = withEvent.Event
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// Complete 标记任务完成并释放租约。
//
// 条件里带租约栅栏：租约过期后可能已被别的 worker 重新领取，
// 此时旧 worker 的完成结果不能覆盖新状态。
func (r *ExtractionJobsRepo) Complete(ctx context.Context, job extractworker.Job) error {
	if ctx == nil || strings.TrimSpace(job.EventID) == "" || strings.TrimSpace(job.LeaseOwner) == "" {
		return store.ErrInvalid
	}
	res := r.db.WithContext(ctx).Exec(`
UPDATE extraction_jobs
SET status = 'done', lease_owner = NULL, lease_until = NULL,
    last_error = NULL, updated_at = clock_timestamp()
WHERE event_id = ? AND status = 'running' AND lease_owner = ?
  AND lease_until > clock_timestamp()`, job.EventID, job.LeaseOwner)
	if res.Error != nil {
		return &safeError{"完成任务失败", res.Error}
	}
	if res.RowsAffected == 0 {
		return store.ErrConflict
	}
	return nil
}

// Fail 记录失败：可重试且未超上限回 pending，否则置 failed。
//
// 无论哪种分支都释放租约；回 pending 的任务会在下一轮被重新领取，
// 而不是立刻重试——worker 在失败后会结束本轮，避免紧密循环。
func (r *ExtractionJobsRepo) Fail(ctx context.Context, job extractworker.Job, reason string, retryable bool, maxAttempts int) error {
	if ctx == nil || strings.TrimSpace(job.EventID) == "" || strings.TrimSpace(job.LeaseOwner) == "" {
		return store.ErrInvalid
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	next := extractworker.JobFailed
	if retryable && job.Attempts < maxAttempts {
		next = extractworker.JobPending
	}
	message := truncateError(reason)
	res := r.db.WithContext(ctx).Exec(`
UPDATE extraction_jobs
SET status = ?, lease_owner = NULL, lease_until = NULL,
    last_error = ?, updated_at = clock_timestamp()
WHERE event_id = ? AND status = 'running' AND lease_owner = ?`,
		string(next), message, job.EventID, job.LeaseOwner)
	if res.Error != nil {
		return &safeError{"记录提炼任务失败", res.Error}
	}
	if res.RowsAffected == 0 {
		return store.ErrConflict
	}
	return nil
}

// Renew 续租；处理时间可能超过初始 TTL（LLM 提炼较慢）。
func (r *ExtractionJobsRepo) Renew(ctx context.Context, job extractworker.Job, ttl time.Duration) (extractworker.Job, error) {
	if ctx == nil || strings.TrimSpace(job.EventID) == "" || strings.TrimSpace(job.LeaseOwner) == "" || !validTTL(ttl) {
		return extractworker.Job{}, store.ErrInvalid
	}
	var rows []extractionJobRow
	err := r.db.WithContext(ctx).Raw(`
UPDATE extraction_jobs
SET lease_until = clock_timestamp() + (? * interval '1 microsecond'), updated_at = clock_timestamp()
WHERE event_id = ? AND status = 'running' AND lease_owner = ?
  AND lease_until > clock_timestamp()
RETURNING event_id, extraction_version, status, attempts, lease_owner, lease_until, last_error`,
		ttl.Microseconds(), job.EventID, job.LeaseOwner).Scan(&rows).Error
	if err != nil {
		return extractworker.Job{}, &safeError{"续租提炼任务失败", err}
	}
	if len(rows) == 0 {
		return extractworker.Job{}, store.ErrConflict
	}
	return rows[0].job(), nil
}

// truncateError 截断错误原因，避免超长报错撑爆 last_error。
func truncateError(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "未知错误"
	}
	runes := []rune(reason)
	if len(runes) <= 500 {
		return reason
	}
	return string(runes[:500])
}
