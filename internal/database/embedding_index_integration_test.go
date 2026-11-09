//go:build integration

package database

// 本文件用真实 PG 验收 C2/N08：源版本、文本版本与索引指纹分层。
//
// 两个核心场景：
//  1. barrier 卡住 embedding 计算：读旧源 → 另一路更新/清洗并清空向量 → 旧计算返回，
//     写回必须被 CAS 拒绝，不能把刚清掉的旧向量种回去；
//  2. 摘要/发言已变但向量还在时，查询必须拒绝 stale 行，不能拿旧语义给新正文排序。

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"gorm.io/gorm"
)

// barrierEmbedder 在计算返回前卡住，供测试插入「另一路更新/清洗」。
type barrierEmbedder struct {
	started chan string
	release chan struct{}
}

func newBarrierEmbedder() *barrierEmbedder {
	return &barrierEmbedder{started: make(chan string, 1), release: make(chan struct{})}
}

func (b *barrierEmbedder) Embed(ctx context.Context, inputs []string) ([][]float64, error) {
	select {
	case b.started <- inputs[0]:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	vectors := make([][]float64, len(inputs))
	for i := range vectors {
		vectors[i] = memoryVector(0)
	}
	return vectors, nil
}

// waitBarrier 等 embedding 计算真正开始。
func waitBarrier(t *testing.T, b *barrierEmbedder) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("embedding 计算未在预期时间内开始")
	}
}

// memoryRecord 造一条用于索引状态验收的公开事实。
func memoryRecord(id, summary string, at time.Time) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{
			ID: id, Channel: event.ChLive, Session: "room", Summary: summary,
			Importance: 0.5, Confidence: 0.8, ValidFrom: at,
		},
		Source: "persona", Kind: "fact", Title: "标题",
	}
}

// TestMemoryEmbeddingCASRejectsAfterSourceChange 读旧源 → 更新并清空向量 → 旧计算返回必须被拒。
func TestMemoryEmbeddingCASRejectsAfterSourceChange(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	record := memoryRecord("fact:barrier", "旧摘要：爱吃白饭", at)
	if ok, err := repo.InsertRecord(ctx, record); err != nil || !ok {
		t.Fatalf("写入记忆失败：%v ok=%v", err, ok)
	}

	embedder := newBarrierEmbedder()
	type result struct {
		stats memory.EmbedStats
		err   error
	}
	done := make(chan result, 1)
	go func() {
		stats, err := memory.RunEmbedding(ctx, repo, embedder, memory.EmbedOptions{
			BatchSize: 8, Fingerprint: testIndexFingerprint,
		})
		done <- result{stats: stats, err: err}
	}()

	// 旧计算已经开始（旧源已被读取），此时另一路更新摘要并清空向量。
	waitBarrier(t, embedder)
	updated := record
	updated.Summary = "新摘要：改吃面条"
	updated.ValidFrom = at.Add(time.Second)
	if ok, err := repo.InsertRecord(ctx, updated); err != nil || !ok {
		t.Fatalf("更新摘要失败：%v ok=%v", err, ok)
	}
	if err := db.WithContext(ctx).Exec(
		"UPDATE memory_records SET embedding=NULL, embedding_text_hash=NULL WHERE id='fact:barrier'").Error; err != nil {
		t.Fatal(err)
	}
	close(embedder.release)
	got := <-done
	if got.err != nil {
		t.Fatalf("入库失败：%v", got.err)
	}
	if got.stats.Embedded != 0 || got.stats.Rejected != 1 {
		t.Fatalf("旧计算应被 CAS 拒绝：%+v", got.stats)
	}

	var stillNull struct {
		EmbeddingNull bool
		HashEmpty     bool
	}
	if err := db.WithContext(ctx).Raw(`
SELECT embedding IS NULL AS embedding_null,
       COALESCE(embedding_text_hash,'') = '' AS hash_empty
FROM memory_records WHERE id='fact:barrier'`).Scan(&stillNull).Error; err != nil {
		t.Fatal(err)
	}
	if !stillNull.EmbeddingNull || !stillNull.HashEmpty {
		t.Fatalf("被拒绝的写回仍恢复了旧向量：%+v", stillNull)
	}
}

