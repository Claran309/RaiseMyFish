//go:build integration

package database

// 本文件用真实 PG 测 0.10 记忆向量检索：范围/私密过滤、混合排序与降级。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// memoryVector 造一个第 idx 位为 1 的 1024 维测试向量。
func memoryVector(idx int) []float64 {
	vector := make([]float64, memory.EmbeddingDimension)
	vector[idx%len(vector)] = 1
	return vector
}

// insertSearchRecords 写入记忆并更新向量。
func insertSearchRecords(t *testing.T, ctx context.Context, repo *MemoryRepo) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	records := []store.MemoryRecord{
		{Record: memory.Record{ID: "mem-kw", Channel: event.ChLive, Session: "room", Summary: "主人爱白饭",
			Importance: 0.5, Confidence: 0.8, ValidFrom: now}, Source: "owner_direct", Kind: "note", Title: "白饭"},
		{Record: memory.Record{ID: "mem-sem", Channel: event.ChLive, Session: "room", Summary: "喜欢在深夜吃东西",
			Importance: 0.9, Confidence: 0.8, ValidFrom: now}, Source: "owner_direct", Kind: "note", Title: "夜宵"},
		{Record: memory.Record{ID: "mem-private", Channel: event.ChDM, Session: "owner", Private: true,
			Summary: "私密白饭", Importance: 0.5, Confidence: 0.8, ValidFrom: now}, Source: "owner_direct", Kind: "note", Title: "私密"},
	}
	for _, record := range records {
		if ok, err := repo.InsertRecord(ctx, record); err != nil || !ok {
			t.Fatalf("写入记忆 %s 失败：%v", record.ID, err)
		}
	}
	if err := repo.UpsertMemoryEmbeddings(ctx, []memory.EmbeddingRecord{
		{ID: "mem-kw", Hash: "h-kw", Embedding: memoryVector(0)},
		{ID: "mem-sem", Hash: "h-sem", Embedding: memoryVector(1)},
		{ID: "mem-private", Hash: "h-private", Embedding: memoryVector(0)},
	}); err != nil {
		t.Fatalf("写回记忆向量失败：%v", err)
	}
}

// TestMemoryKeywordAndSemanticScope 关键词与语义两路都必须遵守范围/私密过滤。
func TestMemoryKeywordAndSemanticScope(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	insertSearchRecords(t, ctx, repo)

	public := memory.Scope{Channel: event.ChLive, Session: "room"}
	keyword, err := repo.KeywordRecords(ctx, public, "白饭", 10)
	if err != nil {
		t.Fatalf("关键词检索失败：%v", err)
	}
	if len(keyword) != 1 || keyword[0].ID != "mem-kw" {
		t.Fatalf("关键词公开视角不符：%+v", keyword)
	}

	semantic, err := repo.SemanticRecords(ctx, public, memoryVector(0), 10)
	if err != nil {
		t.Fatalf("语义检索失败：%v", err)
	}
	if len(semantic) == 0 || semantic[0].Record.ID != "mem-kw" {
		t.Fatalf("语义公开视角应按相似度命中公开行：%+v", semantic)
	}
	if semantic[0].Similarity < 0.99 {
		t.Errorf("同向量相似度 = %v，期望接近 1", semantic[0].Similarity)
	}
	for _, hit := range semantic {
		if hit.Record.ID == "mem-private" {
			t.Fatal("公开视角读取到了私密记忆")
		}
	}

	// 私密视角能看到自己的私密行，也仍能看到公开行
	private := memory.Scope{Channel: event.ChDM, Session: "owner", Private: true}
	semantic, err = repo.SemanticRecords(ctx, private, memoryVector(0), 10)
	if err != nil {
		t.Fatalf("私密语义检索失败：%v", err)
	}
	seen := map[string]bool{}
	for _, hit := range semantic {
		seen[hit.Record.ID] = true
	}
	if !seen["mem-kw"] || !seen["mem-private"] {
		t.Fatalf("私密视角结果不符：%+v", semantic)
	}

	// 私密行不能借别的 DM 会话越权
	other := memory.Scope{Channel: event.ChDM, Session: "other", Private: true}
	semantic, err = repo.SemanticRecords(ctx, other, memoryVector(0), 10)
	if err != nil {
		t.Fatalf("越权视角检索失败：%v", err)
	}
	for _, hit := range semantic {
		if hit.Record.ID == "mem-private" {
			t.Fatal("其他 DM 会话读取到了私密记忆")
		}
	}
}

