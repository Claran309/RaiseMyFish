package database

// 本文件实现观众档案：读接口 + 从 events 聚合重建。
//
// 聚合口径（派发单）：
//   - in 事件按 from_id 分组：hits 计数、recent_texts（最近 N 条）
//   - out 事件按 reply_to 关联回 in 事件，再按 from_id 分组：replies 计数、recent_replies（最近 M 条）
//   - 只给 hits ≥ minHits 的人建档（一次路过不建）

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// ViewerRepo 是 viewer_profiles 的仓储实现。
type ViewerRepo struct{ db *gorm.DB }

// NewViewerRepo 注入现有连接，不自动迁移。
func NewViewerRepo(db *gorm.DB) (*ViewerRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &ViewerRepo{db: db}, nil
}

type viewerRow struct {
	UID                  string
	Name                 string
	Hits                 int
	Replies              int
	LastSeen             time.Time
	RecentTexts          []byte
	RecentReplies        []byte
	ProfileSummary       string
	SummaryUpdatedAt     *time.Time
	SummarySourceHash    string     `gorm:"column:profile_summary_source_hash"`
	SummarySourceIDs     []byte     `gorm:"column:profile_summary_source_ids"`
	SummaryGenerator     string     `gorm:"column:profile_summary_generator"`
	SummaryKeywords      []byte     `gorm:"column:profile_summary_keywords"`
	SummaryLastSuccessAt *time.Time `gorm:"column:profile_summary_last_success_at"`
	SummaryFallbackAt    *time.Time `gorm:"column:profile_summary_fallback_at"`
	SummaryFailures      int        `gorm:"column:profile_summary_failures"`
	SummaryNextRetryAt   *time.Time `gorm:"column:profile_summary_next_retry_at"`
	SummaryAttemptedAt   *time.Time `gorm:"column:summary_attempted_at"`
	SummaryStable        string     `gorm:"column:profile_summary_stable"`
	SummaryRecent        string     `gorm:"column:profile_summary_recent"`
	SummaryRecentUntil   *time.Time `gorm:"column:profile_summary_recent_until"`
	SummaryFields        []byte     `gorm:"column:profile_summary_fields"`
	UpdatedAt            time.Time
}

func (r viewerRow) profile() (store.ViewerProfile, error) {
	p := store.ViewerProfile{
		UID: r.UID, Name: r.Name, Hits: r.Hits, Replies: r.Replies,
		LastSeen: r.LastSeen, ProfileSummary: r.ProfileSummary, UpdatedAt: r.UpdatedAt,
		SummarySourceHash: r.SummarySourceHash, SummaryGenerator: r.SummaryGenerator,
		SummaryFailures: r.SummaryFailures,
	}
	if r.SummaryUpdatedAt != nil {
		p.SummaryUpdatedAt = *r.SummaryUpdatedAt
	}
	if r.SummaryLastSuccessAt != nil {
		p.SummaryLastSuccessAt = *r.SummaryLastSuccessAt
	}
	if r.SummaryFallbackAt != nil {
		p.SummaryFallbackAt = *r.SummaryFallbackAt
	}
	if r.SummaryNextRetryAt != nil {
		p.SummaryNextRetryAt = *r.SummaryNextRetryAt
	}
	if r.SummaryAttemptedAt != nil {
		p.SummaryAttemptedAt = *r.SummaryAttemptedAt
	}
	if r.SummaryRecentUntil != nil {
		p.SummaryRecentUntil = *r.SummaryRecentUntil
	}
	p.SummaryStable = r.SummaryStable
	p.SummaryRecent = r.SummaryRecent
	if len(r.SummaryFields) > 0 {
		if err := json.Unmarshal(r.SummaryFields, &p.SummaryFields); err != nil {
			return store.ViewerProfile{}, &safeError{"解析侧写字段失败", err}
		}
	}
	if err := decodeStrings(r.RecentTexts, &p.RecentTexts); err != nil {
		return store.ViewerProfile{}, &safeError{"解析观众发言失败", err}
	}
	if err := decodeStrings(r.RecentReplies, &p.RecentReplies); err != nil {
		return store.ViewerProfile{}, &safeError{"解析回复快照失败", err}
	}
	if err := decodeStrings(r.SummarySourceIDs, &p.SummarySourceIDs); err != nil {
		return store.ViewerProfile{}, &safeError{"解析侧写来源失败", err}
	}
	if err := decodeStrings(r.SummaryKeywords, &p.SummaryKeywords); err != nil {
		return store.ViewerProfile{}, &safeError{"解析侧写关键词失败", err}
	}
	return p, nil
}

func decodeStrings(raw []byte, out *[]string) error {
	if len(raw) == 0 {
		*out = nil
		return nil
	}
	return json.Unmarshal(raw, out)
}

// UpsertProfile 按 uid 覆盖写入。
func (r *ViewerRepo) UpsertProfile(ctx context.Context, p store.ViewerProfile) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return err
	}
	texts, err := json.Marshal(nonNil(p.RecentTexts))
	if err != nil {
		return store.ErrInvalid
	}
	replies, err := json.Marshal(nonNil(p.RecentReplies))
	if err != nil {
		return store.ErrInvalid
	}
	err = r.db.WithContext(ctx).Exec(`
INSERT INTO viewer_profiles(uid,name,hits,replies,last_seen,recent_texts,recent_replies,updated_at)
VALUES (?,?,?,?,?,?::jsonb,?::jsonb,clock_timestamp())
ON CONFLICT (uid) DO UPDATE SET
  name=EXCLUDED.name, hits=EXCLUDED.hits, replies=EXCLUDED.replies,
  last_seen=EXCLUDED.last_seen, recent_texts=EXCLUDED.recent_texts,
  recent_replies=EXCLUDED.recent_replies, updated_at=clock_timestamp()`,
		p.UID, p.Name, p.Hits, p.Replies, p.LastSeen.UTC(), string(texts), string(replies)).Error
	if err != nil {
		return &safeError{"写入观众档案失败", err}
	}
	return nil
}

