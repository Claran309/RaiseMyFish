package memory

// 本文件测 0.10 的记忆混合检索：关键词档位、语义加权、降级与错误透传。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeSearchBase struct {
	ctx       Context
	shared    SharedState
	err       error
	recalls   int
	sharedCnt int
	gotScope  Scope
}

func (f *fakeSearchBase) Recall(_ context.Context, scope Scope) (Context, error) {
	f.recalls++
	f.gotScope = scope
	if f.err != nil {
		return Context{}, f.err
	}
	return f.ctx, nil
}

func (f *fakeSearchBase) Shared(_ context.Context, scope Scope) (SharedState, error) {
	f.sharedCnt++
	return f.shared, nil
}

type fakeSearchSource struct {
	keyword       []Record
	semantic      []SemanticRecord
	messages      []Message
	keywordErr    error
	semanticErr   error
	messageErr    error
	gotKeyword    string
	gotMsgKeyword string
	gotLimit      int
	gotSemLimit   int
	gotVector     []float64
	keywordCalls  int
	semanticCalls int
	messageCalls  int
}

func (f *fakeSearchSource) KeywordRecords(_ context.Context, _ Scope, keyword string, limit int) ([]Record, error) {
	f.keywordCalls++
	f.gotKeyword = keyword
	f.gotLimit = limit
	return f.keyword, f.keywordErr
}

func (f *fakeSearchSource) KeywordMessages(_ context.Context, _ Scope, keyword string, limit int) ([]Message, error) {
	f.messageCalls++
	f.gotMsgKeyword = keyword
	return f.messages, f.messageErr
}

func (f *fakeSearchSource) SemanticRecords(_ context.Context, _ Scope, vector []float64, limit int) ([]SemanticRecord, error) {
	f.semanticCalls++
	f.gotVector = vector
	f.gotSemLimit = limit
	return f.semantic, f.semanticErr
}

type fakeQueryEmbedder struct {
	vectors [][]float64
	err     error
	calls   int
	inputs  []string
}

func (f *fakeQueryEmbedder) Embed(_ context.Context, inputs []string) ([][]float64, error) {
	f.calls++
	f.inputs = inputs
	if f.err != nil {
		return nil, f.err
	}
	return f.vectors, nil
}

func searchScope() Scope { return Scope{Channel: "live", Session: "room-1"} }

func searchRecord(id, summary string, importance float64) Record {
	return Record{
		ID: id, Channel: "live", Session: "room-1", Summary: summary,
		Importance: importance, Confidence: 0.8, ValidFrom: readerBaseTime,
	}
}

func searchBaseContext() Context {
	return Context{Scope: searchScope(), Records: []Record{searchRecord("base-1", "最近摘要", 0.5)}}
}

func newTestHybrid(t *testing.T, source SearchRecordSource, embedder Embedder, opts HybridSearchOptions) *HybridSearcher {
	t.Helper()
	base := &fakeSearchBase{ctx: searchBaseContext()}
	h, err := NewHybridSearcher(base, source, embedder, opts)
	if err != nil {
		t.Fatalf("装配混合检索失败：%v", err)
	}
	return h
}

// TestHybridSearchEmptyQueryUsesRecall 空查询直接复用既有 Recall，不消耗 embedding。
func TestHybridSearchEmptyQueryUsesRecall(t *testing.T) {
	source := &fakeSearchSource{}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{make([]float64, EmbeddingDimension)}}
	h := newTestHybrid(t, source, embedder, HybridSearchOptions{Alpha: 0.5})
	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "  ", Limit: 5})
	if err != nil {
		t.Fatalf("空查询失败：%v", err)
	}
	if source.keywordCalls != 0 || source.semanticCalls != 0 || source.messageCalls != 0 || embedder.calls != 0 {
		t.Errorf("空查询不应查检索源：kw=%d sem=%d msg=%d embed=%d", source.keywordCalls, source.semanticCalls, source.messageCalls, embedder.calls)
	}
	if len(got.Records) != 1 || got.Records[0].ID != "base-1" {
		t.Errorf("应返回既有召回：%+v", got.Records)
	}
}

// TestHybridSearchIncludesMessageText 摘要为空时也要从 events 原文召回。
func TestHybridSearchIncludesMessageText(t *testing.T) {
	message := Message{
		EventID: "ev-1", Channel: "live", Session: "room-1", Dir: "in",
		FromID: "1001", FromName: "弓长三益", Text: "今晚也在", At: readerBaseTime,
	}
	source := &fakeSearchSource{messages: []Message{message}}
	h := newTestHybrid(t, source, nil, HybridSearchOptions{Alpha: 0.5})

	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "弓长三益", Limit: 5})
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if source.messageCalls != 1 || source.gotMsgKeyword != "弓长三益" {
		t.Errorf("原文检索参数不符：calls=%d q=%q", source.messageCalls, source.gotMsgKeyword)
	}
	if len(got.Recent) != 1 || got.Recent[0].FromName != "弓长三益" {
		t.Fatalf("原文召回不符：%+v", got.Recent)
	}
	if len(got.Records) != 0 {
		t.Errorf("无摘要时 Records 应为空：%+v", got.Records)
	}
}

