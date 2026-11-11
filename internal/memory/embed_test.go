package memory

// 本文件测长期记忆的增量向量化：跳过未变更、空摘要、批量与重试，
// 以及 C2/N08 的三层索引状态（文本 hash / 源 hash / 向量空间指纹）。

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/llmmeta"
)

const testEmbedFingerprint = "test-fingerprint"

type fakeEmbedStore struct {
	inputs  []EmbeddingInput
	indexes map[string]EmbeddingIndex
	upserts []EmbeddingRecord
	err     error
	upsErrs error
	// written 是 UpsertMemoryEmbeddings 的返回条数；nil 表示全部写入成功。
	written *int
	// allowPrivate 记录仓储收到的授权参数，用于断言入口透传。
	allowPrivate bool
}

func (f *fakeEmbedStore) MemoryEmbeddingInputs(_ context.Context, allowPrivate bool) ([]EmbeddingInput, error) {
	f.allowPrivate = allowPrivate
	return f.inputs, f.err
}

func (f *fakeEmbedStore) ExistingMemoryEmbeddingIndexes(context.Context) (map[string]EmbeddingIndex, error) {
	return f.indexes, nil
}

func (f *fakeEmbedStore) UpsertMemoryEmbeddings(_ context.Context, records []EmbeddingRecord) (int, error) {
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

func testEmbedInput(id, title, summary string) EmbeddingInput {
	return EmbeddingInput{ID: id, Title: title, Summary: summary, SourceHash: "src-" + id}
}

// TestRunEmbeddingIncrementalSkipsUnchanged 未变更与空摘要跳过，只算新增/变化。
func TestRunEmbeddingIncrementalSkipsUnchanged(t *testing.T) {
	unchanged := testEmbedInput("mem-a", "猫", "猫今天很乖")
	changed := testEmbedInput("mem-b", "天气", "今天晴天")
	empty := testEmbedInput("mem-c", "", "  ")
	fresh := testEmbedInput("mem-d", "", "新记录")
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{unchanged, changed, empty, fresh},
		indexes: map[string]EmbeddingIndex{
			"mem-a": {
				TextHash:    hashText(embeddingText(unchanged)),
				SourceHash:  unchanged.SourceHash,
				Fingerprint: testEmbedFingerprint,
			},
			"mem-b": {
				TextHash:    "stale-hash",
				SourceHash:  changed.SourceHash,
				Fingerprint: testEmbedFingerprint,
			},
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{
		BatchSize: 8, Fingerprint: testEmbedFingerprint,
	})
	if err != nil {
		t.Fatalf("增量入库失败：%v", err)
	}
	if stats.Total != 4 || stats.Skipped != 2 || stats.Pending != 2 || stats.Batches != 1 || stats.Embedded != 2 {
		t.Fatalf("统计不符：%+v", stats)
	}
	if embedder.calls != 1 {
		t.Errorf("embedding 调用次数 = %d，期望 1", embedder.calls)
	}
	if embedder.call.Purpose != llmmeta.PurposeEmbedIndex || embedder.call.CorrelationID != "memory" {
		t.Fatalf("索引向量化用途未标注：%+v", embedder.call)
	}
	if len(store.upserts) != 2 {
		t.Fatalf("写回条数 = %d，期望 2", len(store.upserts))
	}
	got := map[string]string{}
	for _, record := range store.upserts {
		got[record.ID] = record.Hash
		if len(record.Embedding) != EmbeddingDimension {
			t.Errorf("%s 向量维度 = %d", record.ID, len(record.Embedding))
		}
		if record.Fingerprint != testEmbedFingerprint {
			t.Errorf("%s 指纹不符：%q", record.ID, record.Fingerprint)
		}
	}
	if got["mem-b"] != hashText("天气\n今天晴天") {
		t.Errorf("变化记录 hash 不符：%q", got["mem-b"])
	}
	if got["mem-d"] != hashText("新记录") {
		t.Errorf("新增记录 hash 不符：%q", got["mem-d"])
	}
	if _, ok := got["mem-a"]; ok {
		t.Error("未变更记录不应重新写入")
	}
}

// TestRunEmbeddingSelectionMatrix 私密/归档/失效/未生效记录不得进入外部请求。
//
// 用捕获输入的假 Embedder 验证选择矩阵：默认只发公开有效行；
// 显式授权后加入私密行；归档、valid_to 已失效、valid_from 未生效始终排除。
func TestRunEmbeddingSelectionMatrix(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	expiredAt := now.Add(-time.Minute)
	inputs := []EmbeddingInput{
		{ID: "public", Summary: "公开有效", SourceHash: "src-public"},
		{ID: "private", Summary: "私密有效", Private: true, SourceHash: "src-private"},
		{ID: "archived", Summary: "已归档", Archived: true, SourceHash: "src-archived"},
		{ID: "expired", Summary: "已失效", ValidTo: &expiredAt, SourceHash: "src-expired"},
		{ID: "future", Summary: "尚未生效", ValidFrom: now.Add(time.Hour), SourceHash: "src-future"},
		{ID: "fresh", Summary: "刚刚生效", ValidFrom: now.Add(-time.Minute), SourceHash: "src-fresh"},
	}
	clock := func() time.Time { return now }

	// 默认（未授权）：只有公开且有效的行外发。
	store := &fakeEmbedStore{inputs: inputs}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{Now: clock, Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("默认授权下入库失败：%v", err)
	}
	if store.allowPrivate {
		t.Error("默认不应向仓储申请私密行")
	}
	if stats.Total != 6 || stats.Skipped != 4 || stats.Pending != 2 || stats.Embedded != 2 {
		t.Fatalf("默认授权统计不符：%+v", stats)
	}
	assertEmbedInputs(t, embedder.gotInputs, []string{"公开有效", "刚刚生效"})

	// 显式授权：私密行加入，但归档/失效/未生效仍然排除。
	store = &fakeEmbedStore{inputs: inputs}
	embedder = &fakeEmbedWorker{}
	stats, err = RunEmbedding(context.Background(), store, embedder, EmbedOptions{AllowPrivate: true, Now: clock, Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("授权后入库失败：%v", err)
	}
	if !store.allowPrivate {
		t.Error("授权后应向仓储申请私密行")
	}
	if stats.Pending != 3 || stats.Embedded != 3 {
		t.Fatalf("授权后统计不符：%+v", stats)
	}
	assertEmbedInputs(t, embedder.gotInputs, []string{"公开有效", "私密有效", "刚刚生效"})
}

// assertEmbedInputs 断言假 Embedder 实际收到的文本恰好等于期望序列（按批展平）。
func assertEmbedInputs(t *testing.T, batches [][]string, want []string) {
	t.Helper()
	var got []string
	for _, batch := range batches {
		got = append(got, batch...)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("外发输入 = %v，期望 %v", got, want)
	}
}

// TestRunEmbeddingNoPending 全部未变更时不调用 embedding。
func TestRunEmbeddingNoPending(t *testing.T) {
	input := testEmbedInput("mem-a", "猫", "乖")
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{input},
		indexes: map[string]EmbeddingIndex{
			"mem-a": {
				TextHash:    hashText(embeddingText(input)),
				SourceHash:  input.SourceHash,
				Fingerprint: testEmbedFingerprint,
			},
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	if stats.Pending != 0 || stats.Batches != 0 || embedder.calls != 0 {
		t.Errorf("无待处理时不应调用：%+v calls=%d", stats, embedder.calls)
	}
	if len(store.upserts) != 0 {
		t.Errorf("无待处理时不应写回：%+v", store.upserts)
	}
}

// TestRunEmbeddingSourceHashChangeReembeds 文本没变但源 hash 变了也要重算。
//
// 典型场景：摘要被清洗成相同文本前的中间状态、或源字段在读取后又被改写；
// 只看文本 hash 会漏掉这类版本变化。
func TestRunEmbeddingSourceHashChangeReembeds(t *testing.T) {
	input := testEmbedInput("mem-a", "", "同一段正文")
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{input},
		indexes: map[string]EmbeddingIndex{
			"mem-a": {
				TextHash:    hashText(embeddingText(input)),
				SourceHash:  "older-source",
				Fingerprint: testEmbedFingerprint,
			},
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	if stats.Pending != 1 || stats.Embedded != 1 {
		t.Fatalf("源 hash 变化应重算：%+v", stats)
	}
}

// TestRunEmbeddingFingerprintSwitchReembeds 同维度换模型时旧向量必须重算。
func TestRunEmbeddingFingerprintSwitchReembeds(t *testing.T) {
	input := testEmbedInput("mem-a", "猫", "乖")
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{input},
		indexes: map[string]EmbeddingIndex{
			"mem-a": {
				TextHash:    hashText(embeddingText(input)),
				SourceHash:  input.SourceHash,
				Fingerprint: "old-provider-fingerprint",
			},
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("入库失败：%v", err)
	}
	if stats.Pending != 1 || stats.Embedded != 1 || len(store.upserts) != 1 {
		t.Fatalf("指纹变化应重算：%+v", stats)
	}
	if store.upserts[0].Fingerprint != testEmbedFingerprint {
		t.Errorf("写回指纹不符：%q", store.upserts[0].Fingerprint)
	}
}

// TestRunEmbeddingRequiresSourceHash 缺少源 hash 的输入直接拒绝，不写任何向量。
func TestRunEmbeddingRequiresSourceHash(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{{ID: "mem-a", Summary: "正文"}}}
	_, err := RunEmbedding(context.Background(), store, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err == nil || !strings.Contains(err.Error(), "源hash") {
		t.Fatalf("缺源 hash 应报错，实际 %v", err)
	}
	if len(store.upserts) != 0 {
		t.Errorf("失败时不应写回：%+v", store.upserts)
	}
}

// TestRunEmbeddingRequiresFingerprint 缺指纹直接拒绝：不知道向量空间就不能算。
func TestRunEmbeddingRequiresFingerprint(t *testing.T) {
	_, err := RunEmbedding(context.Background(), &fakeEmbedStore{}, &fakeEmbedWorker{}, EmbedOptions{})
	if err == nil || !strings.Contains(err.Error(), "指纹") {
		t.Fatalf("缺指纹应报错，实际 %v", err)
	}
}

// TestRunEmbeddingCountsRejectedWrites CAS 拒绝写回计入 Rejected，不当作失败。
func TestRunEmbeddingCountsRejectedWrites(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{
		testEmbedInput("a", "", "一"), testEmbedInput("b", "", "二"), testEmbedInput("c", "", "三"),
	}}
	zero := 0
	store.written = &zero
	stats, err := RunEmbedding(context.Background(), store, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("CAS 拒绝不应视为错误：%v", err)
	}
	if stats.Embedded != 0 || stats.Rejected != 3 {
		t.Fatalf("拒绝统计不符：%+v", stats)
	}

	one := 1
	store = &fakeEmbedStore{inputs: []EmbeddingInput{
		testEmbedInput("a", "", "一"), testEmbedInput("b", "", "二"),
	}, written: &one}
	stats, err = RunEmbedding(context.Background(), store, &fakeEmbedWorker{}, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("部分拒绝不应视为错误：%v", err)
	}
	if stats.Embedded != 1 || stats.Rejected != 1 {
		t.Fatalf("部分写入统计不符：%+v", stats)
	}
}

// TestRunEmbeddingRetriesOnce 首次失败重试一次后成功。
func TestRunEmbeddingRetriesOnce(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{testEmbedInput("mem-a", "", "鱼")}}
	embedder := &fakeEmbedWorker{err: errors.New("网络抖动"), failTimes: 1}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{RetryDelay: 1, Fingerprint: testEmbedFingerprint})
	if err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if embedder.calls != 2 || stats.Embedded != 1 {
		t.Errorf("重试行为不符：calls=%d stats=%+v", embedder.calls, stats)
	}
}

// TestRunEmbeddingBatchAndProgress 3 条按 2 条一批，进度两次。
func TestRunEmbeddingBatchAndProgress(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{
		testEmbedInput("a", "", "一"), testEmbedInput("b", "", "二"), testEmbedInput("c", "", "三"),
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
	if stats.Batches != 2 || embedder.calls != 2 || progress != 2 || stats.Embedded != 3 {
		t.Errorf("批量行为不符：stats=%+v calls=%d progress=%d", stats, embedder.calls, progress)
	}
}

// TestRunEmbeddingRejectsBadVector 维度不符立即报错，不写入脏向量。
func TestRunEmbeddingRejectsBadVector(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{testEmbedInput("a", "", "鱼")}}
	embedder := &fakeEmbedWorker{err: nil}
	// 用只返回短向量的实现覆盖
	short := &shortEmbedder{fakeEmbedWorker: embedder}
	_, err := RunEmbedding(context.Background(), store, short, EmbedOptions{Fingerprint: testEmbedFingerprint})
	if err == nil || !strings.Contains(err.Error(), "维度") {
		t.Fatalf("维度不符应报错，实际 %v", err)
	}
	if len(store.upserts) != 0 {
		t.Errorf("失败时不应写回：%+v", store.upserts)
	}
}

type shortEmbedder struct{ *fakeEmbedWorker }

func (s *shortEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	s.calls++
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

// TestEmbeddingTextJoin 标题与摘要按换行拼接，空段不产生多余换行。
func TestEmbeddingTextJoin(t *testing.T) {
	cases := []struct {
		input EmbeddingInput
		want  string
	}{
		{EmbeddingInput{Title: " 猫 ", Summary: " 乖 "}, "猫\n乖"},
		{EmbeddingInput{Title: "", Summary: "只有摘要"}, "只有摘要"},
		{EmbeddingInput{Title: "只有标题"}, "只有标题"},
		{EmbeddingInput{}, ""},
	}
	for _, tc := range cases {
		if got := embeddingText(tc.input); got != tc.want {
			t.Errorf("embeddingText(%+v) = %q，期望 %q", tc.input, got, tc.want)
		}
	}
}