// GetProfile 按 uid 读档案；陌生人返回 store.ErrViewerNotFound
// （errors.Is 同时匹配 store.ErrNotFound）。
func (r *ViewerRepo) GetProfile(ctx context.Context, uid string) (store.ViewerProfile, error) {
	if ctx == nil || strings.TrimSpace(uid) == "" {
		return store.ViewerProfile{}, store.ErrInvalid
	}
	var row viewerRow
	err := r.db.WithContext(ctx).Table("viewer_profiles").Where("uid = ?", uid).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ViewerProfile{}, store.ErrViewerNotFound
	}
	if err != nil {
		return store.ViewerProfile{}, &safeError{"读取观众档案失败", err}
	}
	return row.profile()
}

// TopViewers 按 hits 倒序取前 limit 个档案。
//
// 上限放宽到 500：身份近似匹配（错字/谐音）需要在一份较大的昵称池里找人，
// 档案总量目前只有两百人左右，一次性读回内存做编辑距离是可接受的。
func (r *ViewerRepo) TopViewers(ctx context.Context, limit int) ([]store.ViewerProfile, error) {
	if ctx == nil || limit < 1 || limit > 500 {
		return nil, store.ErrInvalid
	}
	var rows []viewerRow
	err := r.db.WithContext(ctx).Table("viewer_profiles").
		Order("hits DESC, last_seen DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取观众榜失败", err}
	}
	out := make([]store.ViewerProfile, 0, len(rows))
	for _, row := range rows {
		p, err := row.profile()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ActiveSince 判断 since 之后是否有公开入向事件（用来判断直播是否活跃）。
//
// DM 和私密会话不算公开活跃度：否则只有私信时也会触发档案重建。
func (r *ViewerRepo) ActiveSince(ctx context.Context, since time.Time) (bool, error) {
	if ctx == nil || since.IsZero() {
		return false, store.ErrInvalid
	}
	var exists bool
	err := r.db.WithContext(ctx).Raw(
		"SELECT EXISTS(SELECT 1 FROM events WHERE dir = 'in' AND at >= ? AND NOT private AND channel <> 'dm')", since.UTC()).Scan(&exists).Error
	if err != nil {
		return false, &safeError{"检查直播活跃度失败", err}
	}
	return exists, nil
}

// ActiveViewers 返回本轮该做侧写的观众档案（R06/R09）。
//
// 选人分两条路：
//   - 更新/补偿：已有侧写的档案，只有「侧写之后新增公开消息达到 MinNewMessages」
//     或补偿重试到期才入选；失败达到上限后 next_retry 为空，必须等新发言。
//   - 首次回填：从未侧写过的档案，按 hits 倒序先试熟人，且每轮最多
//     BackfillLimit 个；0 表示本轮不回填，避免一次性对全库跑模型。
//
// 两条路都受建档口径约束：since 之后有公开发言，或累计说过 MinHits 句。
func (r *ViewerRepo) ActiveViewers(ctx context.Context, q store.ViewerSummaryQuery) ([]store.ViewerProfile, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	var rows []viewerRow
	err := r.db.WithContext(ctx).Table("viewer_profiles").
		Where("(last_seen >= ? OR hits >= ?)", q.Since.UTC(), q.MinHits).
		Where("(profile_summary_next_retry_at IS NULL OR profile_summary_next_retry_at <= ?)", q.Now.UTC()).
		Where(`(summary_updated_at IS NOT NULL AND (
		    profile_summary_next_retry_at IS NOT NULL
		    OR ((summary_attempted_at IS NULL OR summary_attempted_at < last_seen)
		      AND (SELECT COUNT(*) FROM events e
		           WHERE e.from_id = viewer_profiles.uid AND e.dir = 'in' AND NOT e.private
		             AND e.channel <> 'dm' AND e.at > viewer_profiles.summary_updated_at) >= ?)
		  ))`, q.MinNewMessages).
		Order("CASE WHEN profile_summary_next_retry_at IS NOT NULL THEN 0 ELSE 1 END, " +
			"profile_summary_next_retry_at ASC NULLS LAST, last_seen DESC, uid ASC").
		Limit(q.Limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取待更新观众失败", err}
	}
	out := make([]store.ViewerProfile, 0, len(rows))
	for _, row := range rows {
		p, err := row.profile()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}

	remaining := q.Limit - len(out)
	backfill := q.BackfillLimit
	if backfill > remaining {
		backfill = remaining
	}
	if backfill < 1 {
		return out, nil
	}
	var fresh []viewerRow
	err = r.db.WithContext(ctx).Table("viewer_profiles").
		Where("(last_seen >= ? OR hits >= ?)", q.Since.UTC(), q.MinHits).
		Where("summary_updated_at IS NULL").
		Where("(profile_summary_next_retry_at IS NULL OR profile_summary_next_retry_at <= ?)", q.Now.UTC()).
		Where("(profile_summary_next_retry_at IS NOT NULL OR summary_attempted_at IS NULL OR summary_attempted_at < last_seen)").
		Order("hits DESC, last_seen DESC, uid ASC").
		Limit(backfill).Find(&fresh).Error
	if err != nil {
		return nil, &safeError{"读取首次回填观众失败", err}
	}
	for _, row := range fresh {
		p, err := row.profile()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ExpireProfileSummaryRecent 清掉已过期的近期层，并同步记忆与投影任务（R06）。
//
// 过期只撤回「近期观察」，稳定自述继续保留：档案文本退回稳定层；
// 稳定层也为空时记忆行归档并登记 purge，否则更新记忆行并登记 publish。
func (r *ViewerRepo) ExpireProfileSummaryRecent(ctx context.Context, now time.Time) ([]string, error) {
	if ctx == nil || now.IsZero() {
		return nil, store.ErrInvalid
	}
	var expired []string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			UID    string
			Stable string
			Fields []byte
		}
		err := tx.Raw(`SELECT uid, profile_summary_stable AS stable, to_json(profile_summary_fields) AS fields
FROM viewer_profiles
WHERE profile_summary_recent <> '' AND profile_summary_recent_until IS NOT NULL
  AND profile_summary_recent_until <= ?
ORDER BY uid
FOR UPDATE`, now.UTC()).Scan(&rows).Error
		if err != nil {
			return err
		}
		for _, row := range rows {
			var fields []store.ViewerSummaryField
			if len(row.Fields) > 0 {
				if err := json.Unmarshal(row.Fields, &fields); err != nil {
					return err
				}
			}
			stableFields := make([]store.ViewerSummaryField, 0, len(fields))
			evidence := make([]store.MemoryEvidence, 0, len(fields))
			for _, field := range fields {
				if field.Layer != store.ViewerSummaryLayerStable {
					continue
				}
				stableFields = append(stableFields, field)
				if strings.TrimSpace(field.Evidence) != "" {
					evidence = append(evidence, store.MemoryEvidence{Field: field.Name, Type: field.Basis, Text: field.Evidence})
				}
			}
			encodedFields, err := json.Marshal(nonNilFields(stableFields))
			if err != nil {
				return err
			}
			encodedEvidence, err := json.Marshal(evidence)
			if err != nil {
				return err
			}
			if strings.TrimSpace(row.Stable) == "" {
				// 只有近期层：过期后档案内容整体清空，summary_updated_at 归零表示当前没有侧写。
				if err := tx.Exec(`UPDATE viewer_profiles SET
  profile_summary='', summary_updated_at=NULL,
  profile_summary_stable='', profile_summary_recent='', profile_summary_recent_until=NULL,
  profile_summary_fields='[]'::jsonb, updated_at=clock_timestamp()
WHERE uid=?`, row.UID).Error; err != nil {
					return err
				}
				if err := tx.Exec(`UPDATE memory_records SET archived = TRUE
WHERE id = ? AND kind = 'viewer_profile' AND NOT archived`, "viewer-profile:"+row.UID).Error; err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO viewer_summary_jobs(uid,action,attempts,next_retry_at,created_at,updated_at)
VALUES (?,?,0,clock_timestamp(),clock_timestamp(),clock_timestamp())
ON CONFLICT (uid) DO UPDATE SET
  action='purge', attempts=0, next_retry_at=clock_timestamp(), updated_at=clock_timestamp()`,
					row.UID, store.ViewerSummaryActionPurge).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Exec(`UPDATE viewer_profiles SET
  profile_summary=?, summary_updated_at=clock_timestamp(),
  profile_summary_recent='', profile_summary_recent_until=NULL,
  profile_summary_fields=?::jsonb, updated_at=clock_timestamp()
WHERE uid=?`, row.Stable, string(encodedFields), row.UID).Error; err != nil {
					return err
				}
				if err := tx.Exec(`UPDATE memory_records SET summary=?, evidence=?::jsonb, archived=FALSE
WHERE id = ? AND kind = 'viewer_profile'`, row.Stable, string(encodedEvidence), "viewer-profile:"+row.UID).Error; err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO viewer_summary_jobs(uid,action,attempts,next_retry_at,created_at,updated_at)
VALUES (?,?,0,clock_timestamp(),clock_timestamp(),clock_timestamp())
ON CONFLICT (uid) DO UPDATE SET
  action='publish', attempts=0, next_retry_at=clock_timestamp(), updated_at=clock_timestamp()`,
					row.UID, store.ViewerSummaryActionPublish).Error; err != nil {
					return err
				}
			}
			expired = append(expired, row.UID)
		}
		return nil
	})
	if err != nil {
		return nil, &safeError{"清理过期观众侧写失败", err}
	}
	return expired, nil
}

