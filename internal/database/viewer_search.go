package database

// 本文件实现观众档案的关键词/向量检索。
//
// M10 起按索引用途查询：identity（昵称）/ facts（本人发言）/ interaction（鱼的回复）。
// 认人只查 identity/facts；interaction 只在互动查询里使用，避免鱼的口头禅污染认人。
//
// 档案本身是公开投影（uid/昵称/累计次数/最近发言），不含渠道私密字段，
// 所以查询不需要 Scope；SQL 仍然全部参数化。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
)

// profileKeywordColumns 是「索引路 → 关键词列」的白名单。
//
// 列名不能参数化，只能拼进 SQL，所以必须走白名单（绝不接受外部输入）。
var profileKeywordColumns = map[viewer.EmbeddingKind]string{
	viewer.KindIdentity:    "name",
	viewer.KindFacts:       "recent_texts::text",
	viewer.KindInteraction: "recent_replies::text",
}

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

// KeywordProfiles 按指定索引做关键词模糊匹配。
//
// kind 必须是 identity/facts/interaction 之一；列名由白名单决定，keyword 走参数。
func (r *ViewerRepo) KeywordProfiles(ctx context.Context, kind viewer.EmbeddingKind, keyword string, limit int) ([]store.ViewerProfileHit, error) {
	keyword = strings.TrimSpace(keyword)
	column, ok := profileKeywordColumns[kind]
	if ctx == nil || !ok || keyword == "" || limit < 1 || limit > 50 {
		return nil, store.ErrInvalid
	}
	pattern := "%" + keyword + "%"
	var rows []viewerHitRow
	query := fmt.Sprintf(`
SELECT uid, name, hits, replies, last_seen, recent_texts, recent_replies, updated_at, 0 AS sim
FROM viewer_profiles
WHERE %s ILIKE ?
ORDER BY hits DESC, last_seen DESC, uid ASC
LIMIT ?`, column)
	err := r.db.WithContext(ctx).Raw(query, pattern, limit).Scan(&rows).Error
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

// SemanticProfiles 按指定索引取最相近的 limit 个档案。
//
//   - identity：昵称向量，用于同音/改名/模糊昵称；
//   - facts：本人发言向量，用于「谁说过这句话 / 谁爱要白饭」；
//   - interaction：鱼的回复向量，只用于互动查询，不参与认人。
//
// 列名来自白名单（profileEmbeddingColumnSet），不接受外部输入。
//
// stale 拒绝（C2/N08）：向量非空还不够——
//   - 索引指纹必须等于查询侧指纹（换模型/提供方/维度/模板后旧向量不参与排序）；
//   - 写回时的源 hash 必须等于该路当前源字段 hash（档案重建/清洗后旧向量立即失效）；
//   - 索引文本 hash 非空（未标记索引版本的向量不可信）。
//
// 三者任一不满足都不进入排序，宁可暂时少召回，也不拿旧语义给新档案排序。
func (r *ViewerRepo) SemanticProfiles(ctx context.Context, kind viewer.EmbeddingKind, vector []float64, fingerprint string, limit int) ([]store.ViewerProfileHit, error) {
	columns, ok := profileEmbeddingColumnSet[kind]
	fingerprint = strings.TrimSpace(fingerprint)
	if ctx == nil || !ok || fingerprint == "" || limit < 1 || limit > 200 {
		return nil, store.ErrInvalid
	}
	sourceExpr, ok := viewerSourceHashExpr(kind)
	if !ok {
		return nil, store.ErrInvalid
	}
	literal, err := vectorLiteral(vector, viewer.EmbeddingDimension)
	if err != nil {
		return nil, store.ErrInvalid
	}
	var rows []viewerHitRow
	query := fmt.Sprintf(`
WITH q AS (SELECT ?::vector AS v)
SELECT uid, name, hits, replies, last_seen, recent_texts, recent_replies, updated_at,
       1 - (%s <=> q.v) AS sim
FROM viewer_profiles, q
WHERE %s IS NOT NULL
  AND COALESCE(%s,'') <> ''
  AND %s = ?
  AND %s = %s
ORDER BY %s <=> q.v, uid ASC
LIMIT ?`,
		columns.vector, columns.vector, columns.hash,
		columns.fingerprint, columns.sourceHash, sourceExpr,
		columns.vector)
	err = r.db.WithContext(ctx).Raw(query, literal, fingerprint, limit).Scan(&rows).Error
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
