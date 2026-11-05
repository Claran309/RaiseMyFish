package database

// 知识向量的持久化与 pgvector 检索。
// SQL 全部参数化；向量按 pgvector 文本格式传参并用 ::vector 转换，不拼接用户输入。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// KnowledgeEmbeddingRepo 实现 knowledge.EmbeddingStore。
type KnowledgeEmbeddingRepo struct{ db *gorm.DB }

func NewKnowledgeEmbeddingRepo(db *gorm.DB) (*KnowledgeEmbeddingRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &KnowledgeEmbeddingRepo{db: db}, nil
}

// ExistingEmbeddings 返回所有已入库条目的增量判断信息。
func (r *KnowledgeEmbeddingRepo) ExistingEmbeddings(ctx context.Context) (map[string]knowledge.EmbeddingMeta, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		ID        string
		Source    string
		Title     string
		UpdatedAt time.Time
	}
	if err := r.db.WithContext(ctx).Raw("SELECT id, source, title, updated_at FROM knowledge_embeddings").Scan(&rows).Error; err != nil {
		return nil, &safeError{"查询知识向量状态失败", err}
	}
	result := make(map[string]knowledge.EmbeddingMeta, len(rows))
	for _, row := range rows {
		result[row.ID] = knowledge.EmbeddingMeta{Source: row.Source, Title: row.Title, UpdatedAt: row.UpdatedAt}
	}
	return result, nil
}

const upsertKnowledgeEmbeddingSQL = `
INSERT INTO knowledge_embeddings (id, source, title, updated_at, embedding)
VALUES (?, ?, ?, ?, ?::vector)
ON CONFLICT (id) DO UPDATE
SET source = EXCLUDED.source,
    title = EXCLUDED.title,
    updated_at = EXCLUDED.updated_at,
    embedding = EXCLUDED.embedding`

// UpsertEmbeddings 在一个事务内幂等写入一批向量；重复 id 覆盖旧值。
func (r *KnowledgeEmbeddingRepo) UpsertEmbeddings(ctx context.Context, records []knowledge.EmbeddingRecord) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	if len(records) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.Source) == "" || strings.TrimSpace(record.Title) == "" {
				return errors.New("知识向量缺少必需字段")
			}
			vector, err := vectorLiteral(record.Embedding, knowledge.EmbeddingDimension)
			if err != nil {
				return err
			}
			updatedAt := record.UpdatedAt
			if updatedAt.IsZero() {
				updatedAt = time.Now().UTC()
			}
			if err := tx.Exec(upsertKnowledgeEmbeddingSQL, record.ID, record.Source, record.Title, updatedAt.UTC(), vector).Error; err != nil {
				return &safeError{"写入知识向量失败", err}
			}
		}
		return nil
	})
}

// SemanticSearch 按余弦距离取最相近的 limit 条，返回 1-distance。
func (r *KnowledgeEmbeddingRepo) SemanticSearch(ctx context.Context, vector []float64, limit int) ([]knowledge.SemanticHit, error) {
	if ctx == nil || limit < 1 || limit > 200 {
		return nil, store.ErrInvalid
	}
	literal, err := vectorLiteral(vector, knowledge.EmbeddingDimension)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID  string
		Sim float64
	}
	if err := r.db.WithContext(ctx).Raw(`
WITH q AS (SELECT ?::vector AS v)
SELECT id, 1 - (embedding <=> q.v) AS sim
FROM knowledge_embeddings, q
WHERE embedding IS NOT NULL
ORDER BY embedding <=> q.v
LIMIT ?`, literal, limit).Scan(&rows).Error; err != nil {
		return nil, &safeError{"知识向量检索失败", err}
	}
	hits := make([]knowledge.SemanticHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, knowledge.SemanticHit{ID: row.ID, Similarity: row.Sim})
	}
	return hits, nil
}

// vectorLiteral 生成 pgvector 的 [x,y,...] 文本；拒绝非法维度与非有限值。
//
// 维度由调用方传入：知识、长期记忆、观众档案共用同一段 PG 文本协议。
func vectorLiteral(vector []float64, dimension int) (string, error) {
	if len(vector) != dimension {
		return "", fmt.Errorf("向量维度必须为%d", dimension)
	}
	parts := make([]string, len(vector))
	for i, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", errors.New("向量包含非法数值")
		}
		parts[i] = strconv.FormatFloat(value, 'g', -1, 64)
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}