// ForegroundBusy 判断近期是否有正在生成的回复（R09）。
//
// 侧写是后台任务，前台回复（status=replying）繁忙时应让路：
// 调用方据此跳过本轮生成，撤回/过期/投影等纠正动作不受影响。
func (r *ViewerRepo) ForegroundBusy(ctx context.Context, since time.Time) (bool, error) {
	if ctx == nil || since.IsZero() {
		return false, store.ErrInvalid
	}
	var busy bool
	err := r.db.WithContext(ctx).Raw(
		"SELECT EXISTS(SELECT 1 FROM events WHERE status = ? AND status_changed_at >= ?)",
		event.StatusReplying, since.UTC()).Scan(&busy).Error
	if err != nil {
		return false, &safeError{"检查前台回复忙碌状态失败", err}
	}
	return busy, nil
}

// SummaryAttemptsSince 统计 since 之后尝试过侧写的观众数（按 summary_attempted_at）。
//
// 同一观众当天多次尝试只计一次：日预算按「观众数」而不是调用次数控制。
func (r *ViewerRepo) SummaryAttemptsSince(ctx context.Context, since time.Time) (int, error) {
	if ctx == nil || since.IsZero() {
		return 0, store.ErrInvalid
	}
	var count int
	err := r.db.WithContext(ctx).Raw(
		"SELECT COUNT(*) FROM viewer_profiles WHERE summary_attempted_at IS NOT NULL AND summary_attempted_at >= ?",
		since.UTC()).Scan(&count).Error
	if err != nil {
		return 0, &safeError{"统计观众侧写日预算失败", err}
	}
	return count, nil
}

