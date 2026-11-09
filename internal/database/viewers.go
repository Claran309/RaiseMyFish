package database

// 本文件实现观众档案：读接口 + 从 events 聚合重建。
//
// 聚合口径（派发单）：
//   - in 事件按 from_id 分组：hits 计数、recent_texts（最近 N 条）
//   - out 事件按 reply_to 关联回 in 事件，再按 from_id 分组：replies 计数、recent_replies（最近 M 条）
//   - 只给 hits ≥ minHits 的人建档（一次路过不建）

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

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
	UID           string
	Name          string
	Hits          int
	Replies       int
	LastSeen      time.Time
	RecentTexts   []byte
	RecentReplies []byte
	UpdatedAt     time.Time
}

func (r viewerRow) profile() (store.ViewerProfile, error) {
	p := store.ViewerProfile{
		UID: r.UID, Name: r.Name, Hits: r.Hits, Replies: r.Replies,
		LastSeen: r.LastSeen, UpdatedAt: r.UpdatedAt,
	}
	if err := decodeStrings(r.RecentTexts, &p.RecentTexts); err != nil {
		return store.ViewerProfile{}, &safeError{"解析观众发言失败", err}
	}
	if err := decodeStrings(r.RecentReplies, &p.RecentReplies); err != nil {
		return store.ViewerProfile{}, &safeError{"解析回复快照失败", err}
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
)
DELETE FROM viewer_profiles WHERE uid NOT IN (SELECT uid FROM inbound)`
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
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先删不符合公开门槛的旧档案：历史私密来源混入后不再达标的人必须消失，
		// 其向量随行删除，不能只是「以后不读」。
		if res := tx.Exec(cleanupSQL, opts.MinHits); res.Error != nil {
			return res.Error
		}
		res := tx.Exec(upsertSQL, opts.RecentTexts, opts.MinHits, opts.RecentReplies)
		if res.Error != nil {
			return res.Error
		}
		profiles = int(res.RowsAffected)
		return nil
	})
	if err != nil {
		return store.ViewerRebuildStats{}, &safeError{"重建观众档案失败", err}
	}
	return store.ViewerRebuildStats{Profiles: profiles}, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