// TestHybridSearchMessageErrorPropagates events 查询失败不能被吞成空结果。
func TestHybridSearchMessageErrorPropagates(t *testing.T) {
	source := &fakeSearchSource{messageErr: errors.New("events 查询失败")}
	h := newTestHybrid(t, source, nil, HybridSearchOptions{})
	if _, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "弓长三益"}); err == nil {
		t.Fatal("原文查询失败应返回错误")
	}
}

// TestHybridSearchScoresKeywordAndSemantic 关键词档位 + α×相似度合并排序。
func TestHybridSearchScoresKeywordAndSemantic(t *testing.T) {
	source := &fakeSearchSource{
		keyword: []Record{
			searchRecord("kw-only", "关键词命中", 0.5),
			searchRecord("both", "两路都命中", 0.5),
		},
		semantic: []SemanticRecord{
			{Record: searchRecord("sem-only", "语义命中", 0.5), Similarity: 0.9},
			{Record: searchRecord("both", "两路都命中", 0.5), Similarity: 0.8},
		},
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	h := newTestHybrid(t, source, embedder, HybridSearchOptions{Alpha: 0.5})

	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "爱要白饭", Limit: 10})
	if err != nil {
		t.Fatalf("混合检索失败：%v", err)
	}
	// both = 1 + 0.5*0.8 = 1.4；kw-only = 1；sem-only = 0.45
	want := []string{"both", "kw-only", "sem-only"}
	if len(got.Records) != len(want) {
		t.Fatalf("返回条数 = %d，期望 %d：%+v", len(got.Records), len(want), got.Records)
	}
	for i, id := range want {
		if got.Records[i].ID != id {
			t.Errorf("第 %d 条 = %q，期望 %q", i, got.Records[i].ID, id)
		}
	}
	if source.gotKeyword != "爱要白饭" || source.gotLimit != 10 {
		t.Errorf("关键词查询参数不符：q=%q limit=%d", source.gotKeyword, source.gotLimit)
	}
	if len(embedder.inputs) != 1 || embedder.inputs[0] != "爱要白饭" {
		t.Errorf("embedding 输入不符：%v", embedder.inputs)
	}
	if source.semanticCalls != 1 || len(source.gotVector) != EmbeddingDimension {
		t.Errorf("语义查询参数不符：calls=%d dim=%d", source.semanticCalls, len(source.gotVector))
	}
	if source.gotSemLimit != semanticCandidateLimit {
		t.Errorf("语义候选数 = %d，期望 %d", source.gotSemLimit, semanticCandidateLimit)
	}
}

// TestHybridSearchAlphaZeroKeywordOnly α=0 时不调用 embedding。
func TestHybridSearchAlphaZeroKeywordOnly(t *testing.T) {
	source := &fakeSearchSource{keyword: []Record{searchRecord("kw", "关键词", 0.5)}}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	h := newTestHybrid(t, source, embedder, HybridSearchOptions{Alpha: 0})
	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "鱼"})
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if embedder.calls != 0 || source.semanticCalls != 0 {
		t.Errorf("α=0 不应走语义：embed=%d sem=%d", embedder.calls, source.semanticCalls)
	}
	if len(got.Records) != 1 || got.Records[0].ID != "kw" {
		t.Errorf("关键词结果不符：%+v", got.Records)
	}
}

// TestHybridSearchDegradesOnEmbeddingError embedding 失败降级关键词且回调可见。
func TestHybridSearchDegradesOnEmbeddingError(t *testing.T) {
	source := &fakeSearchSource{keyword: []Record{searchRecord("kw", "关键词", 0.5)}}
	embedder := &fakeQueryEmbedder{err: errors.New("embedding 不可用")}
	var degraded error
	h := newTestHybrid(t, source, embedder, HybridSearchOptions{
		Alpha:           0.5,
		OnSemanticError: func(err error) { degraded = err },
	})
	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "鱼"})
	if err != nil {
		t.Fatalf("降级不应报错：%v", err)
	}
	if degraded == nil || !strings.Contains(degraded.Error(), "不可用") {
		t.Errorf("降级回调 = %v", degraded)
	}
	if len(got.Records) != 1 || got.Records[0].ID != "kw" {
		t.Errorf("降级后应返回关键词结果：%+v", got.Records)
	}
}

