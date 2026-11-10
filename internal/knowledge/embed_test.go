package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/llmmeta"
	"go.yaml.in/yaml/v3"
)

type fakeEmbedder struct {
	mu        sync.Mutex
	calls     int
	inputs    [][]string
	failFirst int
	err       error
	factory   func(input string) []float64
	call      llmmeta.Call
}

func (f *fakeEmbedder) Embed(ctx context.Context, inputs []string) ([][]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.call = llmmeta.From(ctx)
	f.inputs = append(f.inputs, append([]string(nil), inputs...))
	if f.calls <= f.failFirst {
		if f.err != nil {
			return nil, f.err
		}
		return nil, errors.New("模拟embedding失败")
	}
	vectors := make([][]float64, len(inputs))
	for i, input := range inputs {
		if f.factory != nil {
			vectors[i] = f.factory(input)
		} else {
			vectors[i] = unitEmbedding(f.calls*len(inputs) + i)
		}
	}
	return vectors, nil
}

func unitEmbedding(seed int) []float64 {
	vector := make([]float64, EmbeddingDimension)
	vector[seed%EmbeddingDimension] = 1
	return vector
}

const testKnowledgeFingerprint = "test-knowledge-fingerprint"

type fakeEmbeddingStore struct {
	mu              sync.Mutex
	existing        map[string]EmbeddingMeta
	records         []EmbeddingRecord
	semantic        []SemanticHit
	errExist        error
	errUpsert       error
	errSearch       error
	rejectAll       bool
	lastLimit       int
	lastVector      []float64
	lastFingerprint string
}

func newFakeEmbeddingStore() *fakeEmbeddingStore {
	return &fakeEmbeddingStore{existing: make(map[string]EmbeddingMeta)}
}

func (s *fakeEmbeddingStore) ExistingEmbeddings(context.Context) (map[string]EmbeddingMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errExist != nil {
		return nil, s.errExist
	}
	result := make(map[string]EmbeddingMeta, len(s.existing))
	for id, meta := range s.existing {
		result[id] = meta
	}
	return result, nil
}

func (s *fakeEmbeddingStore) UpsertEmbeddings(_ context.Context, records []EmbeddingRecord) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errUpsert != nil {
		return 0, s.errUpsert
	}
	if s.rejectAll {
		return 0, nil
	}
	for _, record := range records {
		s.records = append(s.records, record)
		s.existing[record.ID] = EmbeddingMeta{TextHash: record.Hash, SourceHash: record.SourceHash, Fingerprint: record.Fingerprint}
	}
	return len(records), nil
}

func (s *fakeEmbeddingStore) SemanticSearch(_ context.Context, vector []float64, fingerprint string, limit int) ([]SemanticHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.errSearch != nil {
		return nil, s.errSearch
	}
	s.lastLimit = limit
	s.lastVector = append([]float64(nil), vector...)
	s.lastFingerprint = fingerprint
	return append([]SemanticHit(nil), s.semantic...), nil
}

func writeEmbeddingEntries(t *testing.T, dir, name string, entries ...diskEntry) {
	t.Helper()
	data, err := yaml.Marshal(diskFile{Schema: "fish-knowledge/v1", Entries: &entries})
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledge(t, dir, name, "---\n"+string(data)+"---\n")
}

