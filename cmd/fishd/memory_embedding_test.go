package main

// 本文件测 M08：记忆/档案向量的定时增量刷新。
//
// 关注三件事：
//   - 只算「缺失或内容变了」的行（增量跳过期不重复花钱）；
//   - 一类索引失败不阻塞另一类（记忆挂了档案照刷）；
//   - 关闭 auto_refresh 时不启动。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
)

// fakeVectorEmbedder 记录调用次数并返回合法维度的向量。
type fakeVectorEmbedder struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeVectorEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	f.mu.Lock()
	f.calls += len(inputs)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float64, len(inputs))
	for i := range out {
		vector := make([]float64, memory.EmbeddingDimension)
		vector[i%len(vector)] = 1
		out[i] = vector
	}
	return out, nil
}

func (f *fakeVectorEmbedder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeMemoryEmbedStore 是内存版记忆向量存储。
type fakeMemoryEmbedStore struct {
	inputs    []memory.EmbeddingInput
	existing  map[string]string
	upserted  []memory.EmbeddingRecord
	inputsErr error
	upsertErr error
	upserts   int
	// allowPrivate 记录 refresher 透传的授权参数。
	allowPrivate bool
}

func (f *fakeMemoryEmbedStore) MemoryEmbeddingInputs(_ context.Context, allowPrivate bool) ([]memory.EmbeddingInput, error) {
	f.allowPrivate = allowPrivate
	if f.inputsErr != nil {
		return nil, f.inputsErr
	}
	return f.inputs, nil
}

func (f *fakeMemoryEmbedStore) ExistingMemoryEmbeddingHashes(context.Context) (map[string]string, error) {
	if f.existing == nil {
		return map[string]string{}, nil
	}
	return f.existing, nil
}

func (f *fakeMemoryEmbedStore) UpsertMemoryEmbeddings(_ context.Context, records []memory.EmbeddingRecord) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserts++
	f.upserted = append(f.upserted, records...)
	return nil
}

// fakeViewerEmbedStore 是内存版档案向量存储。
type fakeViewerEmbedStore struct {
	inputs    []viewer.EmbeddingInput
	existing  map[string]map[viewer.EmbeddingKind]string
	upserted  []viewer.EmbeddingRecord
	inputsErr error
}

func (f *fakeViewerEmbedStore) ViewerEmbeddingInputs(context.Context) ([]viewer.EmbeddingInput, error) {
	if f.inputsErr != nil {
		return nil, f.inputsErr
	}
	return f.inputs, nil
}

func (f *fakeViewerEmbedStore) ExistingProfileEmbeddingHashes(context.Context) (map[string]map[viewer.EmbeddingKind]string, error) {
	if f.existing == nil {
		return map[string]map[viewer.EmbeddingKind]string{}, nil
	}
	return f.existing, nil
}

func (f *fakeViewerEmbedStore) UpsertProfileEmbeddings(_ context.Context, records []viewer.EmbeddingRecord) error {
	f.upserted = append(f.upserted, records...)
	return nil
}

// newTestRefresher 造一个只跑一轮的 refresher。
func newTestRefresher(mem memory.EmbeddingStore, vw viewer.EmbeddingStore, emb vectorEmbedder) *memoryEmbeddingRefresher {
	return &memoryEmbeddingRefresher{
		memoryStore: mem,
		viewerStore: vw,
		embedder:    emb,
		batchSize:   32,
		logger:      zap.NewNop(),
	}
}

