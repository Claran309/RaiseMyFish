package database

// 本文件实现观众档案的关键词/向量检索。
//
// 档案本身是公开投影（uid/昵称/累计次数/最近发言），不含渠道私密字段，
// 所以查询不需要 Scope；SQL 仍然全部参数化。

import (
	"context"
	"strings"
	"time"

	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
)

// viewerHitRow 是检索结果的 SQL 投影；recent_* 以 JSONB 文本扫描。
type viewerHitRow struct {
	UID           string
	Name          string
	Hits          int
	Replies       int
	LastSeen      time.Time
	RecentTexts   []byte
	RecentReplies []byte
	UpdatedAt     time.Time
	Sim           float64
}

func (row viewerHitRow) hit() (store.ViewerProfileHit, error) {
	profile, err := viewerRow{
		UID: row.UID, Name: row.Name, Hits: row.Hits, Replies: row.Replies,
		LastSeen: row.LastSeen, RecentTexts: row.RecentTexts,
		RecentReplies: row.RecentReplies, UpdatedAt: row.UpdatedAt,
	}.profile()
	if err != nil {
		return store.ViewerProfileHit{}, err
	}
	return store.ViewerProfileHit{Profile: profile, Similarity: row.Sim, Score: row.Sim}, nil
}

// KeywordProfiles 按昵称/最近发言/最近回复做关键词模糊匹配。
func (r *ViewerRepo) KeywordProfiles(ctx context.Context, keyword string, limit int) ([]store.ViewerProfileHit, error) {
	keyword = strings.TrimSpace(keyword)
	if ctx == nil || keyword == "" || limit < 1 || limit > 50 {
		return nil, store.ErrInvalid
	}
	pattern := "%" + keyword + "%"
	var rows []viewerHitRow
	err := r.db.WithContext(ctx).Table("viewer_profiles").
		Where("name ILIKE ? OR recent_texts::text ILIKE ? OR recent_replies::text ILIKE ?", pattern, pattern, pattern).
		Select("uid, name, hits, replies, last_seen, recent_texts, recent_replies, updated_at, 0 AS sim").
		Order("hits DESC, last_seen DESC, uid ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, &safeError{"关键词检索观众档案失败", err}
	}
	hits := make([]store.ViewerProfileHit, 0, len(rows))
	for _, row := range rows {
		hit, err := row.hit()
		if err != nil {
			return nil, err
		}
		hit.Score = 1
		hits = append(hits, hit)
	}
	return hits, nil
}

// SemanticProfiles 按档案向量余弦距离取最相近的 limit 个。
func (r *ViewerRepo) SemanticProfiles(ctx context.Context, vector []float64, limit int) ([]store.ViewerProfileHit, error) {
	if ctx == nil || limit < 1 || limit > 200 {
		return nil, store.ErrInvalid
	}
	literal, err := vectorLiteral(vector, viewer.EmbeddingDimension)
	if err != nil {
		return nil, store.ErrInvalid
	}
	var rows []viewerHitRow
	err = r.db.WithContext(ctx).Raw(`
WITH q AS (SELECT ?::vector AS v)
SELECT uid, name, hits, replies, last_seen, recent_texts, recent_replies, updated_at,
       1 - (profile_embedding <=> q.v) AS sim
FROM viewer_profiles, q
WHERE profile_embedding IS NOT NULL
ORDER BY profile_embedding <=> q.v, uid ASC
LIMIT ?`, literal, limit).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"向量检索观众档案失败", err}
	}
	hits := make([]store.ViewerProfileHit, 0, len(rows))
	for _, row := range rows {
		hit, err := row.hit()
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}
