package database

// 本文件实现观众档案向量的增量读写。
//
// 只更新既有档案的向量列，不新建档案；档案内容是否变化由调用方的
// profile_embedding_hash 比较决定。

import (
	"context"
	"strings"

	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"gorm.io/gorm"
)

// ViewerEmbeddingInputs 返回全部档案的向量化源字段（含最近发言与回复）。
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

// ExistingProfileEmbeddingHashes 返回已入库向量的内容 hash，用于增量跳过。
func (r *ViewerRepo) ExistingProfileEmbeddingHashes(ctx context.Context) (map[string]string, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		UID  string
		Hash string
	}
	err := r.db.WithContext(ctx).Table("viewer_profiles").
		Select("uid, profile_embedding_hash AS hash").
		Where("profile_embedding IS NOT NULL AND COALESCE(profile_embedding_hash,'') <> ''").
		Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"查询档案向量状态失败", err}
	}
	hashes := make(map[string]string, len(rows))
	for _, row := range rows {
		hashes[row.UID] = row.Hash
	}
	return hashes, nil
}

// UpsertProfileEmbeddings 只更新既有档案的向量列。
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
			literal, err := vectorLiteral(record.Embedding, viewer.EmbeddingDimension)
			if err != nil {
				return err
			}
			if err := tx.Exec(`
UPDATE viewer_profiles
SET profile_embedding=?::vector, profile_embedding_hash=?, profile_embedding_updated_at=clock_timestamp()
WHERE uid=?`, literal, record.Hash, record.UID).Error; err != nil {
				return &safeError{"写入档案向量失败", err}
			}
		}
		return nil
	})
}
