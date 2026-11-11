package viewer

// 本文件测观众档案的增量向量化（M10 三路版）：分路跳过、空路跳过、批量与重试，
// 以及 C2/N08 的每路索引状态（文本 hash / 源 hash / 向量空间指纹）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"RaiseMyFish/internal/llmmeta"
)

const testEmbedFingerprint = "test-fingerprint"

type fakeEmbedStore struct {
	inputs   []EmbeddingInput
	indexes  map[string]map[EmbeddingKind]EmbeddingIndex
	upserts  []EmbeddingRecord
	err      error
	upsErrs  error
	written  *int
	gotCalls int
}

func (f *fakeEmbedStore) ViewerEmbeddingInputs(context.Context) ([]EmbeddingInput, error) {
	f.gotCalls++
	return f.inputs, f.err
}

func (f *fakeEmbedStore) ExistingProfileEmbeddingIndexes(context.Context) (map[string]map[EmbeddingKind]EmbeddingIndex, error) {
	return f.indexes, nil
}

func (f *fakeEmbedStore) UpsertProfileEmbeddings(_ context.Context, records []EmbeddingRecord) (int, error) {
	if f.upsErrs != nil {
		return 0, f.upsErrs
	}
	f.upserts = append(f.upserts, records...)
	if f.written != nil {
		return *f.written, nil
	}
	return len(records), nil
}

type fakeEmbedWorker struct {
	err       error
	failTimes int
	calls     int
	gotInputs [][]string
	call      llmmeta.Call
}

