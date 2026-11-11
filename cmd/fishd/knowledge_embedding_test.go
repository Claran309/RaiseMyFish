package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/knowledge"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const testFishdFingerprint = "test-fishd-knowledge-fingerprint"

type fishdEmbedder struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (e *fishdEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	vectors := make([][]float64, len(inputs))
	for i := range inputs {
		vectors[i] = make([]float64, knowledge.EmbeddingDimension)
		vectors[i][e.calls%knowledge.EmbeddingDimension] = 1
	}
	return vectors, nil
}

func (e *fishdEmbedder) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type fishdEmbeddingStore struct {
	mu        sync.Mutex
	existing  map[string]knowledge.EmbeddingMeta
	records   []knowledge.EmbeddingRecord
	upserted  chan struct{}
	rejectAll bool
}

func newFishdEmbeddingStore() *fishdEmbeddingStore {
	return &fishdEmbeddingStore{existing: make(map[string]knowledge.EmbeddingMeta), upserted: make(chan struct{}, 16)}
}

func (s *fishdEmbeddingStore) ExistingEmbeddings(context.Context) (map[string]knowledge.EmbeddingMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]knowledge.EmbeddingMeta, len(s.existing))
	for id, meta := range s.existing {
		result[id] = meta
	}
	return result, nil
}

func (s *fishdEmbeddingStore) UpsertEmbeddings(_ context.Context, records []knowledge.EmbeddingRecord) (int, error) {
	s.mu.Lock()
	if s.rejectAll {
		s.mu.Unlock()
		return 0, nil
	}
	for _, record := range records {
		s.records = append(s.records, record)
		s.existing[record.ID] = knowledge.EmbeddingMeta{TextHash: record.Hash, SourceHash: record.SourceHash, Fingerprint: record.Fingerprint}
	}
	s.mu.Unlock()
	select {
	case s.upserted <- struct{}{}:
	default:
	}
	return len(records), nil
}

func (s *fishdEmbeddingStore) SemanticSearch(context.Context, []float64, string, int) ([]knowledge.SemanticHit, error) {
	return nil, nil
}

func (s *fishdEmbeddingStore) recordCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

type fishdEntry struct {
	ID    string
	Title string
}

func writeFishdEntries(t *testing.T, dir, name string, entries ...fishdEntry) {
	t.Helper()
	body := "---\nschema: fish-knowledge/v1\nentries:\n"
	for i, entry := range entries {
		body += fmt.Sprintf("  - id: %s\n    title: %s\n    channel: [live]\n    tags: [测试]\n    priority: 50\n    visibility: public\n    source: test:%s\n    summary: 摘要%d\n    usage: |-\n      接法%d\n", entry.ID, entry.Title, entry.ID, i, i)
	}
	body += "---\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeFishdMemeFile(t *testing.T, dir string, titles ...string) {
	t.Helper()
	entries := make([]fishdEntry, len(titles))
	for i, title := range titles {
		entries[i] = fishdEntry{ID: fmt.Sprintf("item-%d", i), Title: title}
	}
	writeFishdEntries(t, dir, "memes.md", entries...)
}

