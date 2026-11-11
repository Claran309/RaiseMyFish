//go:build integration

package database

// 本文件用真实 PG 测面板向量入库统计：总数、已嵌入、待嵌入与最后同步时间。

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/viewer"
)

func TestWebEmbeddingStatusCounts(t *testing.T) {
	db, ctx := repositoryDB(t)
	memRepo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	insertSearchRecords(t, ctx, memRepo)
	viewerRepo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seedViewerProfiles(t, ctx, viewerRepo)
	if _, err := viewer.RunEmbedding(ctx, viewerRepo, &fakeViewerEmbedder{}, viewer.EmbedOptions{BatchSize: 2, Fingerprint: testIndexFingerprint}); err != nil {
		t.Fatalf("档案入库失败：%v", err)
	}
	knowledgeRepo, err := NewKnowledgeEmbeddingRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	synced := time.Now().UTC().Truncate(time.Microsecond)
	if written, err := knowledgeRepo.UpsertEmbeddings(ctx, []knowledge.EmbeddingRecord{{
		ID: "k1", Source: "lore.md", Title: "白饭", UpdatedAt: synced,
		Hash: "k1-text", SourceHash: "k1-source", Fingerprint: testIndexFingerprint,
		Embedding: memoryVector(0),
	}}); err != nil || written != 1 {
		t.Fatalf("知识入库失败：%d %v", written, err)
	}

	// 各制造一条待嵌入，验证 pending 与最后同步只统计已嵌入行。
	if err := db.WithContext(ctx).Exec(
		"UPDATE memory_records SET embedding=NULL, embedding_text_hash=NULL, embedding_source_hash=NULL, embedding_fingerprint=NULL, embedding_updated_at=NULL WHERE id='mem-sem'").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Exec(
		"UPDATE viewer_profiles SET facts_embedding=NULL, facts_embedding_hash=NULL, facts_embedding_source_hash=NULL, facts_embedding_fingerprint=NULL, facts_embedding_updated_at=NULL WHERE uid='4004'").Error; err != nil {
		t.Fatal(err)
	}

	repo, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	st, err := repo.EmbeddingStatus(ctx)
	if err != nil {
		t.Fatalf("读取向量状态失败：%v", err)
	}
	if st.Knowledge.Total != 1 || st.Knowledge.Embedded != 1 || st.Knowledge.Pending != 0 {
		t.Errorf("知识统计不符：%+v", st.Knowledge)
	}
	if st.Memory.Total != 3 || st.Memory.Embedded != 2 || st.Memory.Pending != 1 {
		t.Errorf("记忆统计不符：%+v", st.Memory)
	}
	if st.Viewer.Total != 3 || st.Viewer.Embedded != 2 || st.Viewer.Pending != 1 {
		t.Errorf("档案统计不符：%+v", st.Viewer)
	}
	if st.Knowledge.LastUpdated.IsZero() || st.Memory.LastUpdated.IsZero() || st.Viewer.LastUpdated.IsZero() {
		t.Errorf("最后同步时间缺失：knowledge=%v memory=%v viewer=%v",
			st.Knowledge.LastUpdated, st.Memory.LastUpdated, st.Viewer.LastUpdated)
	}
	if st.GeneratedAt.IsZero() {
		t.Error("缺少生成时间")
	}
}

func TestWebEmbeddingStatusEmpty(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	st, err := repo.EmbeddingStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Knowledge.Total != 0 || st.Knowledge.Embedded != 0 || st.Knowledge.Pending != 0 ||
		st.Memory.Total != 0 || st.Memory.Embedded != 0 || st.Memory.Pending != 0 ||
		st.Viewer.Total != 0 || st.Viewer.Embedded != 0 || st.Viewer.Pending != 0 {
		t.Errorf("空库统计应为零：%+v", st)
	}
	if !st.Knowledge.LastUpdated.IsZero() || !st.Memory.LastUpdated.IsZero() || !st.Viewer.LastUpdated.IsZero() {
		t.Errorf("空库不应有最后同步时间：%+v", st)
	}
	_ = ctx
}