// TestVectorRefreshEmbedsPending 缺向量的行必须被算出来。
func TestVectorRefreshEmbedsPending(t *testing.T) {
	memStore := &fakeMemoryEmbedStore{inputs: []memory.EmbeddingInput{
		{ID: "m1", Title: "事实：我是学生", Summary: "我今年大二"},
		{ID: "m2", Title: "偏好：爱吃白饭", Summary: "每天都要白饭"},
	}}
	vwStore := &fakeViewerEmbedStore{inputs: []viewer.EmbeddingInput{
		{UID: "2002", Name: "白饭哥", Texts: []string{"今天也要白饭"}},
	}}
	emb := &fakeVectorEmbedder{}

	newTestRefresher(memStore, vwStore, emb).refreshOnce(context.Background())

	if len(memStore.upserted) != 2 {
		t.Fatalf("记忆应写入 2 条向量：%+v", memStore.upserted)
	}
	// M10：一份档案拆成 identity + facts 两路（没有回复 → 无 interaction）。
	if len(vwStore.upserted) != 2 || vwStore.upserted[0].UID != "2002" {
		t.Fatalf("档案应写入 2 条向量：%+v", vwStore.upserted)
	}
	if emb.count() != 4 {
		t.Errorf("embedding 调用条数 = %d，期望 4", emb.count())
	}
	for _, rec := range memStore.upserted {
		if rec.Hash == "" || len(rec.Embedding) != memory.EmbeddingDimension {
			t.Errorf("向量记录不完整：%+v", rec)
		}
	}
}

// TestVectorRefreshSkipsUnchanged 第二轮不应重复花钱（hash 未变即跳过）。
func TestVectorRefreshSkipsUnchanged(t *testing.T) {
	memStore := &fakeMemoryEmbedStore{inputs: []memory.EmbeddingInput{
		{ID: "m1", Title: "事实：我是学生", Summary: "我今年大二"},
	}}
	vwStore := &fakeViewerEmbedStore{inputs: []viewer.EmbeddingInput{
		{UID: "2002", Name: "白饭哥", Texts: []string{"今天也要白饭"}},
	}}
	emb := &fakeVectorEmbedder{}
	r := newTestRefresher(memStore, vwStore, emb)

	r.refreshOnce(context.Background())
	first := emb.count()
	// 记忆 1 条 + 档案 identity/facts 两路 = 3。
	if first != 3 {
		t.Fatalf("首轮应调用 3 次 embedding，实际 %d", first)
	}

	// 把首轮结果当成「已入库」的 hash，模拟下一次轮询。
	memStore.existing = map[string]string{}
	for _, rec := range memStore.upserted {
		memStore.existing[rec.ID] = rec.Hash
	}
	vwStore.existing = map[string]map[viewer.EmbeddingKind]string{}
	for _, rec := range vwStore.upserted {
		if vwStore.existing[rec.UID] == nil {
			vwStore.existing[rec.UID] = map[viewer.EmbeddingKind]string{}
		}
		vwStore.existing[rec.UID][rec.Kind] = rec.Hash
	}

	r.refreshOnce(context.Background())
	if emb.count() != first {
		t.Fatalf("内容没变仍在重复 embedding：前=%d 后=%d", first, emb.count())
	}
}

// TestVectorRefreshFailureDoesNotBlockOther 记忆侧失败时档案侧照刷。
func TestVectorRefreshFailureDoesNotBlockOther(t *testing.T) {
	memStore := &fakeMemoryEmbedStore{inputsErr: errors.New("记忆表读取失败")}
	vwStore := &fakeViewerEmbedStore{inputs: []viewer.EmbeddingInput{
		{UID: "2002", Name: "白饭哥", Texts: []string{"今天也要白饭"}},
	}}
	emb := &fakeVectorEmbedder{}

	newTestRefresher(memStore, vwStore, emb).refreshOnce(context.Background())

	if len(vwStore.upserted) != 2 {
		t.Fatalf("记忆失败不应阻塞档案刷新：%+v", vwStore.upserted)
	}
	if emb.count() != 2 {
		t.Errorf("embedding 调用 = %d，期望 2（identity+facts）", emb.count())
	}
}

