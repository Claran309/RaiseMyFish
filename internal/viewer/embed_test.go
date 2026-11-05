package viewer

// 本文件测观众档案的增量向量化：跳过未变更、空档案、批量与重试。

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
}

func (f *fakeEmbedStore) ViewerEmbeddingInputs(context.Context) ([]EmbeddingInput, error) {
	return f.inputs, f.err
}

func (f *fakeEmbedStore) ExistingProfileEmbeddingHashes(context.Context) (map[string]string, error) {
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

// TestRunEmbeddingIncrementalSkipsUnchanged 未变更与空档案跳过，只算新增/变化。
func TestRunEmbeddingIncrementalSkipsUnchanged(t *testing.T) {
	unchanged := EmbeddingInput{UID: "u-a", Name: "白饭", Texts: []string{"今天也要白饭"}}
	changed := EmbeddingInput{UID: "u-b", Name: "熟客", Texts: []string{"又来了"}}
	empty := EmbeddingInput{UID: "u-c"}
	fresh := EmbeddingInput{UID: "u-d", Name: "新人", Texts: []string{"你好"}}
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{unchanged, changed, empty, fresh},
		hashes: map[string]string{
			"u-a": hashText(embeddingText(unchanged)),
			"u-b": "stale-hash",
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
		got[record.UID] = record.Hash
		if len(record.Embedding) != EmbeddingDimension {
			t.Errorf("%s 向量维度 = %d", record.UID, len(record.Embedding))
		}
	}
	if got["u-b"] != hashText("熟客\n又来了") {
		t.Errorf("变化档案 hash 不符：%q", got["u-b"])
	}
	if got["u-d"] != hashText("新人\n你好") {
		t.Errorf("新增档案 hash 不符：%q", got["u-d"])
	}
	if _, ok := got["u-a"]; ok {
		t.Error("未变更档案不应重新写入")
	}
}

// TestRunEmbeddingNoPending 全部未变更时不调用 embedding。
func TestRunEmbeddingNoPending(t *testing.T) {
	input := EmbeddingInput{UID: "u-a", Name: "白饭"}
	store := &fakeEmbedStore{
		inputs: []EmbeddingInput{input},
		hashes: map[string]string{"u-a": hashText(embeddingText(input))},
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

// TestRunEmbeddingBatchAndProgress 3 条按 2 条一批，进度两次。
func TestRunEmbeddingBatchAndProgress(t *testing.T) {
	store := &fakeEmbedStore{inputs: []EmbeddingInput{
		{UID: "u-a", Name: "一"}, {UID: "u-b", Name: "二"}, {UID: "u-c", Name: "三"},
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

// TestEmbeddingTextJoin 昵称 + 最近发言 + 最近回复按换行拼接。
func TestEmbeddingTextJoin(t *testing.T) {
	cases := []struct {
		input EmbeddingInput
		want  string
	}{
		{EmbeddingInput{UID: "u", Name: " 白饭 ", Texts: []string{" 今天 ", ""}, Replies: []string{"好"}}, "白饭\n今天\n好"},
		{EmbeddingInput{UID: "u", Name: "", Texts: []string{"只有发言"}}, "只有发言"},
		{EmbeddingInput{UID: "u"}, ""},
	}
	for _, tc := range cases {
		if got := embeddingText(tc.input); got != tc.want {
			t.Errorf("embeddingText(%+v) = %q，期望 %q", tc.input, got, tc.want)
		}
	}
}