// SummaryPanel 汇总面板需要的侧写计数与最近成功时间（R08）。
//
// 只返回计数与时间，不返回侧写正文或来源事件 ID；正文由记忆/档案接口按权限读取。
func (r *ViewerRepo) SummaryPanel(ctx context.Context) (store.ViewerSummaryPanel, error) {
	var out store.ViewerSummaryPanel
	if ctx == nil {
		return out, store.ErrInvalid
	}
	var last sql.NullTime
	err := r.db.WithContext(ctx).Raw(`SELECT
  COUNT(*) FILTER (WHERE profile_summary <> '') AS summarized,
  COUNT(*) FILTER (WHERE profile_summary <> '' AND profile_summary_failures > 0) AS failed,
  COUNT(*) FILTER (WHERE profile_summary <> '' AND profile_summary_last_success_at IS NULL) AS fallback,
  COALESCE(SUM(jsonb_array_length(profile_summary_source_ids)) FILTER (WHERE profile_summary <> ''), 0) AS sources,
  MAX(profile_summary_last_success_at) AS last_summary_at,
  (SELECT COUNT(*) FROM viewer_summary_jobs WHERE action = 'publish') AS pending_publish,
  (SELECT COUNT(*) FROM viewer_summary_jobs WHERE action = 'purge') AS pending_purge
FROM viewer_profiles`).Row().Scan(
		&out.Summarized, &out.Failed, &out.Fallback, &out.Sources, &last,
		&out.PendingPublish, &out.PendingPurge)
	if err != nil {
		return out, &safeError{"统计观众侧写状态失败", err}
	}
	if last.Valid {
		out.LastSummaryAt = last.Time
	}
	return out, nil
}

// RequeueSummary 把一位观众的侧写标记为「下一轮重建」。
//
// 只把补偿时间提前，不伪造尝试时间：重建仍受日预算与前台让路约束，
// 且 ActiveViewers 会把它当作补偿到期优先处理。
func (r *ViewerRepo) RequeueSummary(ctx context.Context, uid string) error {
	if ctx == nil || strings.TrimSpace(uid) == "" {
		return store.ErrInvalid
	}
	res := r.db.WithContext(ctx).Exec(
		"UPDATE viewer_profiles SET profile_summary_next_retry_at = clock_timestamp(), summary_attempted_at = NULL WHERE uid = ?", uid)
	if res.Error != nil {
		return &safeError{"标记观众侧写重建失败", res.Error}
	}
	if res.RowsAffected != 1 {
		return store.ErrViewerNotFound
	}
	return nil
}

// RevokeSummary 立即撤回单个观众的侧写：档案清空、记忆归档、登记 purge 投影任务。
//
// 「立即不可见」在同一个事务里完成；viewers.md 与向量由 purge 任务异步清理。
func (r *ViewerRepo) RevokeSummary(ctx context.Context, uid string) error {
	if ctx == nil || strings.TrimSpace(uid) == "" {
		return store.ErrInvalid
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE TEMP TABLE IF NOT EXISTS viewer_summary_revoked(uid TEXT PRIMARY KEY) ON COMMIT DROP").Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM viewer_summary_revoked").Error; err != nil {
			return err
		}
		res := tx.Exec("INSERT INTO viewer_summary_revoked(uid) SELECT uid FROM viewer_profiles WHERE uid = ?", uid)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return store.ErrViewerNotFound
		}
		if err := tx.Exec(`UPDATE viewer_profiles SET
  profile_summary = '', summary_updated_at = NULL,
  profile_summary_source_hash = '', profile_summary_source_ids = '[]'::jsonb,
  profile_summary_generator = '', profile_summary_keywords = '[]'::jsonb,
  profile_summary_stable = '', profile_summary_recent = '',
  profile_summary_recent_until = NULL, profile_summary_fields = '[]'::jsonb,
  profile_summary_last_success_at = NULL, profile_summary_fallback_at = NULL,
  profile_summary_failures = 0, profile_summary_next_retry_at = NULL,
  summary_attempted_at = NULL
WHERE uid = ?`, uid).Error; err != nil {
			return err
		}
		_, err := finalizeRevokedSummaries(tx)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrViewerNotFound) {
			return store.ErrViewerNotFound
		}
		return &safeError{"撤回观众侧写失败", err}
	}
	return nil
}

