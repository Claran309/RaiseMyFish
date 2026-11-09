package database

// 面板向量入库状态的只读统计。
//
// 统计口径与入库任务保持一致：记忆只算有摘要的行（空摘要本来就不会送 embedding），
// 档案算全部档案行；知识库的条目总数由面板层用文件快照覆盖，这里只管已嵌入与最后同步时间。

import (
	"context"
	"time"

	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
)

// EmbeddingStatus 汇总知识/记忆/档案三路向量入库状态。
func (r *WebRepo) EmbeddingStatus(ctx context.Context) (store.WebEmbeddingStatus, error) {
	if ctx == nil {
		return store.WebEmbeddingStatus{}, store.ErrInvalid
	}
	var st store.WebEmbeddingStatus

	var knowledge struct {
		Embedded    int64
		LastUpdated *time.Time
	}
	if err := r.db.WithContext(ctx).Raw(`
SELECT count(*) FILTER (WHERE embedding IS NOT NULL) AS embedded,
       max(updated_at) FILTER (WHERE embedding IS NOT NULL) AS last_updated
FROM knowledge_embeddings`).Scan(&knowledge).Error; err != nil {
		return store.WebEmbeddingStatus{}, &safeError{"统计知识向量失败", err}
	}
	st.Knowledge.Embedded = knowledge.Embedded
	st.Knowledge.Total = knowledge.Embedded
	if knowledge.LastUpdated != nil {
		st.Knowledge.LastUpdated = *knowledge.LastUpdated
	}

	var memory struct {
		Total       int64
		Embedded    int64
		LastUpdated *time.Time
	}
	// 只有「向量可用」的行才算已入库：源 hash 与当前源一致、文本 hash 与指纹齐全。
	// 否则摘要更新后残留的旧向量会被统计成已完成，面板看不出还需要重算。
	if err := r.db.WithContext(ctx).Raw(`
SELECT count(*) AS total,
       count(*) FILTER (WHERE embedding IS NOT NULL
                          AND COALESCE(embedding_text_hash,'') <> ''
                          AND COALESCE(embedding_fingerprint,'') <> ''
                          AND embedding_source_hash = ` + memorySourceHashExpr + `) AS embedded,
       max(embedding_updated_at) FILTER (WHERE embedding IS NOT NULL
                          AND COALESCE(embedding_text_hash,'') <> ''
                          AND COALESCE(embedding_fingerprint,'') <> ''
                          AND embedding_source_hash = ` + memorySourceHashExpr + `) AS last_updated
FROM memory_records
WHERE COALESCE(summary, '') <> ''`).Scan(&memory).Error; err != nil {
		return store.WebEmbeddingStatus{}, &safeError{"统计记忆向量失败", err}
	}
	st.Memory = store.WebEmbeddingSection{Total: memory.Total, Embedded: memory.Embedded, Pending: memory.Total - memory.Embedded}
	if memory.LastUpdated != nil {
		st.Memory.LastUpdated = *memory.LastUpdated
	}

	var profileStats struct {
		Total       int64
		Embedded    int64
		LastUpdated *time.Time
	}
	// 档案向量有三路（identity/facts/interaction）；面板按「本人发言」口径统计，
	// 因为那才是认人检索实际用的索引，另两路是补充。
	// 同样要求源 hash 与当前发言一致，否则重建前的旧向量不算已入库。
	factsSourceExpr, _ := viewerSourceHashExpr(viewer.KindFacts)
	if err := r.db.WithContext(ctx).Raw(`
SELECT count(*) AS total,
       count(*) FILTER (WHERE facts_embedding IS NOT NULL
                          AND COALESCE(facts_embedding_hash,'') <> ''
                          AND COALESCE(facts_embedding_fingerprint,'') <> ''
                          AND facts_embedding_source_hash = ` + factsSourceExpr + `) AS embedded,
       max(facts_embedding_updated_at) FILTER (WHERE facts_embedding IS NOT NULL
                          AND COALESCE(facts_embedding_hash,'') <> ''
                          AND COALESCE(facts_embedding_fingerprint,'') <> ''
                          AND facts_embedding_source_hash = ` + factsSourceExpr + `) AS last_updated
FROM viewer_profiles`).Scan(&profileStats).Error; err != nil {
		return store.WebEmbeddingStatus{}, &safeError{"统计档案向量失败", err}
	}
	st.Viewer = store.WebEmbeddingSection{Total: profileStats.Total, Embedded: profileStats.Embedded, Pending: profileStats.Total - profileStats.Embedded}
	if profileStats.LastUpdated != nil {
		st.Viewer.LastUpdated = *profileStats.LastUpdated
	}

	st.GeneratedAt = time.Now().UTC()
	return st, nil
}