// TestMemorySemanticQueryRejectsStaleSource 摘要更新后旧向量仍在，但查询必须拒绝 stale。
func TestMemorySemanticQueryRejectsStaleSource(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	record := memoryRecord("fact:stale", "旧摘要：爱吃白饭", at)
	if ok, err := repo.InsertRecord(ctx, record); err != nil || !ok {
		t.Fatalf("写入记忆失败：%v ok=%v", err, ok)
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "room"}
	if _, err := memory.RunEmbedding(ctx, repo, &fakeMemoryEmbedder{vector: memoryVector(0)}, memory.EmbedOptions{
		Fingerprint: testIndexFingerprint,
	}); err != nil {
		t.Fatalf("首次入库失败：%v", err)
	}
	if hits, err := repo.SemanticRecords(ctx, scope, memoryVector(0), testIndexFingerprint, 10); err != nil || len(hits) != 1 {
		t.Fatalf("入库后应能召回：%+v / %v", hits, err)
	}

	// 只更新摘要，不清向量：模拟旧的「摘要更新保留旧向量」路径。
	updated := record
	updated.Summary = "新摘要：改吃面条"
	updated.ValidFrom = at.Add(time.Second)
	if ok, err := repo.InsertRecord(ctx, updated); err != nil || !ok {
		t.Fatalf("更新摘要失败：%v ok=%v", err, ok)
	}
	var retained bool
	if err := db.WithContext(ctx).Raw(
		"SELECT embedding IS NOT NULL FROM memory_records WHERE id='fact:stale'").Scan(&retained).Error; err != nil {
		t.Fatal(err)
	}
	if !retained {
		t.Fatal("前置条件不成立：更新摘要后旧向量应仍然存在")
	}
	if hits, err := repo.SemanticRecords(ctx, scope, memoryVector(0), testIndexFingerprint, 10); err != nil || len(hits) != 0 {
		t.Fatalf("stale 向量不得参与排序：%+v / %v", hits, err)
	}

	// 重新入库后恢复可召回；换指纹（同维度换模型）必须再次拒绝。
	if _, err := memory.RunEmbedding(ctx, repo, &fakeMemoryEmbedder{vector: memoryVector(0)}, memory.EmbedOptions{
		Fingerprint: testIndexFingerprint,
	}); err != nil {
		t.Fatalf("重新入库失败：%v", err)
	}
	if hits, err := repo.SemanticRecords(ctx, scope, memoryVector(0), testIndexFingerprint, 10); err != nil || len(hits) != 1 {
		t.Fatalf("重新入库后应恢复召回：%+v / %v", hits, err)
	}
	if hits, err := repo.SemanticRecords(ctx, scope, memoryVector(0), testIndexFingerprint+"-other-model", 10); err != nil || len(hits) != 0 {
		t.Fatalf("指纹不一致的索引不得参与排序：%+v / %v", hits, err)
	}
}

// viewerProfileRow 造一份用于索引状态验收的观众档案。
func viewerProfileRow(uid, name string, texts []string) store.ViewerProfile {
	return store.ViewerProfile{
		UID: uid, Name: name, Hits: 3, Replies: 0,
		LastSeen: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), RecentTexts: texts,
	}
}

// clearViewerKind 模拟重建/清洗时清空某一路的索引状态。
func clearViewerKind(t *testing.T, db *gorm.DB, ctx context.Context, uid string, kind viewer.EmbeddingKind) {
	t.Helper()
	columns, ok := map[viewer.EmbeddingKind]string{
		viewer.KindIdentity:    "identity",
		viewer.KindFacts:       "facts",
		viewer.KindInteraction: "interaction",
	}[kind]
	if !ok {
		t.Fatalf("未知索引路：%s", kind)
	}
	if err := db.WithContext(ctx).Exec(`
UPDATE viewer_profiles
SET `+columns+`_embedding=NULL, `+columns+`_embedding_hash=NULL,
    `+columns+`_embedding_source_hash=NULL, `+columns+`_embedding_fingerprint=NULL,
    `+columns+`_embedding_updated_at=NULL
WHERE uid=?`, uid).Error; err != nil {
		t.Fatal(err)
	}
}

