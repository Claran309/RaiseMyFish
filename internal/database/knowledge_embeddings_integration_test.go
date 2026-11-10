//go:build integration

package database

// 真实 PG + pgvector 集成：建表/索引、批量入库、余弦检索、混合打分与降级。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
)

type pgEmbedder struct {
	err    error
	inputs []string
}

func (e *pgEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	if e.err != nil {
		return nil, e.err
	}
	e.inputs = append(e.inputs, inputs...)
	vectors := make([][]float64, len(inputs))
	for i, input := range inputs {
		vectors[i] = knowledgeVector(input)
	}
	return vectors, nil
}

// knowledgeVector 用基向量模拟语义：文档带换行，查询是短文本。
func knowledgeVector(input string) []float64 {
	vector := make([]float64, knowledge.EmbeddingDimension)
	if !strings.Contains(input, "\n") {
		vector[0], vector[1] = 0.8, 0.6
		return vector
	}
	switch {
	case strings.Contains(input, "语义甲"):
		vector[0] = 1
	case strings.Contains(input, "语义乙"):
		vector[1] = 1
	default:
		vector[2] = 1
	}
	return vector
}

func writePGKnowledge(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	empty := "---\nschema: fish-knowledge/v1\nentries: []\n---\n"
	for _, name := range []string{"lore.md", "viewers.md", "games.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(empty), 0600); err != nil {
			t.Fatal(err)
		}
	}
	body := "---\nschema: fish-knowledge/v1\nentries:\n" +
		"  - id: title-entry\n    title: 甲乙丙\n    channel: [live, comment, dm]\n    tags: [招财]\n    priority: 50\n    visibility: public\n    source: test:title\n    summary: 甲乙丙标题条目\n    usage: 向量丙\n" +
		"  - id: semantic-first\n    title: 语义甲\n    channel: [live, comment, dm]\n    tags: [招财]\n    priority: 50\n    visibility: public\n    source: test:first\n    summary: 语义甲条目\n    usage: 甲类接法\n" +
		"  - id: semantic-second\n    title: 语义乙\n    channel: [live, comment, dm]\n    tags: [招财]\n    priority: 50\n    visibility: public\n    source: test:second\n    summary: 语义乙条目\n    usage: 乙类接法\n" +
		"  - id: dm-only\n    title: 私密条目\n    channel: [dm]\n    tags: [招财]\n    priority: 50\n    visibility: public\n    source: test:dm\n    summary: 只在私信场景\n    usage: 丁类接法\n" +
		"---\n"
	if err := os.WriteFile(filepath.Join(dir, "memes.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func recallKnowledgeIDs(t *testing.T, reader knowledge.Reader, scope knowledge.Scope, text string) []string {
	t.Helper()
	c, err := reader.Recall(context.Background(), knowledge.Query{Scope: scope, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(c.Items))
	for _, item := range c.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestKnowledgeEmbeddingsRealPostgres(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable("knowledge_embeddings") {
		t.Fatal("缺 knowledge_embeddings 表")
	}
	var index bool
	if err := db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname='knowledge_embeddings_vec_idx')").Scan(&index).Error; err != nil || !index {
		t.Fatalf("缺 hnsw 索引：%v", err)
	}

	reader, err := knowledge.NewFileReader(ctx, writePGKnowledge(t))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewKnowledgeEmbeddingRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &pgEmbedder{}
	stats, err := knowledge.RunEmbedding(ctx, reader, embedder, repo, knowledge.EmbedOptions{BatchSize: 2, Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 4 || stats.Embedded != 4 || stats.Batches != 2 {
		t.Fatalf("首批入库统计不符：%+v", stats)
	}
	var count int64
	if err := db.WithContext(ctx).Table("knowledge_embeddings").Count(&count).Error; err != nil || count != 4 {
		t.Fatalf("向量行数不符：%d %v", count, err)
	}
	// 幂等增量：内容与指纹未变时全部跳过，不重复写库。
	stats, err = knowledge.RunEmbedding(ctx, reader, embedder, repo, knowledge.EmbedOptions{BatchSize: 2, Fingerprint: testIndexFingerprint})
	if err != nil || stats.Skipped != 4 || stats.Embedded != 0 {
		t.Fatalf("增量未跳过：%+v %v", stats, err)
	}

	query := knowledgeVector("完全无关的查询")
	hits, err := repo.SemanticSearch(ctx, query, testIndexFingerprint, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 || hits[0].ID != "semantic-first" || hits[1].ID != "semantic-second" {
		t.Fatalf("余弦排序不符：%+v", hits)
	}
	if hits[0].Similarity < 0.79 || hits[0].Similarity > 0.81 || hits[1].Similarity < 0.59 || hits[1].Similarity > 0.61 {
		t.Fatalf("余弦相似度数值不符：%+v", hits[:2])
	}

	scope := knowledge.Scope{Channel: event.ChLive, Session: "room"}
	hybrid, err := knowledge.NewHybridReader(reader, embedder, repo, knowledge.HybridOptions{Alpha: 0.5, Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	// 语义-only：越界的 dm-only 被过滤；title-entry 余弦为 0，低于相关性下限被拒。
	if got := recallKnowledgeIDs(t, hybrid, scope, "完全无关的查询"); len(got) != 2 || got[0] != "semantic-first" || got[1] != "semantic-second" {
		t.Fatalf("混合语义召回不符：%v", got)
	}
	// 关键词标题档压过语义：标题命中仍排第一。
	if got := recallKnowledgeIDs(t, hybrid, scope, "甲乙丙"); len(got) != 3 || got[0] != "title-entry" {
		t.Fatalf("关键词档位未优先：%v", got)
	}

	// 降级：embedding 失败时只回关键词，不把错误抛给调用方。
	degradedCalls := 0
	degraded, err := knowledge.NewHybridReader(reader, &pgEmbedder{err: errors.New("embedding不可用")}, repo, knowledge.HybridOptions{
		Alpha:           0.5,
		Fingerprint:     testIndexFingerprint,
		OnSemanticError: func(error) { degradedCalls++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := recallKnowledgeIDs(t, reader, scope, "甲乙丙")
	if got := recallKnowledgeIDs(t, degraded, scope, "甲乙丙"); !reflect.DeepEqual(got, want) || degradedCalls != 1 {
		t.Fatalf("降级结果不符：got=%v want=%v calls=%d", got, want, degradedCalls)
	}
}

// TestKnowledgeEmbeddingIndexProtocolRealPostgres 验证换模型后不复用旧空间与写回 CAS。
func TestKnowledgeEmbeddingIndexProtocolRealPostgres(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	reader, err := knowledge.NewFileReader(ctx, writePGKnowledge(t))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewKnowledgeEmbeddingRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &pgEmbedder{}
	stats, err := knowledge.RunEmbedding(ctx, reader, embedder, repo, knowledge.EmbedOptions{BatchSize: 2, Fingerprint: "model-a"})
	if err != nil || stats.Embedded != 4 {
		t.Fatalf("模型A入库失败：%+v %v", stats, err)
	}
	query := knowledgeVector("完全无关的查询")
	if hits, err := repo.SemanticSearch(ctx, query, "model-b", 20); err != nil || len(hits) != 0 {
		t.Fatalf("换模型后仍命中旧空间：hits=%+v err=%v", hits, err)
	}
	stats, err = knowledge.RunEmbedding(ctx, reader, embedder, repo, knowledge.EmbedOptions{BatchSize: 2, Fingerprint: "model-b"})
	if err != nil || stats.Pending != 4 || stats.Embedded != 4 || stats.Rejected != 0 {
		t.Fatalf("换模型未重算：%+v %v", stats, err)
	}
	if hits, err := repo.SemanticSearch(ctx, query, "model-a", 20); err != nil || len(hits) != 0 {
		t.Fatalf("旧指纹查询仍返回向量：%+v %v", hits, err)
	}
	if hits, err := repo.SemanticSearch(ctx, query, "model-b", 20); err != nil || len(hits) != 4 {
		t.Fatalf("新指纹查询不符：%+v %v", hits, err)
	}

	var row struct{ SourceHash string }
	if err := db.WithContext(ctx).Raw("SELECT source_hash FROM knowledge_embeddings WHERE id='semantic-first'").Scan(&row).Error; err != nil || row.SourceHash == "" {
		t.Fatalf("读取行状态失败：%q %v", row.SourceHash, err)
	}
	newer := knowledge.EmbeddingRecord{
		ID: "semantic-first", Source: "memes.md", Title: "语义甲改",
		Hash: "new-text", SourceHash: "new-source", ExpectedSourceHash: row.SourceHash,
		Fingerprint: "model-b", Embedding: knowledgeVector("无关"),
	}
	if written, err := repo.UpsertEmbeddings(ctx, []knowledge.EmbeddingRecord{newer}); err != nil || written != 1 {
		t.Fatalf("新源写回失败：written=%d err=%v", written, err)
	}
	stale := newer
	stale.Hash = "stale-text"
	stale.SourceHash = row.SourceHash
	if written, err := repo.UpsertEmbeddings(ctx, []knowledge.EmbeddingRecord{stale}); err != nil || written != 0 {
		t.Fatalf("过期计算未被CAS拒绝：written=%d err=%v", written, err)
	}
	var after struct{ TextHash, SourceHash string }
	if err := db.WithContext(ctx).Raw("SELECT text_hash,source_hash FROM knowledge_embeddings WHERE id='semantic-first'").Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.TextHash != "new-text" || after.SourceHash != "new-source" {
		t.Fatalf("过期写回覆盖了新版本：%+v", after)
	}
}
