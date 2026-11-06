package database

// 本文件实现观众档案向量的增量读写（M10：三路索引）。
//
// 三路各自一列向量 + 一列内容 hash + 一列更新时间：
//   identity_embedding    昵称
//   facts_embedding       本人发言
//   interaction_embedding 鱼的回复
//
// 只更新既有档案的向量列，不新建档案；某一路是否变化由对应 hash 比较决定。

import (
	"context"
	"fmt"
	"strings"

	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"gorm.io/gorm"
)

// profileEmbeddingColumns 是「向量路 → 列名」的白名单。
//
// 列名不能参数化，只能拼进 SQL，所以必须走白名单（绝不接受外部输入）。
type profileEmbeddingColumns struct {
	vector  string
	hash    string
	updated string
}

var profileEmbeddingColumnSet = map[viewer.EmbeddingKind]profileEmbeddingColumns{
	viewer.KindIdentity:    {vector: "identity_embedding", hash: "identity_embedding_hash", updated: "identity_embedding_updated_at"},
	viewer.KindFacts:       {vector: "facts_embedding", hash: "facts_embedding_hash", updated: "facts_embedding_updated_at"},
	viewer.KindInteraction: {vector: "interaction_embedding", hash: "interaction_embedding_hash", updated: "interaction_embedding_updated_at"},
}

// ViewerEmbeddingInputs 返回全部档案的向量化源字段（昵称 + 最近发言 + 最近回复）。
//
// 拆分发生在领域层（viewer.documentsFor），这里只负责把源字段取全。
func (r *ViewerRepo) ViewerEmbeddingInputs(ctx context.Context) ([]viewer.EmbeddingInput, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []viewerRow
	err := r.db.WithContext(ctx).Table("viewer_profiles").
		Select("uid, name, recent_texts, recent_replies").
		Order("uid ASC").
		Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取档案向量化源失败", err}
	}
	inputs := make([]viewer.EmbeddingInput, 0, len(rows))
	for _, row := range rows {
		var texts, replies []string
		if err := decodeStrings(row.RecentTexts, &texts); err != nil {
			return nil, &safeError{"解析档案发言失败", err}
		}
		if err := decodeStrings(row.RecentReplies, &replies); err != nil {
			return nil, &safeError{"解析档案回复失败", err}
		}
		inputs = append(inputs, viewer.EmbeddingInput{UID: row.UID, Name: row.Name, Texts: texts, Replies: replies})
	}
	return inputs, nil
}

// ExistingProfileEmbeddingHashes 返回 uid → kind → 内容 hash，用于分路增量跳过。
func (r *ViewerRepo) ExistingProfileEmbeddingHashes(ctx context.Context) (map[string]map[viewer.EmbeddingKind]string, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		UID  string
		Kind string
		Hash string
	}
	err := r.db.WithContext(ctx).Raw(`
SELECT uid, kind, hash FROM (
    SELECT uid, 'identity' AS kind, identity_embedding_hash AS hash
    FROM viewer_profiles
    WHERE identity_embedding IS NOT NULL AND COALESCE(identity_embedding_hash, '') <> ''
    UNION ALL
    SELECT uid, 'facts' AS kind, facts_embedding_hash AS hash
    FROM viewer_profiles
    WHERE facts_embedding IS NOT NULL AND COALESCE(facts_embedding_hash, '') <> ''
    UNION ALL
    SELECT uid, 'interaction' AS kind, interaction_embedding_hash AS hash
    FROM viewer_profiles
    WHERE interaction_embedding IS NOT NULL AND COALESCE(interaction_embedding_hash, '') <> ''
) t`).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"查询档案向量状态失败", err}
	}
	hashes := make(map[string]map[viewer.EmbeddingKind]string, len(rows))
	for _, row := range rows {
		kind := viewer.EmbeddingKind(row.Kind)
		if !kind.Valid() {
			continue
		}
		if hashes[row.UID] == nil {
			hashes[row.UID] = make(map[viewer.EmbeddingKind]string, 3)
		}
		hashes[row.UID][kind] = row.Hash
	}
	return hashes, nil
}

// UpsertProfileEmbeddings 按 kind 写回对应列；未知 kind 直接拒绝。
func (r *ViewerRepo) UpsertProfileEmbeddings(ctx context.Context, records []viewer.EmbeddingRecord) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	if len(records) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			if strings.TrimSpace(record.UID) == "" || strings.TrimSpace(record.Hash) == "" {
				return store.ErrInvalid
			}
			columns, ok := profileEmbeddingColumnSet[record.Kind]
			if !ok {
				return store.ErrInvalid
			}
			literal, err := vectorLiteral(record.Embedding, viewer.EmbeddingDimension)
			if err != nil {
				return err
			}
			query := fmt.Sprintf(`
UPDATE viewer_profiles
SET %s=?::vector, %s=?, %s=clock_timestamp()
WHERE uid=?`, columns.vector, columns.hash, columns.updated)
			if err := tx.Exec(query, literal, record.Hash, record.UID).Error; err != nil {
				return &safeError{"写入档案向量失败", err}
			}
		}
		return nil
	})
}
