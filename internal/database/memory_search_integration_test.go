//go:build integration

package database

// 本文件用真实 PG 测 0.10 记忆向量检索：范围/私密过滤、混合排序与降级。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// testIndexFingerprint 是集成测试使用的索引指纹。
const testIndexFingerprint = "integration-fingerprint"

// memoryVector 造一个第 idx 位为 1 的 1024 维测试向量。
func memoryVector(idx int) []float64 {
	vector := make([]float64, memory.EmbeddingDimension)
	vector[idx%len(vector)] = 1
	return vector
}

// memorySourceHashes 按仓储给出的当前源字段 hash 建 ID 索引。
func memorySourceHashes(t *testing.T, ctx context.Context, repo *MemoryRepo) map[string]string {
	t.Helper()
	inputs, err := repo.MemoryEmbeddingInputs(ctx, true)
	if err != nil {
		t.Fatalf("读取入库输入失败：%v", err)
	}
	hashes := make(map[string]string, len(inputs))
	for _, input := range inputs {
		hashes[input.ID] = input.SourceHash
	}
	return hashes
}

// insertSearchRecords 写入记忆并更新向量（源 hash 取自当前行，模拟真实写回）。
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
	hashes := memorySourceHashes(t, ctx, repo)
	written, err := repo.UpsertMemoryEmbeddings(ctx, []memory.EmbeddingRecord{
		{ID: "mem-kw", Hash: "h-kw", SourceHash: hashes["mem-kw"], Fingerprint: testIndexFingerprint, Embedding: memoryVector(0)},
		{ID: "mem-sem", Hash: "h-sem", SourceHash: hashes["mem-sem"], Fingerprint: testIndexFingerprint, Embedding: memoryVector(1)},
		{ID: "mem-private", Hash: "h-private", SourceHash: hashes["mem-private"], Fingerprint: testIndexFingerprint, Embedding: memoryVector(0)},
	})
	if err != nil {
		t.Fatalf("写回记忆向量失败：%v", err)
	}
	if written != 3 {
		t.Fatalf("写回记忆向量条数 = %d，期望 3", written)
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

	semantic, err := repo.SemanticRecords(ctx, public, memoryVector(0), testIndexFingerprint, 10)
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
	semantic, err = repo.SemanticRecords(ctx, private, memoryVector(0), testIndexFingerprint, 10)
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
	semantic, err = repo.SemanticRecords(ctx, other, memoryVector(0), testIndexFingerprint, 10)
	if err != nil {
		t.Fatalf("越权视角检索失败：%v", err)
	}
	for _, hit := range semantic {
		if hit.Record.ID == "mem-private" {
			t.Fatal("其他 DM 会话读取到了私密记忆")
		}
	}
}

// TestMemoryKeywordMessagesRecall 摘要为空时用 events 原文召回，且不越界。
func TestMemoryKeywordMessagesRecall(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(id, channel, session, text string, private bool, at time.Time) {
		t.Helper()
		err := repo.db.WithContext(ctx).Exec(`INSERT INTO events(id,channel,dir,session,from_id,from_name,text,private,at,status)
VALUES(?,?,?,?,?,?,?,?,?,?)`, id, channel, "in", session, "3003", "弓长三益", text, private, at, "done").Error
		if err != nil {
			t.Fatalf("写入事件 %s 失败：%v", id, err)
		}
	}
	// 真实形态：同一个观众在本房间说了 12 句。
	for i := 0; i < 12; i++ {
		insert(fmt.Sprintf("live:room:g%d", i), "live", "room", fmt.Sprintf("第%d条", i), false, now.Add(time.Duration(i)*time.Second))
	}
	insert("live:other:g", "live", "other", "别的房间", false, now)
	insert("dm:owner:g", "dm", "owner", "私信里的弓长三益", true, now)

	scope := memory.Scope{Channel: event.ChLive, Session: "room"}
	got, err := repo.KeywordMessages(ctx, scope, "弓长三益", 20)
	if err != nil {
		t.Fatalf("原文召回失败：%v", err)
	}
	if len(got) != 12 {
		t.Fatalf("应召回本房间 12 条，实际 %d：%+v", len(got), got)
	}
	for _, m := range got {
		if m.Session != "room" || m.Private {
			t.Fatalf("越界召回：%+v", m)
		}
	}

	// limit 取最新 5 条，返回仍按时间升序；最后一条应是第 11 条。
	got, err = repo.KeywordMessages(ctx, scope, "弓长三益", 5)
	if err != nil {
		t.Fatalf("限量召回失败：%v", err)
	}
	if len(got) != 5 || got[4].Text != "第11条" {
		t.Fatalf("限量/排序不符：%+v", got)
	}

	otherHits, err := repo.KeywordMessages(ctx, memory.Scope{Channel: event.ChLive, Session: "other"}, "私信", 10)
	if err != nil {
		t.Fatalf("别的房间查询失败：%v", err)
	}
	if len(otherHits) != 0 {
		t.Fatalf("公开视角读到了私信：%+v", otherHits)
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
	hybrid, err := memory.NewHybridSearcher(reader, repo, embedder, memory.HybridSearchOptions{Alpha: 0.5, Fingerprint: testIndexFingerprint})
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
		Fingerprint:     testIndexFingerprint,
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

	// 清掉向量与索引状态，模拟首次入库
	if err := db.WithContext(ctx).Exec("UPDATE memory_records SET embedding=NULL, embedding_text_hash=NULL, embedding_source_hash=NULL, embedding_fingerprint=NULL").Error; err != nil {
		t.Fatal(err)
	}
	embedder := &fakeMemoryEmbedder{vector: memoryVector(0)}
	// 默认未授权私密：只有两条公开记录参与向量化。
	stats, err := memory.RunEmbedding(ctx, repo, embedder, memory.EmbedOptions{BatchSize: 2, Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatalf("首次入库失败：%v", err)
	}
	if stats.Total != 2 || stats.Embedded != 2 || stats.Pending != 2 {
		t.Fatalf("默认授权入库统计不符：%+v", stats)
	}
	// 显式授权后：私密行补齐，已入库的公开行按 hash 跳过。
	stats, err = memory.RunEmbedding(ctx, repo, embedder, memory.EmbedOptions{BatchSize: 2, AllowPrivate: true, Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatalf("授权入库失败：%v", err)
	}
	if stats.Total != 3 || stats.Skipped != 2 || stats.Pending != 1 || stats.Embedded != 1 {
		t.Fatalf("授权入库统计不符：%+v", stats)
	}
	stats, err = memory.RunEmbedding(ctx, repo, embedder, memory.EmbedOptions{BatchSize: 2, AllowPrivate: true, Fingerprint: testIndexFingerprint})
	if err != nil {
		t.Fatalf("二次入库失败：%v", err)
	}
	if stats.Skipped != 3 || stats.Pending != 0 || stats.Embedded != 0 {
		t.Fatalf("二次入库未全部跳过：%+v", stats)
	}
}
