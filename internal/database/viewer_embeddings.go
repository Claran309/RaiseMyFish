package database

// 本文件实现观众档案向量的增量读写（M10：三路索引）。
//
// 三路各自一组索引列：
//   identity_embedding    昵称
//   facts_embedding       本人发言
//   interaction_embedding 鱼的回复
//
// 每路独立记录：向量、文本 hash（增量跳过）、源 hash（stale 判定与写回 CAS）、
// 索引指纹（向量空间身份）、更新时间。
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
	vector      string
	hash        string
	sourceHash  string
	fingerprint string
	updated     string
}

var profileEmbeddingColumnSet = map[viewer.EmbeddingKind]profileEmbeddingColumns{
	viewer.KindIdentity: {
		vector: "identity_embedding", hash: "identity_embedding_hash",
		sourceHash: "identity_embedding_source_hash", fingerprint: "identity_embedding_fingerprint",
		updated: "identity_embedding_updated_at",
	},
	viewer.KindFacts: {
		vector: "facts_embedding", hash: "facts_embedding_hash",
		sourceHash: "facts_embedding_source_hash", fingerprint: "facts_embedding_fingerprint",
		updated: "facts_embedding_updated_at",
	},
	viewer.KindInteraction: {
		vector: "interaction_embedding", hash: "interaction_embedding_hash",
		sourceHash: "interaction_embedding_source_hash", fingerprint: "interaction_embedding_fingerprint",
		updated: "interaction_embedding_updated_at",
	},
}

// ViewerEmbeddingInputs 返回全部档案的向量化源字段（昵称 + 最近发言 + 最近回复）。
//
// 源 hash 在 SQL 层按「这一路自己的源字段」计算：identity 只看昵称、
// facts 只看本人发言、interaction 只看鱼的回复；任何一路写入路径
// （UpsertProfile / RebuildProfiles / 手写 SQL）都会自动让该路 stale，
// 不需要每个写入点各自维护版本号。
func (r *ViewerRepo) ViewerEmbeddingInputs(ctx context.Context) ([]viewer.EmbeddingInput, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		UID            string
		Name           string
		RecentTexts    []byte
		RecentReplies  []byte
		IdentitySource string
		FactsSource    string
		InteractSource string
	}
	identityExpr, _ := viewerSourceHashExpr(viewer.KindIdentity)
	factsExpr, _ := viewerSourceHashExpr(viewer.KindFacts)
	interactExpr, _ := viewerSourceHashExpr(viewer.KindInteraction)
	err := r.db.WithContext(ctx).Table("viewer_profiles").
		Select("uid, name, recent_texts, recent_replies, " +
			identityExpr + " AS identity_source, " +
			factsExpr + " AS facts_source, " +
			interactExpr + " AS interact_source").
		Order("uid ASC").
		Scan(&rows).Error
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
		inputs = append(inputs, viewer.EmbeddingInput{
			UID: row.UID, Name: row.Name, Texts: texts, Replies: replies,
			SourceHashes: map[viewer.EmbeddingKind]string{
				viewer.KindIdentity:    row.IdentitySource,
				viewer.KindFacts:       row.FactsSource,
				viewer.KindInteraction: row.InteractSource,
			},
		})
	}
	return inputs, nil
}

// ExistingProfileEmbeddingIndexes 返回 uid → kind → 索引状态，用于分路增量跳过。
func (r *ViewerRepo) ExistingProfileEmbeddingIndexes(ctx context.Context) (map[string]map[viewer.EmbeddingKind]viewer.EmbeddingIndex, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		UID         string
		Kind        string
		TextHash    string
		SourceHash  string
		Fingerprint string
	}
	err := r.db.WithContext(ctx).Raw(`
SELECT uid, kind,
       COALESCE(text_hash,'') AS text_hash,
       COALESCE(source_hash,'') AS source_hash,
       COALESCE(fingerprint,'') AS fingerprint
FROM (
    SELECT uid, 'identity' AS kind,
           identity_embedding_hash AS text_hash,
           identity_embedding_source_hash AS source_hash,
           identity_embedding_fingerprint AS fingerprint
    FROM viewer_profiles
    WHERE identity_embedding IS NOT NULL
    UNION ALL
    SELECT uid, 'facts' AS kind,
           facts_embedding_hash,
           facts_embedding_source_hash,
           facts_embedding_fingerprint
    FROM viewer_profiles
    WHERE facts_embedding IS NOT NULL
    UNION ALL
    SELECT uid, 'interaction' AS kind,
           interaction_embedding_hash,
           interaction_embedding_source_hash,
           interaction_embedding_fingerprint
    FROM viewer_profiles
    WHERE interaction_embedding IS NOT NULL
) t`).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"查询档案向量状态失败", err}
	}
	indexes := make(map[string]map[viewer.EmbeddingKind]viewer.EmbeddingIndex, len(rows))
	for _, row := range rows {
		kind := viewer.EmbeddingKind(row.Kind)
		if !kind.Valid() {
			continue
		}
		if indexes[row.UID] == nil {
			indexes[row.UID] = make(map[viewer.EmbeddingKind]viewer.EmbeddingIndex, 3)
		}
		indexes[row.UID][kind] = viewer.EmbeddingIndex{
			TextHash:    row.TextHash,
			SourceHash:  row.SourceHash,
			Fingerprint: row.Fingerprint,
		}
	}
	return indexes, nil
}

// UpsertProfileEmbeddings 按 kind 写回对应列；未知 kind 直接拒绝。
//
// 写回用 CAS 校验该路源未变化：源 hash 由读取方随输入下发，写回时与当前行
// 重新计算的源 hash 比较；不一致说明读取后档案已被重建/清洗，本次结果
// 属于过期计算，必须拒绝写回，否则会把刚清掉的污染向量重新种回去。
// 返回实际写入条数，被拒绝的由调用方计入 Rejected。
func (r *ViewerRepo) UpsertProfileEmbeddings(ctx context.Context, records []viewer.EmbeddingRecord) (int, error) {
	if ctx == nil {
		return 0, store.ErrInvalid
	}
	if len(records) == 0 {
		return 0, nil
	}
	written := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			if strings.TrimSpace(record.UID) == "" || strings.TrimSpace(record.Hash) == "" ||
				strings.TrimSpace(record.SourceHash) == "" || strings.TrimSpace(record.Fingerprint) == "" {
				return store.ErrInvalid
			}
			columns, ok := profileEmbeddingColumnSet[record.Kind]
			if !ok {
				return store.ErrInvalid
			}
			sourceExpr, ok := viewerSourceHashExpr(record.Kind)
			if !ok {
				return store.ErrInvalid
			}
			literal, err := vectorLiteral(record.Embedding, viewer.EmbeddingDimension)
			if err != nil {
				return err
			}
			query := fmt.Sprintf(`
UPDATE viewer_profiles
SET %s=?::vector, %s=?, %s=?, %s=?, %s=clock_timestamp()
WHERE uid=? AND %s = ?`,
				columns.vector, columns.hash, columns.sourceHash, columns.fingerprint, columns.updated, sourceExpr)
			result := tx.Exec(query, literal, record.Hash, record.SourceHash, record.Fingerprint, record.UID, record.SourceHash)
			if result.Error != nil {
				return &safeError{"写入档案向量失败", result.Error}
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