func newFishdKnowledge(t *testing.T, titles ...string) (*knowledge.FileReader, string) {
	t.Helper()
	dir := t.TempDir()
	empty := "---\nschema: fish-knowledge/v1\nentries: []\n---\n"
	for _, name := range []string{"lore.md", "viewers.md", "games.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(empty), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeFishdMemeFile(t, dir, titles...)
	reader, err := knowledge.NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return reader, dir
}

func TestAttachKnowledgeSemanticWrapsBaseReader(t *testing.T) {
	reader, _ := newFishdKnowledge(t, "甲鱼")
	assembly := knowledgeAssembly{Reader: reader, Base: reader, Diagnostic: knowledgeDiagnostic{Enabled: true, Assembled: true}}
	cfg := config.Config{Embedding: config.Embedding{
		Enabled: true, URL: "https://example.com/v1/embeddings", Model: "embedding-3",
		Key: "unit-key", Dimension: knowledge.EmbeddingDimension, BatchSize: 32, Alpha: 0.5,
	}}
	if err := attachKnowledgeSemantic(cfg, &gorm.DB{}, &assembly, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	if _, ok := assembly.Reader.(*knowledge.HybridReader); !ok || assembly.Embedder == nil || assembly.Store == nil {
		t.Fatal("语义装配未包装为混合召回")
	}
	if assembly.Fingerprint == "" {
		t.Fatal("装配未记录索引指纹")
	}
	if assembly.Base != reader {
		t.Fatal("Base快照被替换")
	}

	disabled := cfg
	disabled.Embedding.Enabled = false
	plain := knowledgeAssembly{Reader: reader, Base: reader}
	if err := attachKnowledgeSemantic(disabled, &gorm.DB{}, &plain, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	if _, ok := plain.Reader.(*knowledge.FileReader); !ok {
		t.Fatal("未启用embedding时不应包装")
	}

	invalid := cfg
	invalid.Embedding.URL = "http://example.com/v1/embeddings"
	broken := knowledgeAssembly{Reader: reader, Base: reader}
	if err := attachKnowledgeSemantic(invalid, &gorm.DB{}, &broken, zap.NewNop()); err == nil {
		t.Fatal("非法地址未拒绝")
	}
	if _, ok := broken.Reader.(*knowledge.FileReader); !ok {
		t.Fatal("装配失败后Reader应保持纯关键词")
	}
	if err := attachKnowledgeSemantic(cfg, nil, &broken, zap.NewNop()); err == nil {
		t.Fatal("空数据库未拒绝")
	}
}

func TestKnowledgeEmbeddingRefresherOnlyOnRevisionChange(t *testing.T) {
	reader, dir := newFishdKnowledge(t, "甲鱼")
	writeFishdEntries(t, dir, "lore.md", fishdEntry{ID: "lore-1", Title: "乙鱼"})
	if err := reader.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	embedder := &fishdEmbedder{}
	store := newFishdEmbeddingStore()
	refresher := &knowledgeEmbeddingRefresher{
		reader: reader, embedder: embedder, store: store, fingerprint: testFishdFingerprint,
		batchSize: 1, logger: zap.NewNop(),
	}
	if err := refresher.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.recordCount() != 2 || refresher.lastRevision == "" {
		t.Fatalf("首次未全量入库：records=%d revision=%q", store.recordCount(), refresher.lastRevision)
	}
	firstRevision := refresher.lastRevision
	if err := refresher.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.recordCount() != 2 || embedder.callCount() != 2 || refresher.lastRevision != firstRevision {
		t.Fatalf("revision未变仍调用embedding：records=%d calls=%d", store.recordCount(), embedder.callCount())
	}

	// 只改 memes.md：lore.md 的条目按文件 mtime 跳过，变化文件内的条目重新向量化。
	writeFishdMemeFile(t, dir, "丙鱼")
	if err := reader.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := refresher.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.recordCount() != 3 || embedder.callCount() != 3 || refresher.lastRevision == firstRevision {
		t.Fatalf("revision变化未增量：records=%d calls=%d", store.recordCount(), embedder.callCount())
	}
}

func TestKnowledgeEmbeddingRefresherRetriesAfterFailure(t *testing.T) {
	reader, _ := newFishdKnowledge(t, "甲鱼")
	embedder := &fishdEmbedder{err: errors.New("embedding不可用")}
	store := newFishdEmbeddingStore()
	refresher := &knowledgeEmbeddingRefresher{
		reader: reader, embedder: embedder, store: store, fingerprint: testFishdFingerprint,
		batchSize: 32, logger: zap.NewNop(),
	}
	if err := refresher.runOnce(context.Background()); err == nil {
		t.Fatal("失败未返回")
	}
	if refresher.lastRevision != "" || store.recordCount() != 0 {
		t.Fatal("失败不应推进revision或写库")
	}
	embedder.err = nil
	if err := refresher.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.recordCount() != 1 || refresher.lastRevision == "" {
		t.Fatal("恢复后未重试成功")
	}
}

// TestKnowledgeEmbeddingRefresherRetriesRejectedWrites CAS 拒绝后不推进 revision。
func TestKnowledgeEmbeddingRefresherRetriesRejectedWrites(t *testing.T) {
	reader, _ := newFishdKnowledge(t, "甲鱼")
	store := newFishdEmbeddingStore()
	store.rejectAll = true
	refresher := &knowledgeEmbeddingRefresher{
		reader: reader, embedder: &fishdEmbedder{}, store: store, fingerprint: testFishdFingerprint,
		batchSize: 32, logger: zap.NewNop(),
	}
	if err := refresher.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refresher.lastRevision != "" {
		t.Fatal("CAS 拒绝后不应推进 revision")
	}
	store.rejectAll = false
	if err := refresher.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refresher.lastRevision == "" || store.recordCount() != 1 {
		t.Fatalf("恢复后未重试成功：revision=%q records=%d", refresher.lastRevision, store.recordCount())
	}
}

func TestStartKnowledgeEmbeddingRefreshRunsAndStops(t *testing.T) {
	reader, _ := newFishdKnowledge(t, "甲鱼")
	store := newFishdEmbeddingStore()
	assembly := knowledgeAssembly{Base: reader, Embedder: &fishdEmbedder{}, Store: store, Fingerprint: testFishdFingerprint}
	cfg := config.Config{Embedding: config.Embedding{Enabled: true, AutoRefresh: true, RefreshIntervalSeconds: 3600, BatchSize: 32}}
	stop := startKnowledgeEmbeddingRefresh(context.Background(), cfg, assembly, zap.NewNop())
	select {
	case <-store.upserted:
	case <-time.After(2 * time.Second):
		t.Fatal("定时增量未执行首轮")
	}
	stop()

	disabled := cfg
	disabled.Embedding.AutoRefresh = false
	embedder := &fishdEmbedder{}
	stop = startKnowledgeEmbeddingRefresh(context.Background(), disabled, knowledgeAssembly{Base: reader, Embedder: embedder, Store: newFishdEmbeddingStore()}, zap.NewNop())
	stop()
	if embedder.callCount() != 0 {
		t.Fatal("auto_refresh关闭仍调用embedding")
	}
}