// SaveProfileSummary 在同一个事务里校验来源、写入档案侧写与对应的记忆记录，并登记投影任务。
//
// 来源 CAS：按生成时的事件顺序重查 events，重算 store.ViewerSummarySourceHash；
// 任何来源被删除、改密、移出公开入向或正文变化都返回 store.ErrViewerSourceChanged。
// 记忆记录用稳定 ID 覆盖同一观众的旧摘要；冲突更新只允许 viewer_profile → viewer_profile，
// 不会误改其他 kind 的同 ID 行。记录身份（private/subject/asserted_by）在这里再核一遍。
func (r *ViewerRepo) SaveProfileSummary(ctx context.Context, w store.ViewerSummaryWrite) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	if err := w.Validate(); err != nil {
		return err
	}
	// asserted_by 只允许明确自述：派生侧写必须留空，不能被写成「观众本人断言」。
	if w.Record.Kind != "viewer_profile" || w.Record.Private || w.Record.Subject != memory.SoulSubjectViewer ||
		w.Record.FromID != w.UID || (w.Record.AssertedByID != "" && w.Record.AssertedByID != w.UID) {
		return store.ErrInvalid
	}
	ids, err := json.Marshal(w.Record.EventIDs)
	if err != nil {
		return store.ErrInvalid
	}
	keywords, err := json.Marshal(nonNil(w.Keywords))
	if err != nil {
		return store.ErrInvalid
	}
	fields, err := json.Marshal(nonNilFields(w.Fields))
	if err != nil {
		return store.ErrInvalid
	}
	evidence, err := json.Marshal(w.Record.Evidence)
	if err != nil {
		return store.ErrInvalid
	}
	if string(evidence) == "null" {
		evidence = []byte("[]")
	}
	at := w.At.UTC()
	var nextRetry any
	if !w.NextRetryAt.IsZero() {
		nextRetry = w.NextRetryAt.UTC()
	}
	var recentUntil any
	if !w.RecentUntil.IsZero() {
		recentUntil = w.RecentUntil.UTC()
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := verifySummarySources(tx, w.UID, w.Sources, w.SourceHash, w.Generator); err != nil {
			return err
		}
		res := tx.Exec(`UPDATE viewer_profiles SET
  profile_summary=?, summary_updated_at=?,
  profile_summary_source_hash=?, profile_summary_source_ids=?::jsonb,
  profile_summary_generator=?, profile_summary_keywords=?::jsonb,
  profile_summary_stable=?, profile_summary_recent=?, profile_summary_recent_until=?,
  profile_summary_fields=?::jsonb,
  summary_attempted_at=?, profile_summary_failures=?, profile_summary_next_retry_at=?,
  profile_summary_last_success_at=CASE WHEN ? THEN profile_summary_last_success_at ELSE ? END,
  profile_summary_fallback_at=CASE WHEN ? THEN ? ELSE profile_summary_fallback_at END
WHERE uid=?`,
			w.Summary, at, w.SourceHash, string(ids), w.Generator, string(keywords),
			w.Stable, w.Recent, recentUntil, string(fields),
			at, w.Failures, nextRetry,
			w.Fallback, at, w.Fallback, at, w.UID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("观众档案不存在: %w", store.ErrViewerNotFound)
		}
		res = tx.Exec(`INSERT INTO memory_records
(id,source,kind,title,summary,body,importance,confidence,private,channel,session,valid_from,event_ids,from_id,from_name,asserted_by_id,asserted_by_name,subject,derived_by,evidence)
VALUES (?,?,?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,ARRAY(SELECT jsonb_array_elements_text(NULLIF(?::jsonb,'null'::jsonb))),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?::jsonb)
ON CONFLICT (id) DO UPDATE SET
  source = EXCLUDED.source,
  title = EXCLUDED.title,
  summary = EXCLUDED.summary,
  body = EXCLUDED.body,
  importance = EXCLUDED.importance,
  confidence = EXCLUDED.confidence,
  valid_from = EXCLUDED.valid_from,
  event_ids = EXCLUDED.event_ids,
  from_id = COALESCE(EXCLUDED.from_id, memory_records.from_id),
  from_name = COALESCE(EXCLUDED.from_name, memory_records.from_name),
  asserted_by_id = EXCLUDED.asserted_by_id,
  asserted_by_name = EXCLUDED.asserted_by_name,
  subject = COALESCE(EXCLUDED.subject, memory_records.subject),
  derived_by = COALESCE(NULLIF(EXCLUDED.derived_by,''), memory_records.derived_by),
  evidence = CASE WHEN EXCLUDED.evidence = '[]'::jsonb THEN memory_records.evidence ELSE EXCLUDED.evidence END,
  archived = FALSE
WHERE memory_records.kind = 'viewer_profile'
  AND EXCLUDED.kind = 'viewer_profile'
  AND memory_records.private = EXCLUDED.private`,
			w.Record.ID, w.Record.Source, w.Record.Kind, w.Record.Title, w.Record.Summary, w.Record.Body,
			w.Record.Importance, w.Record.Confidence, w.Record.Private, w.Record.Channel, w.Record.Session,
			w.Record.ValidFrom, string(ids), w.Record.FromID, w.Record.FromName,
			w.Record.AssertedByID, w.Record.AssertedByName, w.Record.Subject, w.Record.DerivedBy, string(evidence))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("观众侧写记忆未写入：标识冲突或记录不可覆盖")
		}
		// 投影任务与侧写同事务登记：知识文件失败时下轮只补投影，不再付费调模型。
		res = tx.Exec(`INSERT INTO viewer_summary_jobs(uid,action,attempts,next_retry_at,created_at,updated_at)
VALUES (?,?,0,clock_timestamp(),clock_timestamp(),clock_timestamp())
ON CONFLICT (uid) DO UPDATE SET
  action='publish', attempts=0, next_retry_at=clock_timestamp(), updated_at=clock_timestamp()`,
			w.UID, store.ViewerSummaryActionPublish)
		if res.Error != nil {
			return res.Error
		}
		return nil
	})
	if err != nil {
		return &safeError{"保存观众侧写失败", err}
	}
	return nil
}

