package viewer

// 本文件测观众档案的增量向量化（M10 三路版）：分路跳过、空路跳过、批量与重试。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeEmbedStore struct {
	inputs  []EmbeddingInput
	hashes  map[string]map[EmbeddingKind]string
	upserts []EmbeddingRecord
	err     error
}

func (f *fakeEmbedStore) ViewerEmbeddingInputs(context.Context) ([]EmbeddingInput, error) {
	return f.inputs, f.err
}

func (f *fakeEmbedStore) ExistingProfileEmbeddingHashes(context.Context) (map[string]map[EmbeddingKind]string, error) {
	return f.hashes, nil
}

func (f *fakeEmbedStore) UpsertProfileEmbeddings(_ context.Context, records []EmbeddingRecord) error {
	f.upserts = append(f.upserts, records...)
	return nil
}

type fakeEmbedWorker struct {
	err       error
	failTimes int
	calls     int
	gotInputs [][]string
}

func (f *fakeEmbedWorker) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	f.calls++
	f.gotInputs = append(f.gotInputs, inputs)
	if f.failTimes > 0 {
		f.failTimes--
		return nil, f.err
	}
	vectors := make([][]float64, len(inputs))
	for i := range vectors {
		vectors[i] = vectorWith(i)
	}
	return vectors, nil
}

// TestDocumentsAreIsolatedByKind M10 核心：三路各自只放自己的内容。
//
// 回归背景：旧实现把「昵称 + 本人发言 + 鱼的回复」拼成一个向量，
// 鱼说的「白饭」被算成观众特点，认人和查证都被污染。
func TestDocumentsAreIsolatedByKind(t *testing.T) {
	docs := documentsFor(EmbeddingInput{
		UID:     "u-1",
		Name:    "白饭哥",
		Texts:   []string{"我爱白饭", "今天也要白饭"},
		Replies: []string{"鱼说白饭真香"},
	})
	if len(docs) != 3 {
		t.Fatalf("应产出三条文档：%+v", docs)
	}
	byKind := make(map[EmbeddingKind]string, len(docs))
	for _, doc := range docs {
		byKind[doc.kind] = doc.text
	}
	if byKind[KindIdentity] != "白饭哥" {
		t.Errorf("identity 只该有昵称：%q", byKind[KindIdentity])
	}
	if byKind[KindFacts] != "我爱白饭\n今天也要白饭" {
		t.Errorf("facts 只该有本人发言：%q", byKind[KindFacts])
	}
	if byKind[KindInteraction] != "鱼说白饭真香" {
		t.Errorf("interaction 只该有鱼的回复：%q", byKind[KindInteraction])
	}
	// 三路互不包含：鱼的话不能进 facts，观众的话也不能进 interaction。
	if strings.Contains(byKind[KindFacts], "鱼说") {
		t.Error("facts 混入了鱼的话")
	}
	if strings.Contains(byKind[KindInteraction], "我爱白饭") {
		t.Error("interaction 混入了观众的话")
	}
}

// TestRunEmbeddingSkipsPerKind 分路增量：只有内容变化的那一路重算。
func TestRunEmbeddingSkipsPerKind(t *testing.T) {
	unchanged := EmbeddingInput{UID: "u-a", Name: "白饭", Texts: []string{"今天也要白饭"}, Replies: []string{"好"}}
	changed := EmbeddingInput{UID: "u-b", Name: "熟客", Texts: []string{"又来了"}}
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{unchanged, changed},
		hashes: map[string]map[EmbeddingKind]string{
			"u-a": {
				KindIdentity:    hashText("白饭"),
				KindFacts:       hashText("今天也要白饭"),
				KindInteraction: hashText("好"),
			},
			// u-b 的 identity 没变，facts 缺失 → 只该重算 facts；interaction 无内容。
			"u-b": {KindIdentity: hashText("熟客")},
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{BatchSize: 8})
	if err != nil {
		t.Fatalf("增量入库失败：%v", err)
	}
	// 2 份档案 × 3 路 = 6 份文档；跳过 u-a 三路 + u-b 的 identity 与空 interaction。
	if stats.Total != 6 || stats.Skipped != 5 || stats.Pending != 1 || stats.Embedded != 1 {
		t.Fatalf("统计不符：%+v", stats)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("只应写回 1 条：%+v", store.upserts)
	}
	record := store.upserts[0]
	if record.UID != "u-b" || record.Kind != KindFacts {
		t.Fatalf("写回的路不对：%+v", record)
	}
	if record.Hash != hashText("又来了") {
		t.Errorf("facts hash = %q", record.Hash)
	}
	if len(record.Embedding) != EmbeddingDimension {
		t.Errorf("向量维度 = %d", len(record.Embedding))
	}
}

