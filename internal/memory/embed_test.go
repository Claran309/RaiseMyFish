package memory

// 本文件测长期记忆的增量向量化：跳过未变更、空摘要、批量与重试。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeEmbedStore struct {
	inputs  []EmbeddingInput
	hashes  map[string]string
	upserts []EmbeddingRecord
	err     error
	upsErrs error
}

func (f *fakeEmbedStore) MemoryEmbeddingInputs(context.Context) ([]EmbeddingInput, error) {
	return f.inputs, f.err
}

func (f *fakeEmbedStore) ExistingMemoryEmbeddingHashes(context.Context) (map[string]string, error) {
	return f.hashes, nil
}

func (f *fakeEmbedStore) UpsertMemoryEmbeddings(_ context.Context, records []EmbeddingRecord) error {
	if f.upsErrs != nil {
		return f.upsErrs
	}
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

func testEmbedInput(id, title, summary string) EmbeddingInput {
	return EmbeddingInput{ID: id, Title: title, Summary: summary}
}

// TestRunEmbeddingIncrementalSkipsUnchanged 未变更与空摘要跳过，只算新增/变化。
func TestRunEmbeddingIncrementalSkipsUnchanged(t *testing.T) {
	unchanged := testEmbedInput("mem-a", "猫", "猫今天很乖")
	changed := testEmbedInput("mem-b", "天气", "今天晴天")
	empty := testEmbedInput("mem-c", "", "  ")
	fresh := testEmbedInput("mem-d", "", "新记录")
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{unchanged, changed, empty, fresh},
		hashes: map[string]string{
			"mem-a": hashText(embeddingText(unchanged)),
			"mem-b": "stale-hash",
		},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{BatchSize: 8})
	if err != nil {
		t.Fatalf("增量入库失败：%v", err)
	}
	if stats.Total != 4 || stats.Skipped != 2 || stats.Pending != 2 || stats.Batches != 1 || stats.Embedded != 2 {
		t.Fatalf("统计不符：%+v", stats)
	}
	if embedder.calls != 1 {
		t.Errorf("embedding 调用次数 = %d，期望 1", embedder.calls)
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

// TestRunEmbeddingNoPending 全部未变更时不调用 embedding。
func TestRunEmbeddingNoPending(t *testing.T) {
	input := testEmbedInput("mem-a", "猫", "乖")
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{input},
		hashes: map[string]string{"mem-a": hashText(embeddingText(input))},
	}
	embedder := &fakeEmbedWorker{}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{})
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

// TestRunEmbeddingRetriesOnce 首次失败重试一次后成功。
func TestRunEmbeddingRetriesOnce(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{testEmbedInput("mem-a", "", "鱼")}}
	embedder := &fakeEmbedWorker{err: errors.New("网络抖动"), failTimes: 1}
	stats, err := RunEmbedding(context.Background(), store, embedder, EmbedOptions{RetryDelay: 1})
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
		BatchSize: 2,
		Progress:  func(_, _, _ int) { progress++ },
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
	_, err := RunEmbedding(context.Background(), store, short, EmbedOptions{})
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