// TestViewerEmbeddingCASRejectsAfterKindSourceChange 某一路的发言被重建清空后，旧计算不能写回。
func TestViewerEmbeddingCASRejectsAfterKindSourceChange(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seed := viewerProfileRow("7007", "白饭哥", []string{"旧发言：爱吃白饭"})
	if err := repo.UpsertProfile(ctx, seed); err != nil {
		t.Fatalf("写入档案失败：%v", err)
	}

	embedder := newBarrierEmbedder()
	type result struct {
		stats viewer.EmbedStats
		err   error
	}
	done := make(chan result, 1)
	go func() {
		stats, err := viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{
			BatchSize: 8, Fingerprint: testIndexFingerprint,
		})
		done <- result{stats: stats, err: err}
	}()

	// identity/facts 两路都在这一批里；先改 facts 源再清空它。
	waitBarrier(t, embedder)
	if err := repo.UpsertProfile(ctx, viewerProfileRow("7007", "白饭哥", []string{"重建后的干净发言"})); err != nil {
		t.Fatalf("重建档案失败：%v", err)
	}
	clearViewerKind(t, db, ctx, "7007", viewer.KindFacts)
	close(embedder.release)
	got := <-done
	if got.err != nil {
		t.Fatalf("入库失败：%v", got.err)
	}
	// identity 源没变可以写回；facts 源已变必须被拒。
	if got.stats.Embedded != 1 || got.stats.Rejected != 1 {
		t.Fatalf("CAS 结果不符：%+v", got.stats)
	}
	var factsNull struct {
		EmbeddingNull bool
		HashEmpty     bool
	}
	if err := db.WithContext(ctx).Raw(`
SELECT facts_embedding IS NULL AS embedding_null,
       COALESCE(facts_embedding_hash,'') = '' AS hash_empty
FROM viewer_profiles WHERE uid='7007'`).Scan(&factsNull).Error; err != nil {
		t.Fatal(err)
	}
	if !factsNull.EmbeddingNull || !factsNull.HashEmpty {
		t.Fatalf("被拒绝的写回仍恢复了 facts 旧向量：%+v", factsNull)
	}

	// 下一轮按新源重算 facts，索引恢复可用。
	if _, err := viewer.RunEmbedding(ctx, repo, &fakeViewerEmbedder{}, viewer.EmbedOptions{
		Fingerprint: testIndexFingerprint,
	}); err != nil {
		t.Fatalf("重算失败：%v", err)
	}
	var factsPresent bool
	if err := db.WithContext(ctx).Raw(
		"SELECT facts_embedding IS NOT NULL FROM viewer_profiles WHERE uid='7007'").Scan(&factsPresent).Error; err != nil {
		t.Fatal(err)
	}
	if !factsPresent {
		t.Fatal("新源重算后 facts 向量应已写回")
	}
}

// TestViewerSemanticQueryRejectsStaleSource 档案发言更新后，旧 facts 向量必须退出排序。
func TestViewerSemanticQueryRejectsStaleSource(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewViewerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertProfile(ctx, viewerProfileRow("7008", "夜猫子", []string{"半夜吃播开始了"})); err != nil {
		t.Fatalf("写入档案失败：%v", err)
	}
	embedder := &fakeViewerEmbedder{}
	if _, err := viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{Fingerprint: testIndexFingerprint}); err != nil {
		t.Fatalf("首次入库失败：%v", err)
	}
	if hits, err := repo.SemanticProfiles(ctx, viewer.KindFacts, viewerVector(1), testIndexFingerprint, 10); err != nil || len(hits) != 1 {
		t.Fatalf("入库后应能召回：%+v / %v", hits, err)
	}

	// 只更新发言，不清向量：旧 facts 向量还在但已 stale。
	if err := repo.UpsertProfile(ctx, viewerProfileRow("7008", "夜猫子", []string{"改吃面条了"})); err != nil {
		t.Fatalf("更新档案失败：%v", err)
	}
	var retained bool
	if err := db.WithContext(ctx).Raw(
		"SELECT facts_embedding IS NOT NULL FROM viewer_profiles WHERE uid='7008'").Scan(&retained).Error; err != nil {
		t.Fatal(err)
	}
	if !retained {
		t.Fatal("前置条件不成立：更新发言后旧 facts 向量应仍然存在")
	}
	if hits, err := repo.SemanticProfiles(ctx, viewer.KindFacts, viewerVector(1), testIndexFingerprint, 10); err != nil || len(hits) != 0 {
		t.Fatalf("stale facts 向量不得参与排序：%+v / %v", hits, err)
	}

	// 重算后恢复；指纹不一致仍然拒绝。
	if _, err := viewer.RunEmbedding(ctx, repo, embedder, viewer.EmbedOptions{Fingerprint: testIndexFingerprint}); err != nil {
		t.Fatalf("重算失败：%v", err)
	}
	if hits, err := repo.SemanticProfiles(ctx, viewer.KindFacts, viewerVector(2), testIndexFingerprint, 10); err != nil || len(hits) != 1 {
		t.Fatalf("重算后应恢复召回：%+v / %v", hits, err)
	}
	if hits, err := repo.SemanticProfiles(ctx, viewer.KindFacts, viewerVector(2), testIndexFingerprint+"-other-model", 10); err != nil || len(hits) != 0 {
		t.Fatalf("指纹不一致的档案索引不得参与排序：%+v / %v", hits, err)
	}
}