// TestRunEmbeddingSkipsEmptyKind 从没被回复过的人没有 interaction 向量，不算待处理。
func TestRunEmbeddingSkipsEmptyKind(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{{UID: "u-a", Name: "新人", Texts: []string{"你好"}}}}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	if stats.Total != 3 || stats.Skipped != 1 || stats.Pending != 2 || stats.Embedded != 2 {
		t.Fatalf("空 interaction 应跳过：%+v", stats)
	}
	for _, record := range store.upserts {
		if record.Kind == KindInteraction {
			t.Error("空 interaction 不该写入")
		}
	}
}

// TestRunEmbeddingNoPending 全部未变更时不调用 embedding。
func TestRunEmbeddingNoPending(t *testing.T) {
	input := EmbeddingInput{UID: "u-a", Name: "白饭"}
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{input},
		hashes: map[string]map[EmbeddingKind]string{
			"u-a": {KindIdentity: hashText("白饭")},
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	if stats.Pending != 0 || stats.Batches != 0 || embedder.calls != 0 || len(store.upserts) != 0 {
		t.Errorf("无待处理时不应调用/写入：%+v calls=%d upserts=%d", stats, embedder.calls, len(store.upserts))
	}
}

// TestRunEmbeddingRetriesOnce 首次失败重试一次后成功。
func TestRunEmbeddingRetriesOnce(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{{UID: "u-a", Name: "鱼"}}}
	embedder := &fakeEmbedWorker{err: errors.New("网络抖动"), failTimes: 1}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{RetryDelay: 1})
	if err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if embedder.calls != 2 || stats.Embedded != 1 {
		t.Errorf("重试行为不符：calls=%d stats=%+v", embedder.calls, stats)
	}
}

// TestRunEmbeddingBatchAndProgress 5 条文档按 2 条一批，进度三次。
func TestRunEmbeddingBatchAndProgress(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{
		{UID: "u-a", Name: "一", Texts: []string{"说一"}},
		{UID: "u-b", Name: "二", Replies: []string{"回二"}},
	}}
	embedder := &fakeEmbedWorker{}
	progress := 0
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{
		BatchSize: 2,
		Progress:  func(_, _, _ int) { progress++ },
	})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	// u-a: identity+facts，u-b: identity+interaction → 4 份文档，2 批。
	if stats.Batches != 2 || embedder.calls != 2 || progress != 2 || stats.Embedded != 4 {
		t.Errorf("批量行为不符：stats=%+v calls=%d progress=%d", stats, embedder.calls, progress)
	}
}

// TestRunEmbeddingRejectsBadVector 维度不符立即报错，不写入脏向量。
func TestRunEmbeddingRejectsBadVector(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{{UID: "u-a", Name: "鱼"}}}
	short := &shortEmbedder{}
	_, err := RunEmbedding(context.Background(), store, short, EmbedOptions{})
	if err == nil || !strings.Contains(err.Error(), "维度") {
		t.Fatalf("维度不符应报错，实际 %v", err)
	}
	if len(store.upserts) != 0 {
		t.Errorf("失败时不应写回：%+v", store.upserts)
	}
}

type shortEmbedder struct{}

func (s *shortEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	vectors := make([][]float64, len(inputs))
	for i := range vectors {
		vectors[i] = []float64{1, 2, 3}
	}
	return vectors, nil
}

// TestRunEmbeddingDependencyAndOptionValidation 依赖与参数边界。
func TestRunEmbeddingDependencyAndOptionValidation(t *testing.T) {
	if _, err := RunEmbedding(context.Background(), nil, &fakeEmbedWorker{}, EmbedOptions{}); err == nil {
		t.Error("store 为 nil 应报错")
	}
	if _, err := RunEmbedding(context.Background(), &fakeEmbedStore{}, nil, EmbedOptions{}); err == nil {
		t.Error("embedder 为 nil 应报错")
	}
	if _, err := RunEmbedding(context.Background(), &fakeEmbedStore{}, &fakeEmbedWorker{}, EmbedOptions{BatchSize: 4096}); err == nil {
		t.Error("批量越界应报错")
	}
}

// TestKindTextHelpers 三路文本函数的拼接与空段处理。
func TestKindTextHelpers(t *testing.T) {
	input := EmbeddingInput{
		UID:     "u",
		Name:    " 白饭 ",
		Texts:   []string{" 今天 ", "", " 也要白饭 "},
		Replies: []string{" 好 "},
	}
	if got := identityText(input); got != "白饭" {
		t.Errorf("identityText = %q", got)
	}
	if got := factsText(input); got != "今天\n也要白饭" {
		t.Errorf("factsText = %q", got)
	}
	if got := interactionText(input); got != "好" {
		t.Errorf("interactionText = %q", got)
	}
	if got := identityText(EmbeddingInput{UID: "u"}); got != "" {
		t.Errorf("空昵称应返回空串：%q", got)
	}
}
