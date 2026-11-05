package viewer

// 本文件测观众档案混合检索：关键词档位、语义加权、降级与边界。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

type fakeProfileSource struct {
	keyword     []store.ViewerProfileHit
	semantic    []store.ViewerProfileHit
	keywordErr  error
	semanticErr error
	gotKeyword  string
	gotLimit    int
	gotVector   []float64
	kwCalls     int
	semCalls    int
}

func (f *fakeProfileSource) KeywordProfiles(_ context.Context, keyword string, limit int) ([]store.ViewerProfileHit, error) {
	f.kwCalls++
	f.gotKeyword = keyword
	f.gotLimit = limit
	return f.keyword, f.keywordErr
}

func (f *fakeProfileSource) SemanticProfiles(_ context.Context, vector []float64, limit int) ([]store.ViewerProfileHit, error) {
	f.semCalls++
	f.gotVector = vector
	if len(vector) != EmbeddingDimension {
		return nil, errors.New("向量维度不符")
	}
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

func viewerProfile(uid, name string) store.ViewerProfile {
	return store.ViewerProfile{
		UID: uid, Name: name, Hits: 3, Replies: 1,
		LastSeen:    time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
		RecentTexts: []string{"今天也要白饭"},
	}
}

func hit(uid, name string, sim float64) store.ViewerProfileHit {
	return store.ViewerProfileHit{Profile: viewerProfile(uid, name), Similarity: sim}
}

func newTestSearcher(t *testing.T, source ProfileSource, embedder Embedder, opts SearchOptions) *Searcher {
	t.Helper()
	s, err := NewSearcher(source, embedder, opts)
	if err != nil {
		t.Fatalf("装配档案检索失败：%v", err)
	}
	return s
}

// TestSearchScoresKeywordAndSemantic 关键词档位 + α×相似度合并排序。
func TestSearchScoresKeywordAndSemantic(t *testing.T) {
	source := &fakeProfileSource{
		keyword: []store.ViewerProfileHit{hit("u-kw", "关键词", 0)},
		semantic: []store.ViewerProfileHit{
			hit("u-sem", "语义", 0.9),
			hit("u-kw", "关键词", 0.1),
			hit("u-both", "两路", 0.8),
		},
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	s := newTestSearcher(t, source, embedder, SearchOptions{Alpha: 0.5})

	hits, err := s.Search(context.Background(), "那个爱要白饭的人", 10)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	// u-kw = 1 + 0.5*0.1 = 1.05；u-both = 0.4；u-sem = 0.45
	want := []string{"u-kw", "u-sem", "u-both"}
	if len(hits) != len(want) {
		t.Fatalf("命中数 = %d，期望 %d：%+v", len(hits), len(want), hits)
	}
	for i, uid := range want {
		if hits[i].Profile.UID != uid {
			t.Errorf("第 %d 个 = %q，期望 %q", i, hits[i].Profile.UID, uid)
		}
	}
	if source.gotKeyword != "那个爱要白饭的人" || source.gotLimit != 10 {
		t.Errorf("关键词参数不符：q=%q limit=%d", source.gotKeyword, source.gotLimit)
	}
	if len(embedder.inputs) != 1 || embedder.inputs[0] != "那个爱要白饭的人" {
		t.Errorf("embedding 输入不符：%v", embedder.inputs)
	}
}

// TestSearchDegradesOnEmbeddingError 语义不可用时退回关键词且不报错。
func TestSearchDegradesOnEmbeddingError(t *testing.T) {
	source := &fakeProfileSource{keyword: []store.ViewerProfileHit{hit("u-1", "关键词", 0)}}
	embedder := &fakeQueryEmbedder{err: errors.New("embedding 不可用")}
	var degraded error
	s := newTestSearcher(t, source, embedder, SearchOptions{
		Alpha:           0.5,
		OnSemanticError: func(err error) { degraded = err },
	})
	hits, err := s.Search(context.Background(), "爱要白饭", 5)
	if err != nil {
		t.Fatalf("降级不应报错：%v", err)
	}
	if degraded == nil || !strings.Contains(degraded.Error(), "不可用") {
		t.Errorf("降级回调 = %v", degraded)
	}
	if len(hits) != 1 || hits[0].Profile.UID != "u-1" {
		t.Errorf("降级后应返回关键词结果：%+v", hits)
	}
	if hits[0].Score != 1 {
		t.Errorf("关键词命中 score = %v，期望 1", hits[0].Score)
	}
}

// TestSearchDegradesOnSemanticError 语义查询失败同样降级。
func TestSearchDegradesOnSemanticError(t *testing.T) {
	source := &fakeProfileSource{
		keyword:     []store.ViewerProfileHit{hit("u-1", "关键词", 0)},
		semanticErr: errors.New("向量查询失败"),
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	var degraded error
	s := newTestSearcher(t, source, embedder, SearchOptions{
		Alpha:           0.5,
		OnSemanticError: func(err error) { degraded = err },
	})
	hits, err := s.Search(context.Background(), "爱要白饭", 5)
	if err != nil {
		t.Fatalf("降级不应报错：%v", err)
	}
	if degraded == nil || len(hits) != 1 {
		t.Errorf("降级行为不符：degraded=%v hits=%+v", degraded, hits)
	}
}

// TestSearchKeywordOnlyWithoutEmbedder 没有 embedding 时纯关键词。
func TestSearchKeywordOnlyWithoutEmbedder(t *testing.T) {
	source := &fakeProfileSource{keyword: []store.ViewerProfileHit{hit("u-1", "关键词", 0)}}
	s := newTestSearcher(t, source, nil, SearchOptions{Alpha: 0.5})
	if _, err := s.Search(context.Background(), "白饭", 5); err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if source.semCalls != 0 {
		t.Errorf("无 embedding 不应查语义：%d", source.semCalls)
	}
}

// questionProfileSource 只在剥掉问句外壳后命中，模拟真实昵称匹配。
type questionProfileSource struct {
	queries []string
}

func (s *questionProfileSource) KeywordProfiles(_ context.Context, keyword string, _ int) ([]store.ViewerProfileHit, error) {
	s.queries = append(s.queries, keyword)
	if keyword != "弓长三益" {
		return nil, nil
	}
	return []store.ViewerProfileHit{hit("5005", "弓长三益", 0)}, nil
}

func (s *questionProfileSource) SemanticProfiles(context.Context, []float64, int) ([]store.ViewerProfileHit, error) {
	return nil, nil
}

// TestSearchStripsQuestionShell 「弓长三益是谁」也要命中昵称「弓长三益」。
func TestSearchStripsQuestionShell(t *testing.T) {
	source := &questionProfileSource{}
	s := newTestSearcher(t, source, nil, SearchOptions{})
	hits, err := s.Search(context.Background(), "弓长三益是谁", 3)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if len(hits) != 1 || hits[0].Profile.UID != "5005" {
		t.Fatalf("问句未命中昵称：%+v", hits)
	}
	if len(source.queries) != 2 || source.queries[0] != "弓长三益是谁" || source.queries[1] != "弓长三益" {
		t.Errorf("关键词候选顺序不符：%v", source.queries)
	}
}

// TestStripQuestionShell 前后缀剥离的边界。
func TestStripQuestionShell(t *testing.T) {
	cases := map[string]string{
		"弓长三益是谁":    "弓长三益",
		"弓长三益？":     "弓长三益",
		"请问弓长三益吗":   "弓长三益",
		"你还记得弓长三益吗": "弓长三益",
		"那个爱要白饭的人":  "爱要白饭",
		"弓长三益":      "弓长三益",
		"是谁":        "是谁",
	}
	for input, want := range cases {
		if got := stripQuestionShell(input); got != want {
			t.Errorf("stripQuestionShell(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestSearchKeywordErrorPropagates 关键词查询失败必须上抛。
func TestSearchKeywordErrorPropagates(t *testing.T) {
	source := &fakeProfileSource{keywordErr: errors.New("数据库挂了")}
	s := newTestSearcher(t, source, nil, SearchOptions{})
	if _, err := s.Search(context.Background(), "白饭", 5); err == nil {
		t.Fatal("关键词失败应返回错误")
	}
}

// TestSearchQueryValidation 空查询与超长查询拒绝。
func TestSearchQueryValidation(t *testing.T) {
	s := newTestSearcher(t, &fakeProfileSource{}, nil, SearchOptions{})
	if _, err := s.Search(context.Background(), "  ", 5); err == nil {
		t.Error("空查询应报错")
	}
	if _, err := s.Search(context.Background(), strings.Repeat("鱼", maxSearchQueryRunes+1), 5); err == nil {
		t.Error("超长查询应报错")
	}
}

// TestSearchLimitClamp 越界 limit 夹到默认/上限。
func TestSearchLimitClamp(t *testing.T) {
	source := &fakeProfileSource{}
	s := newTestSearcher(t, source, nil, SearchOptions{})
	if _, err := s.Search(context.Background(), "白饭", 0); err != nil {
		t.Fatal(err)
	}
	if source.gotLimit != defaultSearchLimit {
		t.Errorf("默认 limit = %d，期望 %d", source.gotLimit, defaultSearchLimit)
	}
	if _, err := s.Search(context.Background(), "白饭", maxSearchLimit+10); err != nil {
		t.Fatal(err)
	}
	if source.gotLimit != maxSearchLimit {
		t.Errorf("上限 limit = %d，期望 %d", source.gotLimit, maxSearchLimit)
	}
}

// vectorWith 造一个第 idx 位为 1 的 1024 维向量。
func vectorWith(idx int) []float64 {
	vector := make([]float64, EmbeddingDimension)
	vector[idx%len(vector)] = 1
	return vector
}