// fakeMemoryEmbedder 返回固定查询向量或错误。
type fakeMemoryEmbedder struct {
	vector []float64
	err    error
	calls  int
}

func (f *fakeMemoryEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	vectors := make([][]float64, len(inputs))
	for i := range vectors {
		vectors[i] = f.vector
	}
	return vectors, nil
}

// TestMemoryHybridSearchAndDegrade 混合排序走真实 SQL；embedding 失败降级纯关键词。
func TestMemoryHybridSearchAndDegrade(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	insertSearchRecords(t, ctx, repo)
	reader, err := memory.NewReader(repo, repo, memory.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "room"}

	embedder := &fakeMemoryEmbedder{vector: memoryVector(1)}
	hybrid, err := memory.NewHybridSearcher(reader, repo, embedder, memory.HybridSearchOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	got, err := hybrid.Search(ctx, memory.SearchQuery{Scope: scope, Text: "白饭", Limit: 10})
	if err != nil {
		t.Fatalf("混合检索失败：%v", err)
	}
	// mem-kw 关键词档位 1；mem-sem 语义 0.5。关键词命中必须在前。
	if len(got.Records) != 2 || got.Records[0].ID != "mem-kw" || got.Records[1].ID != "mem-sem" {
		t.Fatalf("混合排序不符：%+v", got.Records)
	}

	// 语义不可用：降级为关键词，仍能返回 mem-kw，且不报错
	var degraded error
	failing, err := memory.NewHybridSearcher(reader, repo, &fakeMemoryEmbedder{err: errors.New("embedding 不可用")}, memory.HybridSearchOptions{
		Alpha:           0.5,
		OnSemanticError: func(err error) { degraded = err },
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err = failing.Search(ctx, memory.SearchQuery{Scope: scope, Text: "白饭", Limit: 10})
	if err != nil {
		t.Fatalf("降级不应报错：%v", err)
	}
	if degraded == nil {
		t.Error("应触发降级回调")
	}
	if len(got.Records) != 1 || got.Records[0].ID != "mem-kw" {
		t.Fatalf("降级结果不符：%+v", got.Records)
	}
}

// TestMemoryEmbeddingIncremental 增量入库：第二次运行全部跳过。
func TestMemoryEmbeddingIncremental(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	insertSearchRecords(t, ctx, repo)

	// 清掉向量，模拟首次入库
	if err := db.WithContext(ctx).Exec("UPDATE memory_records SET embedding=NULL, embedding_text_hash=NULL").Error; err != nil {
		t.Fatal(err)
	}
	embedder := &fakeMemoryEmbedder{vector: memoryVector(0)}
	stats, err := memory.RunEmbedding(ctx, repo, embedder, memory.EmbedOptions{BatchSize: 2})
	if err != nil {
		t.Fatalf("首次入库失败：%v", err)
	}
	if stats.Embedded != 3 || stats.Pending != 3 {
		t.Fatalf("首次入库统计不符：%+v", stats)
	}
	stats, err = memory.RunEmbedding(ctx, repo, embedder, memory.EmbedOptions{BatchSize: 2})
	if err != nil {
		t.Fatalf("二次入库失败：%v", err)
	}
	if stats.Skipped != 3 || stats.Pending != 0 || stats.Embedded != 0 {
		t.Fatalf("二次入库未全部跳过：%+v", stats)
	}
}