// TestVectorRefreshPrivateGate 私密记忆默认不外发；授权后 refresher 透传并纳入。
func TestVectorRefreshPrivateGate(t *testing.T) {
	inputs := []memory.EmbeddingInput{
		{ID: "m1", Title: "公开", Summary: "公开内容"},
		{ID: "m2", Title: "私密", Summary: "私密内容", Private: true},
	}

	// 默认：即使仓储把私密行带回来了，领域层也必须在外发前剔除。
	memStore := &fakeMemoryEmbedStore{inputs: inputs}
	emb := &fakeVectorEmbedder{}
	newTestRefresher(memStore, &fakeViewerEmbedStore{}, emb).refreshOnce(context.Background())
	if memStore.allowPrivate {
		t.Error("默认刷新不应授权私密外发")
	}
	if emb.count() != 1 {
		t.Fatalf("默认刷新外发条数 = %d，期望 1（只发公开）", emb.count())
	}

	// 授权：refresher 把开关透传给仓储，私密行才进入外发。
	memStore = &fakeMemoryEmbedStore{inputs: inputs}
	emb = &fakeVectorEmbedder{}
	r := newTestRefresher(memStore, &fakeViewerEmbedStore{}, emb)
	r.memoryAllowPrivate = true
	r.refreshOnce(context.Background())
	if !memStore.allowPrivate {
		t.Error("授权后 refresher 未向仓储透传 allowPrivate")
	}
	if emb.count() != 2 {
		t.Fatalf("授权后外发条数 = %d，期望 2", emb.count())
	}
}

// TestVectorRefreshEmbedderErrorIsSoft embedding 挂了只记日志，不 panic、不中断。
func TestVectorRefreshEmbedderErrorIsSoft(t *testing.T) {
	memStore := &fakeMemoryEmbedStore{inputs: []memory.EmbeddingInput{
		{ID: "m1", Title: "事实", Summary: "内容"},
	}}
	vwStore := &fakeViewerEmbedStore{}
	emb := &fakeVectorEmbedder{err: errors.New("embedding 服务不可用")}

	newTestRefresher(memStore, vwStore, emb).refreshOnce(context.Background())

	if len(memStore.upserted) != 0 {
		t.Fatalf("失败时不应写入向量：%+v", memStore.upserted)
	}
}

// TestStartMemoryEmbeddingRefreshNoopWhenDisabled 关闭总开关或没有数据库时不启动。
func TestStartMemoryEmbeddingRefreshNoopWhenDisabled(t *testing.T) {
	logger := zap.NewNop()
	cases := []struct {
		name string
		cfg  config.Config
	}{
		{"auto_refresh关闭", config.Config{Embedding: config.Embedding{Enabled: true, AutoRefresh: false}}},
		{"embedding未启用", config.Config{Embedding: config.Embedding{Enabled: false, AutoRefresh: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stop := startMemoryEmbeddingRefresh(context.Background(), tc.cfg, nil, logger)
			if stop == nil {
				t.Fatal("stop 不应为 nil")
			}
			stop() // 必须可安全调用
		})
	}
}

// TestStartMemoryEmbeddingRefreshRequiresDB 没有数据库连接时同样退化为 noop。
func TestStartMemoryEmbeddingRefreshRequiresDB(t *testing.T) {
	cfg := config.Config{Embedding: config.Embedding{Enabled: true, AutoRefresh: true, MemoryRefreshIntervalSeconds: 60}}
	stop := startMemoryEmbeddingRefresh(context.Background(), cfg, nil, zap.NewNop())
	stop()
}

// TestMemoryEmbeddingRefresherStopsOnContextCancel 取消上下文后一轮直接返回，不卡住。
func TestMemoryEmbeddingRefresherStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	memStore := &fakeMemoryEmbedStore{inputs: []memory.EmbeddingInput{{ID: "m1", Title: "t", Summary: "s"}}}
	emb := &fakeVectorEmbedder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		newTestRefresher(memStore, &fakeViewerEmbedStore{}, emb).refreshOnce(ctx)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 refreshOnce 未及时返回")
	}
}