func embeddingReader(t *testing.T, dir string) *FileReader {
	t.Helper()
	reader, err := NewFileReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

// TestEmbeddingTextUsesTitleUsageTags 输入只拼 title/usage/tags，标签去重且保持顺序。
func TestEmbeddingTextUsesTitleUsageTags(t *testing.T) {
	entry := entry{
		title:    "标题甲",
		usage:    "接法甲",
		tags:     []string{"标签乙", "标签甲", "标签乙"},
		keywords: []string{"不应进入"},
		summary:  "不应进入",
	}
	if got := embeddingText(entry); got != "标题甲\n接法甲\n标签乙 标签甲" {
		t.Fatalf("embedding 输入拼接不符：%q", got)
	}
}

// TestRunEmbeddingIncrementalAndBatches 首次全量、文件未变跳过、单文件变化只重算该文件。
func TestRunEmbeddingIncrementalAndBatches(t *testing.T) {
	dir := knowledgeDir(t)
	first := testEntry("first", "标题一")
	first.Usage = "接法一"
	tags := []string{"标签一", "标签二"}
	first.Tags = &tags
	second := testEntry("second", "标题二")
	writeEmbeddingEntries(t, dir, "memes.md", first, second)
	writeEmbeddingEntries(t, dir, "memes/learned.md", testEntry("learned", "标题三"))
	reader := embeddingReader(t, dir)

	embedder := &fakeEmbedder{}
	store := newFakeEmbeddingStore()
	var progress []int
	stats, err := RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{
		BatchSize:   2,
		Fingerprint: testKnowledgeFingerprint,
		RetryDelay:  time.Millisecond,
		Progress: func(done, total, batches int) {
			if total != 3 || batches != 2 {
				t.Fatalf("进度参数错误：%d/%d/%d", done, total, batches)
			}
			progress = append(progress, done)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 || stats.Pending != 3 || stats.Skipped != 0 || stats.Embedded != 3 || stats.Batches != 2 {
		t.Fatalf("首次统计错误：%+v", stats)
	}
	if len(progress) != 2 || progress[0] != 2 || progress[1] != 3 {
		t.Fatalf("批次进度错误：%v", progress)
	}
	if len(store.records) != 3 {
		t.Fatalf("写入条数错误：%d", len(store.records))
	}
	if embedder.call.Purpose != llmmeta.PurposeEmbedIndex || embedder.call.CorrelationID != "knowledge" {
		t.Fatalf("索引向量化用途未标注：%+v", embedder.call)
	}
	var learnedInput string
	for _, batch := range embedder.inputs {
		for _, input := range batch {
			if strings.Contains(input, "标题一") {
				learnedInput = input
			}
		}
	}
	if learnedInput != "标题一\n接法一\n标签一 标签二" {
		t.Fatalf("embedding 输入不符：%q", learnedInput)
	}

	stats, err = RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{BatchSize: 2, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 3 || stats.Pending != 0 || stats.Embedded != 0 {
		t.Fatalf("未变化时不应重算：%+v", stats)
	}

	time.Sleep(10 * time.Millisecond)
	writeEmbeddingEntries(t, dir, "memes/learned.md", testEntry("learned", "标题三改"))
	if err := reader.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats, err = RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{BatchSize: 2, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 || stats.Skipped != 2 || stats.Pending != 1 || stats.Embedded != 1 {
		t.Fatalf("单文件变化增量错误：%+v", stats)
	}
}

// TestRunEmbeddingRequiresFingerprint 指纹是向量空间身份，缺失时拒绝执行。
func TestRunEmbeddingRequiresFingerprint(t *testing.T) {
	dir := knowledgeDir(t)
	writeEmbeddingEntries(t, dir, "memes.md", testEntry("one", "标题一"))
	reader := embeddingReader(t, dir)
	store := newFakeEmbeddingStore()
	if _, err := RunEmbedding(context.Background(), reader, &fakeEmbedder{}, store, EmbedOptions{BatchSize: 1}); err == nil || !strings.Contains(err.Error(), "索引指纹") {
		t.Fatalf("缺少指纹未拒绝：%v", err)
	}
	if len(store.records) != 0 {
		t.Fatal("缺少指纹仍写库")
	}
}

// TestRunEmbeddingSameIDHotUpdate 同 ID 条目内容热更新后重算且覆盖旧记录。
func TestRunEmbeddingSameIDHotUpdate(t *testing.T) {
	dir := knowledgeDir(t)
	oldEntry := testEntry("hot", "标题旧")
	oldEntry.Usage = "接法旧"
	writeEmbeddingEntries(t, dir, "memes.md", oldEntry)
	reader := embeddingReader(t, dir)
	embedder := &fakeEmbedder{}
	store := newFakeEmbeddingStore()
	run := func() EmbedStats {
		t.Helper()
		stats, err := RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{
			BatchSize: 2, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		return stats
	}
	if stats := run(); stats.Embedded != 1 {
		t.Fatalf("首次入库不符：%+v", stats)
	}
	firstHash := store.records[0].Hash

	newEntry := testEntry("hot", "标题新")
	newEntry.Usage = "接法新"
	writeEmbeddingEntries(t, dir, "memes.md", newEntry)
	if err := reader.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := run()
	if stats.Pending != 1 || stats.Embedded != 1 || stats.Skipped != 0 {
		t.Fatalf("同ID热更新未重算：%+v", stats)
	}
	if len(store.records) != 2 || store.records[1].ID != "hot" || store.records[1].Hash == firstHash {
		t.Fatalf("热更新记录不符：%+v", store.records)
	}
	if store.existing["hot"].Fingerprint != testKnowledgeFingerprint {
		t.Fatal("写入未带指纹")
	}
}

// TestRunEmbeddingDetectsContentChangeWithSameModTime 保留 mtime 改内容也必须重算：
// 增量判断只认内容 hash，不信任文件时间。
func TestRunEmbeddingDetectsContentChangeWithSameModTime(t *testing.T) {
	dir := knowledgeDir(t)
	path := filepath.Join(dir, "memes.md")
	oldEntry := testEntry("mtime", "标题不变")
	oldEntry.Usage = "接法旧"
	writeEmbeddingEntries(t, dir, "memes.md", oldEntry)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reader := embeddingReader(t, dir)
	embedder := &fakeEmbedder{}
	store := newFakeEmbeddingStore()
	opts := EmbedOptions{BatchSize: 1, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond}
	if stats, err := RunEmbedding(context.Background(), reader, embedder, store, opts); err != nil || stats.Embedded != 1 {
		t.Fatalf("首次入库不符：%+v %v", stats, err)
	}

	newEntry := testEntry("mtime", "标题不变")
	newEntry.Usage = "接法新" // 只改正文，标题与来源不变
	writeEmbeddingEntries(t, dir, "memes.md", newEntry)
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := reader.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current, err := os.Stat(path); err != nil || !current.ModTime().Equal(info.ModTime()) {
		t.Fatalf("mtime 未保持：%v %v", current, err)
	}
	stats, err := RunEmbedding(context.Background(), reader, embedder, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.Embedded != 1 || stats.Skipped != 0 || embedder.calls != 2 {
		t.Fatalf("保留mtime改内容未重算：stats=%+v calls=%d", stats, embedder.calls)
	}
}

// TestRunEmbeddingFingerprintChangeReembeds 同维度换模型后不得跳过旧空间条目。
func TestRunEmbeddingFingerprintChangeReembeds(t *testing.T) {
	dir := knowledgeDir(t)
	writeEmbeddingEntries(t, dir, "memes.md", testEntry("one", "标题一"))
	reader := embeddingReader(t, dir)
	embedder := &fakeEmbedder{}
	store := newFakeEmbeddingStore()
	if stats, err := RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{
		BatchSize: 1, Fingerprint: "model-a", RetryDelay: time.Millisecond,
	}); err != nil || stats.Embedded != 1 {
		t.Fatalf("首个模型入库失败：%+v %v", stats, err)
	}
	stats, err := RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{
		BatchSize: 1, Fingerprint: "model-b", RetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.Skipped != 0 || stats.Embedded != 1 || embedder.calls != 2 {
		t.Fatalf("换模型后仍复用旧向量空间：stats=%+v calls=%d", stats, embedder.calls)
	}
	if got := store.existing["one"].Fingerprint; got != "model-b" {
		t.Fatalf("写入指纹未更新：%q", got)
	}
}

// TestRunEmbeddingCountsCASRejected 源已变化被拒绝写回的条数计入 Rejected。
func TestRunEmbeddingCountsCASRejected(t *testing.T) {
	dir := knowledgeDir(t)
	writeEmbeddingEntries(t, dir, "memes.md", testEntry("one", "标题一"), testEntry("two", "标题二"))
	reader := embeddingReader(t, dir)
	store := newFakeEmbeddingStore()
	store.rejectAll = true
	stats, err := RunEmbedding(context.Background(), reader, &fakeEmbedder{}, store, EmbedOptions{
		BatchSize: 2, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Embedded != 0 || stats.Rejected != 2 {
		t.Fatalf("CAS 拒绝未计数：%+v", stats)
	}
}

// TestRunEmbeddingRejectsSourceChangedDuringComputation 计算期间快照被重载时，
// 基于旧源的向量不得写回，计入 Rejected 等下一轮重算。
func TestRunEmbeddingRejectsSourceChangedDuringComputation(t *testing.T) {
	dir := knowledgeDir(t)
	oldEntry := testEntry("race", "标题旧")
	writeEmbeddingEntries(t, dir, "memes.md", oldEntry)
	reader := embeddingReader(t, dir)
	store := newFakeEmbeddingStore()
	newEntry := testEntry("race", "标题新")
	embedder := embedderFunc(func(ctx context.Context, inputs []string) ([][]float64, error) {
		writeEmbeddingEntries(t, dir, "memes.md", newEntry)
		if err := reader.Reload(ctx); err != nil {
			return nil, err
		}
		vectors := make([][]float64, len(inputs))
		for i := range inputs {
			vectors[i] = unitEmbedding(i)
		}
		return vectors, nil
	})
	stats, err := RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{
		BatchSize: 1, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Embedded != 0 || stats.Rejected != 1 || len(store.records) != 0 {
		t.Fatalf("计算期间源变化未拒绝：stats=%+v records=%d", stats, len(store.records))
	}
}

// TestRunEmbeddingRetriesOnce 批次失败重试 1 次；连续失败返回错误且不写入。
func TestRunEmbeddingRetriesOnce(t *testing.T) {
	dir := knowledgeDir(t)
	writeEmbeddingEntries(t, dir, "memes.md", testEntry("one", "标题一"))
	reader := embeddingReader(t, dir)

	embedder := &fakeEmbedder{failFirst: 1}
	store := newFakeEmbeddingStore()
	stats, err := RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{BatchSize: 1, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond})
	if err != nil || stats.Embedded != 1 || embedder.calls != 2 || len(store.records) != 1 {
		t.Fatalf("重试一次后应成功：stats=%+v calls=%d err=%v", stats, embedder.calls, err)
	}

	embedder = &fakeEmbedder{failFirst: 2}
	store = newFakeEmbeddingStore()
	stats, err = RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{BatchSize: 1, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond})
	if err == nil || stats.Embedded != 0 || embedder.calls != 2 || len(store.records) != 0 {
		t.Fatalf("连续失败应终止且不写入：stats=%+v calls=%d err=%v", stats, embedder.calls, err)
	}
}

// TestRunEmbeddingRejectsBadVector 客户端返回错误维度时拒绝入库。
func TestRunEmbeddingRejectsBadVector(t *testing.T) {
	dir := knowledgeDir(t)
	writeEmbeddingEntries(t, dir, "memes.md", testEntry("one", "标题一"))
	reader := embeddingReader(t, dir)
	embedder := &fakeEmbedder{factory: func(string) []float64 { return []float64{1, 2} }}
	store := newFakeEmbeddingStore()
	if _, err := RunEmbedding(context.Background(), reader, embedder, store, EmbedOptions{BatchSize: 1, Fingerprint: testKnowledgeFingerprint, RetryDelay: time.Millisecond}); err == nil || !strings.Contains(err.Error(), "维度") {
		t.Fatalf("错误向量未被拒绝：%v", err)
	}
	if len(store.records) != 0 {
		t.Fatal("错误向量被写入")
	}
}

// TestEmbeddingDocumentsExposeFileSourceAndModTime 文档带文件来源、内容 hash 与 mtime。
func TestEmbeddingDocumentsExposeFileSourceAndModTime(t *testing.T) {
	dir := knowledgeDir(t)
	path := filepath.Join(dir, "memes.md")
	writeEmbeddingEntries(t, dir, "memes.md", testEntry("one", "标题一"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reader := embeddingReader(t, dir)
	documents, err := reader.EmbeddingDocuments(context.Background())
	if err != nil || len(documents) != 1 {
		t.Fatalf("读取向量文档失败：%v %d", err, len(documents))
	}
	doc := documents[0]
	// PostgreSQL timestamptz 微秒精度：文档时间按同样精度归一化。
	if doc.ID != "one" || doc.Source != "memes.md" || doc.Title != "标题一" || !doc.UpdatedAt.Equal(info.ModTime().UTC().Truncate(time.Microsecond)) {
		t.Fatalf("文档元数据错误：%+v", doc)
	}
	if doc.TextHash != hashText(doc.Text) || len(doc.SourceHash) != 64 {
		t.Fatalf("内容hash缺失或不符：%+v", doc)
	}
}