// DeferProfileSummary 记录一次保留旧版本后的失败与补偿时间。
//
// 只推进尝试/失败/重试状态，不改 profile_summary 与 summary_updated_at，
// 旧的好版本继续供注入使用；NextRetryAt 为零表示达到重试上限，等新发言再试。
func (r *ViewerRepo) DeferProfileSummary(ctx context.Context, d store.ViewerSummaryDefer) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	if err := d.Validate(); err != nil {
		return err
	}
	var nextRetry any
	if !d.NextRetryAt.IsZero() {
		nextRetry = d.NextRetryAt.UTC()
	}
	res := r.db.WithContext(ctx).Exec(`UPDATE viewer_profiles SET
  summary_attempted_at=?, profile_summary_failures=?, profile_summary_next_retry_at=?
WHERE uid=?`, d.At.UTC(), d.Failures, nextRetry, d.UID)
	if res.Error != nil {
		return &safeError{"记录观众侧写失败状态", res.Error}
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("观众档案不存在: %w", store.ErrViewerNotFound)
	}
	return nil
}

// summarySourceRow 是来源 CAS 校验用的事件行。
type summarySourceRow struct {
	ID      string
	Text    string
	Dir     string
	Channel string
	FromID  string
	Private bool
	At      time.Time
}

// verifySummarySources 在事务内重查并重算来源指纹；任何不一致都视为来源已变。
func verifySummarySources(tx *gorm.DB, uid string, sources []store.ViewerSummarySource, sourceHash, generator string) error {
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return store.ErrInvalid
	}
	var rows []summarySourceRow
	err = tx.Raw(`SELECT id,text,dir,channel,from_id,private,at FROM events
WHERE id IN (SELECT jsonb_array_elements_text(?::jsonb))`, string(raw)).Scan(&rows).Error
	if err != nil {
		return err
	}
	if len(rows) != len(sources) {
		return store.ErrViewerSourceChanged
	}
	actual := make(map[string]summarySourceRow, len(rows))
	for _, row := range rows {
		actual[row.ID] = row
	}
	checked := make([]store.ViewerSummarySource, 0, len(sources))
	for _, source := range sources {
		row, ok := actual[source.ID]
		if !ok || row.Dir != string(event.DirIn) || row.Private ||
			row.Channel == string(event.ChDM) || row.FromID != uid {
			return store.ErrViewerSourceChanged
		}
		checked = append(checked, store.ViewerSummarySource{ID: row.ID, Text: row.Text, At: row.At})
	}
	if store.ViewerSummarySourceHash(uid, generator, checked) != sourceHash {
		return store.ErrViewerSourceChanged
	}
	return nil
}

// revokeSummarySQL 把来源失效的侧写清空，并把 UID 收进调用方的事务临时表。
//
// 没有来源指纹的旧侧写（迁移前生成）也视为失效：无法验证来源就不应继续公开。
const revokeSummarySQL = `
WITH bad AS (
    SELECT vp.uid
    FROM viewer_profiles vp
    WHERE vp.profile_summary <> ''
      AND (
        vp.profile_summary_source_hash = ''
        OR vp.profile_summary_generator = ''
        OR jsonb_array_length(vp.profile_summary_source_ids) = 0
        OR EXISTS (
            SELECT 1
            FROM jsonb_array_elements_text(vp.profile_summary_source_ids) AS s(eid)
            LEFT JOIN events e ON e.id = s.eid
            WHERE e.id IS NULL OR e.private OR e.channel = 'dm'
               OR e.dir <> 'in' OR COALESCE(e.from_id, '') <> vp.uid
        )
      )
), cleared AS (
    UPDATE viewer_profiles vp SET
      profile_summary = '', summary_updated_at = NULL,
      profile_summary_source_hash = '', profile_summary_source_ids = '[]'::jsonb,
      profile_summary_generator = '', profile_summary_keywords = '[]'::jsonb,
      profile_summary_stable = '', profile_summary_recent = '',
      profile_summary_recent_until = NULL, profile_summary_fields = '[]'::jsonb,
      profile_summary_last_success_at = NULL, profile_summary_fallback_at = NULL,
      profile_summary_failures = 0, profile_summary_next_retry_at = NULL,
      summary_attempted_at = NULL
    FROM bad
    WHERE vp.uid = bad.uid
    RETURNING vp.uid
)
INSERT INTO viewer_summary_revoked(uid)
SELECT uid FROM cleared
ON CONFLICT (uid) DO NOTHING`

// finalizeRevokedSummaries 在事务内归档被撤回的记忆并登记 purge 投影任务，返回 UID。
func finalizeRevokedSummaries(tx *gorm.DB) ([]string, error) {
	if err := tx.Exec(`UPDATE memory_records SET archived = TRUE
WHERE id IN (SELECT 'viewer-profile:' || uid FROM viewer_summary_revoked) AND NOT archived`).Error; err != nil {
		return nil, err
	}
	if err := tx.Exec(`INSERT INTO viewer_summary_jobs(uid,action,attempts,next_retry_at,created_at,updated_at)
SELECT uid, 'purge', 0, clock_timestamp(), clock_timestamp(), clock_timestamp()
FROM viewer_summary_revoked
ON CONFLICT (uid) DO UPDATE SET
  action = 'purge', attempts = 0, next_retry_at = clock_timestamp(), updated_at = clock_timestamp()`).Error; err != nil {
		return nil, err
	}
	var revoked []string
	if err := tx.Raw("SELECT uid FROM viewer_summary_revoked ORDER BY uid").Scan(&revoked).Error; err != nil {
		return nil, err
	}
	return revoked, nil
}

// RevokeInvalidSummaries 扫描来源已失效的侧写并立即让其不可见。
//
// 可见性在同一个事务里切换：清空档案侧写、归档记忆行；知识文件与向量由
// purge 投影任务异步清理（调用方负责处理任务）。
func (r *ViewerRepo) RevokeInvalidSummaries(ctx context.Context) ([]string, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var revoked []string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE TEMP TABLE IF NOT EXISTS viewer_summary_revoked(uid TEXT PRIMARY KEY) ON COMMIT DROP").Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM viewer_summary_revoked").Error; err != nil {
			return err
		}
		if err := tx.Exec(revokeSummarySQL).Error; err != nil {
			return err
		}
		var err error
		revoked, err = finalizeRevokedSummaries(tx)
		return err
	})
	if err != nil {
		return nil, &safeError{"撤回失效观众侧写失败", err}
	}
	return revoked, nil
}

