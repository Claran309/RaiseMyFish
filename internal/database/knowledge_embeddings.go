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
		ID          string
		TextHash    string
		SourceHash  string
		Fingerprint string
	}
	if err := r.db.WithContext(ctx).Raw(`
SELECT id,
       COALESCE(text_hash,'') AS text_hash,
       COALESCE(source_hash,'') AS source_hash,
       COALESCE(fingerprint,'') AS fingerprint
FROM knowledge_embeddings`).Scan(&rows).Error; err != nil {
		return nil, &safeError{"查询知识向量状态失败", err}
	}
	result := make(map[string]knowledge.EmbeddingMeta, len(rows))
	for _, row := range rows {
		result[row.ID] = knowledge.EmbeddingMeta{TextHash: row.TextHash, SourceHash: row.SourceHash, Fingerprint: row.Fingerprint}
	}
	return result, nil
}

// 写回 CAS 与 memory/viewer 同口径：ExpectedSourceHash 是读取方看到的行状态，
// 写回时与当前行比较；不一致说明读取后其他进程已按更新源写过，本次结果
// 属于过期计算，必须拒绝。行不存在或旧行源 hash 为空时以 NULL 作为期望值。
const upsertKnowledgeEmbeddingSQL = `
INSERT INTO knowledge_embeddings (id, source, title, updated_at, embedding, text_hash, source_hash, fingerprint)
VALUES (?, ?, ?, ?, ?::vector, ?, ?, ?)
ON CONFLICT (id) DO UPDATE
SET source = EXCLUDED.source,
    title = EXCLUDED.title,
    updated_at = EXCLUDED.updated_at,
    embedding = EXCLUDED.embedding,
    text_hash = EXCLUDED.text_hash,
    source_hash = EXCLUDED.source_hash,
    fingerprint = EXCLUDED.fingerprint
WHERE knowledge_embeddings.source_hash IS NOT DISTINCT FROM ?`

// UpsertEmbeddings 在一个事务内幂等写入一批向量，返回实际写入条数。
// 源在计算期间变化或已被其他进程更新的行被 CAS 拒绝，不覆盖更新结果。
func (r *KnowledgeEmbeddingRepo) UpsertEmbeddings(ctx context.Context, records []knowledge.EmbeddingRecord) (int, error) {
	if ctx == nil {
		return 0, store.ErrInvalid
	}
	if len(records) == 0 {
		return 0, nil
	}
	written := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.Source) == "" || strings.TrimSpace(record.Title) == "" ||
				strings.TrimSpace(record.Hash) == "" || strings.TrimSpace(record.SourceHash) == "" || strings.TrimSpace(record.Fingerprint) == "" {
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
			var expected any
			if record.ExpectedSourceHash != "" {
				expected = record.ExpectedSourceHash
			}
			result := tx.Exec(upsertKnowledgeEmbeddingSQL, record.ID, record.Source, record.Title, updatedAt.UTC(),
				vector, record.Hash, record.SourceHash, record.Fingerprint, expected)
			if result.Error != nil {
				return &safeError{"写入知识向量失败", result.Error}
			}
			if result.RowsAffected == 1 {
				written++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return written, nil
}

// SemanticSearch 按余弦距离取同一索引指纹下最相近的 limit 条，返回 1-distance。
// 指纹不同的旧向量不参与查询，换模型后不会复用旧空间。
func (r *KnowledgeEmbeddingRepo) SemanticSearch(ctx context.Context, vector []float64, fingerprint string, limit int) ([]knowledge.SemanticHit, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if ctx == nil || fingerprint == "" || limit < 1 || limit > 200 {
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
WHERE embedding IS NOT NULL AND fingerprint = ?
ORDER BY embedding <=> q.v
LIMIT ?`, literal, fingerprint, limit).Scan(&rows).Error; err != nil {
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