func (f *fakeEmbedWorker) Embed(ctx context.Context, inputs []string) ([][]float64, error) {
	f.calls++
	f.call = llmmeta.From(ctx)
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

// viewerEmbedInput 构造带三路源 hash 的输入；hash 只与「这一路自己的源」有关。
func viewerEmbedInput(uid, name string, texts, replies []string) EmbeddingInput {
	hashes := map[EmbeddingKind]string{
		KindIdentity: "src-identity-" + uid,
	}
	if len(texts) > 0 {
		hashes[KindFacts] = "src-facts-" + uid
	}
	if len(replies) > 0 {
		hashes[KindInteraction] = "src-interaction-" + uid
	}
	return EmbeddingInput{UID: uid, Name: name, Texts: texts, Replies: replies, SourceHashes: hashes}
}

// embedIndex 构造形如「文本与源都未变」的索引状态。
func embedIndex(input EmbeddingInput, kind EmbeddingKind, text, fingerprint string) EmbeddingIndex {
	return EmbeddingIndex{
		TextHash:    hashText(text),
		SourceHash:  input.SourceHashes[kind],
		Fingerprint: fingerprint,
	}
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
	unchanged := viewerEmbedInput("u-a", "白饭", []string{"今天也要白饭"}, []string{"好"})
	changed := viewerEmbedInput("u-b", "熟客", []string{"又来了"}, nil)
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{unchanged, changed},
		indexes: map[string]map[EmbeddingKind]EmbeddingIndex{
			"u-a": {
				KindIdentity:    embedIndex(unchanged, KindIdentity, "白饭", testEmbedFingerprint),
				KindFacts:       embedIndex(unchanged, KindFacts, "今天也要白饭", testEmbedFingerprint),
				KindInteraction: embedIndex(unchanged, KindInteraction, "好", testEmbedFingerprint),
			},
			// u-b 的 identity 没变，facts 缺失 → 只该重算 facts；interaction 无内容。
			"u-b": {KindIdentity: embedIndex(changed, KindIdentity, "熟客", testEmbedFingerprint)},
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{
		BatchSize: 8, Fingerprint: testEmbedFingerprint,
	})
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
	if embedder.call.Purpose != llmmeta.PurposeEmbedIndex || embedder.call.CorrelationID != "viewer" {
		t.Fatalf("索引向量化用途未标注：%+v", embedder.call)
	}
	record := store.upserts[0]
	if record.UID != "u-b" || record.Kind != KindFacts {
		t.Fatalf("写回的路不对：%+v", record)
	}
	if record.Hash != hashText("又来了") {
		t.Errorf("facts hash = %q", record.Hash)
	}
	if record.SourceHash != changed.SourceHashes[KindFacts] {
		t.Errorf("facts 源 hash 不符：%q", record.SourceHash)
	}
	if record.Fingerprint != testEmbedFingerprint {
		t.Errorf("指纹不符：%q", record.Fingerprint)
	}
	if len(record.Embedding) != EmbeddingDimension {
		t.Errorf("向量维度 = %d", len(record.Embedding))
	}
}

// TestRunEmbeddingPerKindSourceHashIsolation 某一路源变化只重算该路。
func TestRunEmbeddingPerKindSourceHashIsolation(t *testing.T) {
	input := viewerEmbedInput("u-a", "白饭", []string{"今天也要白饭"}, []string{"好"})
	indexes := map[string]map[EmbeddingKind]EmbeddingIndex{
		"u-a": {
			KindIdentity:    embedIndex(input, KindIdentity, "白饭", testEmbedFingerprint),
			KindFacts:       embedIndex(input, KindFacts, "今天也要白饭", testEmbedFingerprint),
			KindInteraction: embedIndex(input, KindInteraction, "好", testEmbedFingerprint),
		},
	}
	// 只有 facts 的源 hash 变了（重建清空了该路向量后的新版本）。
	facts := indexes["u-a"][KindFacts]
	facts.SourceHash = "older-facts-source"
	indexes["u-a"][KindFacts] = facts

	store := &fakeEmbedStore{inputs: []EmbeddingInput{input}, indexes: indexes}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	if stats.Pending != 1 || stats.Embedded != 1 {
		t.Fatalf("只有 facts 应重算：%+v", stats)
	}
	if len(store.upserts) != 1 || store.upserts[0].Kind != KindFacts {
		t.Fatalf("重算的路不对：%+v", store.upserts)
	}
}

// TestRunEmbeddingFingerprintSwitchReembeds 换模型（同维度）后三路都要重算。
func TestRunEmbeddingFingerprintSwitchReembeds(t *testing.T) {
	input := viewerEmbedInput("u-a", "白饭", []string{"今天也要白饭"}, []string{"好"})
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{input},
		indexes: map[string]map[EmbeddingKind]EmbeddingIndex{
			"u-a": {
				KindIdentity:    embedIndex(input, KindIdentity, "白饭", "old-provider"),
				KindFacts:       embedIndex(input, KindFacts, "今天也要白饭", "old-provider"),
				KindInteraction: embedIndex(input, KindInteraction, "好", "old-provider"),
			},
		},
	}
	stats, err := RunEmbedding(context.Background(), store, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	if stats.Pending != 3 || stats.Embedded != 3 {
		t.Fatalf("指纹变化应三路重算：%+v", stats)
	}
	for _, record := range store.upserts {
		if record.Fingerprint != testEmbedFingerprint {
			t.Errorf("%s 指纹不符：%q", record.Kind, record.Fingerprint)
		}
	}
}

// TestRunEmbeddingSkipsEmptyKind 从没被回复过的人没有 interaction 向量，不算待处理。
func TestRunEmbeddingSkipsEmptyKind(t *testing.T) {
	input := viewerEmbedInput("u-a", "新人", []string{"你好"}, nil)
	store := &fakeEmbedStore{inputs: []EmbeddingInput{input}}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{Fingerprint: testEmbedFingerprint})
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
	input := viewerEmbedInput("u-a", "白饭", nil, nil)
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{input},
		indexes: map[string]map[EmbeddingKind]EmbeddingIndex{
			"u-a": {KindIdentity: embedIndex(input, KindIdentity, "白饭", testEmbedFingerprint)},
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	if stats.Pending != 0 || stats.Batches != 0 || embedder.calls != 0 || len(store.upserts) != 0 {
		t.Errorf("无待处理时不应调用/写入：%+v calls=%d upserts=%d", stats, embedder.calls, len(store.upserts))
	}
}

// TestRunEmbeddingRequiresSourceHash 缺少某路源 hash 直接拒绝，不写任何向量。
func TestRunEmbeddingRequiresSourceHash(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{{UID: "u-a", Name: "鱼"}}}
	_, err := RunEmbedding(context.Background(), store, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err == nil || !strings.Contains(err.Error(), "源hash") {
		t.Fatalf("缺源 hash 应报错，实际 %v", err)
	}
	if len(store.upserts) != 0 {
		t.Errorf("失败时不应写回：%+v", store.upserts)
	}
}

// TestRunEmbeddingRequiresFingerprint 缺指纹直接拒绝。
func TestRunEmbeddingRequiresFingerprint(t *testing.T) {
	_, err := RunEmbedding(context.Background(), &fakeEmbedStore{}, &fakeEmbedWorker{}, EmbedOptions{})
	if err == nil || !strings.Contains(err.Error(), "指纹") {
		t.Fatalf("缺指纹应报错，实际 %v", err)
	}
}

// TestRunEmbeddingCountsRejectedWrites CAS 拒绝写回计入 Rejected，不当作失败。
func TestRunEmbeddingCountsRejectedWrites(t *testing.T) {
	inputs := []EmbeddingInput{
		viewerEmbedInput("u-a", "白饭", []string{"今天也要白饭"}, nil),
		viewerEmbedInput("u-b", "熟客", []string{"又来了"}, nil),
	}
	zero := 0
	store := &fakeEmbedStore{inputs: inputs, written: &zero}
	stats, err := RunEmbedding(context.Background(), store, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("CAS 拒绝不应视为错误：%v", err)
	}
	// 2 份档案 × identity/facts 两路 = 4 条待写；全部被 CAS 拒绝。
	if stats.Embedded != 0 || stats.Rejected != 4 {
		t.Fatalf("拒绝统计不符：%+v", stats)
	}

	one := 1
	store = &fakeEmbedStore{inputs: inputs, written: &one}
	stats, err = RunEmbedding(context.Background(), store, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("部分拒绝不应视为错误：%v", err)
	}
	if stats.Embedded != 1 || stats.Rejected != 3 {
		t.Fatalf("部分写入统计不符：%+v", stats)
	}
}

// TestRunEmbeddingRetriesOnce 首次失败重试一次后成功。
func TestRunEmbeddingRetriesOnce(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{viewerEmbedInput("u-a", "鱼", nil, nil)}}
	embedder := &fakeEmbedWorker{err: errors.New("网络抖动"), failTimes: 1}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{RetryDelay: 1, Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if embedder.calls != 2 || stats.Embedded != 1 {
		t.Errorf("重试行为不符：calls=%d stats=%+v", embedder.calls, stats)
	}
}

// TestRunEmbeddingBatchAndProgress 4 条文档按 2 条一批，进度两次。
func TestRunEmbeddingBatchAndProgress(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{
		viewerEmbedInput("u-a", "一", []string{"说一"}, nil),
		viewerEmbedInput("u-b", "二", nil, []string{"回二"}),
	}}
	embedder := &fakeEmbedWorker{}
	progress := 0
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{
		BatchSize:   2,
		Fingerprint: testEmbedFingerprint,
		Progress:    func(_, _, _ int) { progress++ },
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
	store := &fakeEmbedStore{inputs: []EmbeddingInput{viewerEmbedInput("u-a", "鱼", nil, nil)}}
	short := &shortEmbedder{}
	_, err := RunEmbedding(context.Background(), store, short, EmbedOptions{Fingerprint: testEmbedFingerprint})
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
	if _, err := RunEmbedding(context.Background(), nil, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint}); err == nil {
		t.Error("store 为 nil 应报错")
	}
	if _, err := RunEmbedding(context.Background(), &fakeEmbedStore{}, nil, EmbedOptions{Fingerprint: testEmbedFingerprint}); err == nil {
		t.Error("embedder 为 nil 应报错")
	}
	if _, err := RunEmbedding(context.Background(), &fakeEmbedStore{}, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint, BatchSize: 4096}); err == nil {
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