// PendingSummaryJobs 取到期投影任务；purge 优先于 publish，避免撤回后又被旧任务写回。
func (r *ViewerRepo) PendingSummaryJobs(ctx context.Context, now time.Time, limit int) ([]store.ViewerSummaryJob, error) {
	if ctx == nil || now.IsZero() || limit < 1 || limit > 500 {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		UID      string
		Action   string
		Attempts int
	}
	err := r.db.WithContext(ctx).Table("viewer_summary_jobs").
		Select("uid, action, attempts").
		Where("next_retry_at <= ?", now.UTC()).
		Order("CASE action WHEN 'purge' THEN 0 ELSE 1 END, next_retry_at ASC, uid ASC").
		Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取观众侧写投影任务失败", err}
	}
	out := make([]store.ViewerSummaryJob, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.ViewerSummaryJob{UID: row.UID, Action: row.Action, Attempts: row.Attempts})
	}
	return out, nil
}

// FinishSummaryJob 删除已完成的投影任务；动作已被覆盖（如 publish→purge）时视为已处理。
func (r *ViewerRepo) FinishSummaryJob(ctx context.Context, uid, action string) error {
	if ctx == nil || strings.TrimSpace(uid) == "" {
		return store.ErrInvalid
	}
	if err := r.db.WithContext(ctx).Exec("DELETE FROM viewer_summary_jobs WHERE uid=? AND action=?", uid, action).Error; err != nil {
		return &safeError{"完成观众侧写投影任务失败", err}
	}
	return nil
}

// FailSummaryJob 记录投影失败并推迟下次重试；动作已被覆盖时不覆盖新任务。
func (r *ViewerRepo) FailSummaryJob(ctx context.Context, uid, action string, attempts int, nextRetry time.Time) error {
	if ctx == nil || strings.TrimSpace(uid) == "" || attempts < 1 || nextRetry.IsZero() {
		return store.ErrInvalid
	}
	if err := r.db.WithContext(ctx).Exec(`UPDATE viewer_summary_jobs
SET attempts=?, next_retry_at=?, updated_at=clock_timestamp()
WHERE uid=? AND action=?`, attempts, nextRetry.UTC(), uid, action).Error; err != nil {
		return &safeError{"记录观众侧写投影失败失败", err}
	}
	return nil
}