// TestHybridSearchDegradesOnSemanticError 语义查询失败也降级关键词。
func TestHybridSearchDegradesOnSemanticError(t *testing.T) {
	source := &fakeSearchSource{
		keyword:     []Record{searchRecord("kw", "关键词", 0.5)},
		semanticErr: errors.New("向量查询失败"),
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	var degraded error
	h := newTestHybrid(t, source, embedder, HybridSearchOptions{
		Alpha:           0.5,
		OnSemanticError: func(err error) { degraded = err },
	})
	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "鱼"})
	if err != nil {
		t.Fatalf("降级不应报错：%v", err)
	}
	if degraded == nil {
		t.Error("应触发降级回调")
	}
	if len(got.Records) != 1 || got.Records[0].ID != "kw" {
		t.Errorf("降级后应返回关键词结果：%+v", got.Records)
	}
}

// TestHybridSearchKeywordErrorPropagates 关键词查询失败不能被降级吞掉。
func TestHybridSearchKeywordErrorPropagates(t *testing.T) {
	source := &fakeSearchSource{keywordErr: errors.New("数据库挂了")}
	h := newTestHybrid(t, source, nil, HybridSearchOptions{Alpha: 0.5})
	if _, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "鱼"}); err == nil {
		t.Fatal("关键词查询失败应返回错误")
	}
}

// TestHybridSearchRejectsInvalidScope 非法 Scope 直接拒绝。
func TestHybridSearchRejectsInvalidScope(t *testing.T) {
	h := newTestHybrid(t, &fakeSearchSource{}, nil, HybridSearchOptions{})
	bad := Scope{Channel: "dm", Session: "u-1", Private: false}
	if _, err := h.Search(context.Background(), SearchQuery{Scope: bad, Text: "鱼"}); err == nil {
		t.Fatal("非法 Scope 应被拒绝")
	}
}

// TestHybridSearchRejectsLowRelevanceSemantic I03/T13：低于下限的向量命中被丢弃。
//
// 允许返回 0 条：宁可不注入记忆，也不拿「最相近的无关内容」凑上下文。
func TestHybridSearchRejectsLowRelevanceSemantic(t *testing.T) {
	source := &fakeSearchSource{
		semantic: []SemanticRecord{
			{Record: searchRecord("sem-low", "无关歌词", 0.4), Similarity: 0.33},
		},
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	h := newTestHybrid(t, source, embedder, HybridSearchOptions{Alpha: 0.5})

	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "随便一个陌生名字", Limit: 5})
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if len(got.Records) != 0 || len(got.Recent) != 0 {
		t.Fatalf("低相关语义应被拒：records=%+v recent=%+v", got.Records, got.Recent)
	}
}

// TestHybridSearchKeepsRelevantSemantic 过阈值的语义命中保留，并排在关键词之后。
func TestHybridSearchKeepsRelevantSemantic(t *testing.T) {
	source := &fakeSearchSource{
		keyword:  []Record{searchRecord("kw", "关键词命中", 0.5)},
		semantic: []SemanticRecord{{Record: searchRecord("sem", "语义命中", 0.5), Similarity: 0.85}},
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	h := newTestHybrid(t, source, embedder, HybridSearchOptions{Alpha: 0.5})

	got, err := h.Search(context.Background(), SearchQuery{Scope: searchScope(), Text: "鱼", Limit: 5})
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if len(got.Records) != 2 || got.Records[0].ID != "kw" || got.Records[1].ID != "sem" {
		t.Fatalf("排序不符：%+v", got.Records)
	}
}

// TestHybridSearcherDelegatesReader Recall/Shared 原样委托，不影响既有调用方。
func TestHybridSearcherDelegatesReader(t *testing.T) {
	base := &fakeSearchBase{ctx: searchBaseContext(), shared: SharedState{Scope: searchScope()}}
	h, err := NewHybridSearcher(base, &fakeSearchSource{}, nil, HybridSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Recall(context.Background(), searchScope()); err != nil {
		t.Fatalf("Recall 失败：%v", err)
	}
	if _, err := h.Shared(context.Background(), searchScope()); err != nil {
		t.Fatalf("Shared 失败：%v", err)
	}
	if base.recalls != 1 || base.sharedCnt != 1 {
		t.Errorf("未委托：recall=%d shared=%d", base.recalls, base.sharedCnt)
	}
}

// TestNewHybridSearcherValidation 依赖与 alpha 边界。
func TestNewHybridSearcherValidation(t *testing.T) {
	if _, err := NewHybridSearcher(nil, &fakeSearchSource{}, nil, HybridSearchOptions{}); err == nil {
		t.Error("base 为 nil 应报错")
	}
	if _, err := NewHybridSearcher(&fakeSearchBase{}, nil, nil, HybridSearchOptions{}); err == nil {
		t.Error("source 为 nil 应报错")
	}
	base := &fakeSearchBase{ctx: searchBaseContext()}
	if _, err := NewHybridSearcher(base, &fakeSearchSource{}, nil, HybridSearchOptions{Alpha: 1.5}); err == nil {
		t.Error("alpha 越界应报错")
	}
}

// vectorWith 造一个第 idx 位为 1 的 1024 维向量。
func vectorWith(idx int) []float64 {
	vector := make([]float64, EmbeddingDimension)
	vector[idx%len(vector)] = 1
	return vector
}