// RebuildProfiles 从公开事件全量重算档案（幂等：ON CONFLICT 覆盖）。
//
// 口径（M01）：
//   - 只聚合公开、非 DM 的入向事件；DM 私信和错误降密的 DM 都不进公开档案；
//   - 出向只认已确认 done，且关联的入向与出向都必须是公开来源；
//   - 不再满足门槛的旧档案（例如私密来源被修正后）直接删除，连同其向量；
//   - 昵称/最近发言/最近回复变化时清空旧向量，等待重新入库，避免继续用污染向量召回。
func (r *ViewerRepo) RebuildProfiles(ctx context.Context, opts store.ViewerRebuildOptions) (store.ViewerRebuildStats, error) {
	if ctx == nil || opts.MinHits < 1 || opts.RecentTexts < 1 || opts.RecentReplies < 1 {
		return store.ViewerRebuildStats{}, store.ErrInvalid
	}
	const cleanupSQL = `
WITH inbound AS (
    SELECT from_id AS uid
    FROM events
    WHERE dir = 'in' AND NOT private AND channel <> 'dm'
      AND COALESCE(from_id, '') <> '' AND COALESCE(text, '') <> ''
    GROUP BY from_id
    HAVING COUNT(*) >= ?
), deleted AS (
    DELETE FROM viewer_profiles WHERE uid NOT IN (SELECT uid FROM inbound)
    RETURNING uid
)
INSERT INTO viewer_summary_revoked(uid)
SELECT uid FROM deleted
ON CONFLICT (uid) DO NOTHING`
	const upsertSQL = `
WITH inbound AS (
    SELECT from_id AS uid,
           COUNT(*) AS hits,
           MAX(at) AS last_seen,
           (ARRAY_AGG(text ORDER BY at DESC))[1:LEAST(?, COUNT(*))] AS texts,
           (ARRAY_AGG(from_name ORDER BY at DESC))[1] AS name
    FROM events
    WHERE dir = 'in' AND NOT private AND channel <> 'dm'
      AND COALESCE(from_id, '') <> '' AND COALESCE(text, '') <> ''
    GROUP BY from_id
    HAVING COUNT(*) >= ?
), outbound AS (
    SELECT i.from_id AS uid,
           COUNT(*) AS replies,
           (ARRAY_AGG(o.text ORDER BY o.at DESC))[1:LEAST(?, COUNT(*))] AS replies_texts
    FROM events o
    JOIN events i ON i.id = o.reply_to
    WHERE o.dir = 'out' AND o.status = 'done'
      AND NOT o.private AND o.channel <> 'dm'
      AND i.dir = 'in' AND NOT i.private AND i.channel <> 'dm'
      AND COALESCE(i.from_id, '') <> '' AND COALESCE(o.text, '') <> ''
    GROUP BY i.from_id
)
INSERT INTO viewer_profiles(uid, name, hits, replies, last_seen, recent_texts, recent_replies, updated_at)
SELECT b.uid,
       COALESCE(NULLIF(b.trim_name, ''), b.uid),
       b.hits,
       COALESCE(o.replies, 0),
       b.last_seen,
       to_jsonb(b.texts),
       COALESCE(to_jsonb(o.replies_texts), '[]'::jsonb),
       clock_timestamp()
FROM (SELECT uid, hits, last_seen, texts, btrim(name) AS trim_name FROM inbound) b
LEFT JOIN outbound o USING (uid)
ON CONFLICT (uid) DO UPDATE SET
  name = EXCLUDED.name,
  hits = EXCLUDED.hits,
  replies = EXCLUDED.replies,
  last_seen = EXCLUDED.last_seen,
  recent_texts = EXCLUDED.recent_texts,
  recent_replies = EXCLUDED.recent_replies,
  identity_embedding = CASE
    WHEN viewer_profiles.name IS DISTINCT FROM EXCLUDED.name
    THEN NULL ELSE viewer_profiles.identity_embedding END,
  identity_embedding_hash = CASE
    WHEN viewer_profiles.name IS DISTINCT FROM EXCLUDED.name
    THEN NULL ELSE viewer_profiles.identity_embedding_hash END,
  identity_embedding_source_hash = CASE
    WHEN viewer_profiles.name IS DISTINCT FROM EXCLUDED.name
    THEN NULL ELSE viewer_profiles.identity_embedding_source_hash END,
  identity_embedding_fingerprint = CASE
    WHEN viewer_profiles.name IS DISTINCT FROM EXCLUDED.name
    THEN NULL ELSE viewer_profiles.identity_embedding_fingerprint END,
  identity_embedding_updated_at = CASE
    WHEN viewer_profiles.name IS DISTINCT FROM EXCLUDED.name
    THEN NULL ELSE viewer_profiles.identity_embedding_updated_at END,
  facts_embedding = CASE
    WHEN viewer_profiles.recent_texts IS DISTINCT FROM EXCLUDED.recent_texts
    THEN NULL ELSE viewer_profiles.facts_embedding END,
  facts_embedding_hash = CASE
    WHEN viewer_profiles.recent_texts IS DISTINCT FROM EXCLUDED.recent_texts
    THEN NULL ELSE viewer_profiles.facts_embedding_hash END,
  facts_embedding_source_hash = CASE
    WHEN viewer_profiles.recent_texts IS DISTINCT FROM EXCLUDED.recent_texts
    THEN NULL ELSE viewer_profiles.facts_embedding_source_hash END,
  facts_embedding_fingerprint = CASE
    WHEN viewer_profiles.recent_texts IS DISTINCT FROM EXCLUDED.recent_texts
    THEN NULL ELSE viewer_profiles.facts_embedding_fingerprint END,
  facts_embedding_updated_at = CASE
    WHEN viewer_profiles.recent_texts IS DISTINCT FROM EXCLUDED.recent_texts
    THEN NULL ELSE viewer_profiles.facts_embedding_updated_at END,
  interaction_embedding = CASE
    WHEN viewer_profiles.recent_replies IS DISTINCT FROM EXCLUDED.recent_replies
    THEN NULL ELSE viewer_profiles.interaction_embedding END,
  interaction_embedding_hash = CASE
    WHEN viewer_profiles.recent_replies IS DISTINCT FROM EXCLUDED.recent_replies
    THEN NULL ELSE viewer_profiles.interaction_embedding_hash END,
  interaction_embedding_source_hash = CASE
    WHEN viewer_profiles.recent_replies IS DISTINCT FROM EXCLUDED.recent_replies
    THEN NULL ELSE viewer_profiles.interaction_embedding_source_hash END,
  interaction_embedding_fingerprint = CASE
    WHEN viewer_profiles.recent_replies IS DISTINCT FROM EXCLUDED.recent_replies
    THEN NULL ELSE viewer_profiles.interaction_embedding_fingerprint END,
  interaction_embedding_updated_at = CASE
    WHEN viewer_profiles.recent_replies IS DISTINCT FROM EXCLUDED.recent_replies
    THEN NULL ELSE viewer_profiles.interaction_embedding_updated_at END,
  updated_at = clock_timestamp()`
	profiles := 0
	var revoked []string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("CREATE TEMP TABLE IF NOT EXISTS viewer_summary_revoked(uid TEXT PRIMARY KEY) ON COMMIT DROP").Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM viewer_summary_revoked").Error; err != nil {
			return err
		}
		// 先删不符合公开门槛的旧档案：历史私密来源混入后不再达标的人必须消失，
		// 其向量随行删除，不能只是「以后不读」；同时登记知识条目清理任务。
		if res := tx.Exec(cleanupSQL, opts.MinHits); res.Error != nil {
			return res.Error
		}
		res := tx.Exec(upsertSQL, opts.RecentTexts, opts.MinHits, opts.RecentReplies)
		if res.Error != nil {
			return res.Error
		}
		profiles = int(res.RowsAffected)
		// 存活档案也要校验侧写来源：来源事件被撤回/改密后，派生内容必须立即不可见。
		if res := tx.Exec(revokeSummarySQL); res.Error != nil {
			return res.Error
		}
		var err error
		revoked, err = finalizeRevokedSummaries(tx)
		return err
	})
	if err != nil {
		return store.ViewerRebuildStats{}, &safeError{"重建观众档案失败", err}
	}
	return store.ViewerRebuildStats{Profiles: profiles, Revoked: revoked}, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilFields(v []store.ViewerSummaryField) []store.ViewerSummaryField {
	if v == nil {
		return []store.ViewerSummaryField{}
	}
	return v
}
