package knowledge

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

type embedderFunc func(ctx context.Context, inputs []string) ([][]float64, error)

func (f embedderFunc) Embed(ctx context.Context, inputs []string) ([][]float64, error) {
	return f(ctx, inputs)
}

func hybridIDs(t *testing.T, reader Reader, scope Scope, text string) []string {
	t.Helper()
	c, err := reader.Recall(context.Background(), Query{Scope: scope, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if c.Scope != scope || c.Validate() != nil {
		t.Fatal("返回Scope/Revision/权限无效")
	}
	var ids []string
	for _, item := range c.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func TestNewHybridReaderValidatesOptions(t *testing.T) {
	base := loadEntries(t, testEntry("a", "标题"))
	embedder := &fakeEmbedder{}
	store := newFakeEmbeddingStore()
	if _, err := NewHybridReader(nil, embedder, store, HybridOptions{}); err == nil {
		t.Fatal("空base未拒绝")
	}
	if _, err := NewHybridReader(base, nil, store, HybridOptions{}); err == nil {
		t.Fatal("空embedder未拒绝")
	}
	if _, err := NewHybridReader(base, embedder, nil, HybridOptions{}); err == nil {
		t.Fatal("空store未拒绝")
	}
	for _, alpha := range []float64{-0.1, 1.1, math.NaN()} {
		if _, err := NewHybridReader(base, embedder, store, HybridOptions{Alpha: alpha}); err == nil {
			t.Fatalf("非法alpha未拒绝：%v", alpha)
		}
	}
	for _, alpha := range []float64{0, 0.5, 1} {
		if _, err := NewHybridReader(base, embedder, store, HybridOptions{Alpha: alpha}); err != nil {
			t.Fatalf("合法alpha被拒绝：%v %v", alpha, err)
		}
	}
}

func TestHybridRecallMergesSameEntryAndBoostsSimilarity(t *testing.T) {
	a, b := testEntry("a", "甲乙丙"), testEntry("b", "甲乙丙")
	store := newFakeEmbeddingStore()
	store.semantic = []SemanticHit{{ID: "a", Similarity: 0.2}, {ID: "b", Similarity: 0.8}, {ID: "a", Similarity: 0.1}}
	reader, err := NewHybridReader(loadEntries(t, a, b), &fakeEmbedder{}, store, HybridOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(t, reader, publicScope(), "甲乙丙"); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Fatalf("同条目未去重或相似度未参与排序：%v", got)
	}
}

func TestHybridRecallAlphaChangesSemanticWeight(t *testing.T) {
	first, second := testEntry("first", "标题一号"), testEntry("second", "标题二号")
	tags := []string{"甲乙丙"}
	first.Tags, second.Tags = &tags, &tags
	store := newFakeEmbeddingStore()
	store.semantic = []SemanticHit{{ID: "first", Similarity: 0.1}, {ID: "second", Similarity: 0.9}}
	embedder := &fakeEmbedder{}

	reader, err := NewHybridReader(loadEntries(t, first, second), embedder, store, HybridOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(t, reader, publicScope(), "甲乙丙"); !reflect.DeepEqual(got, []string{"second", "first"}) {
		t.Fatalf("alpha未提升高相似条目：%v", got)
	}
	if embedder.calls != 1 || store.lastLimit != semanticCandidateLimit {
		t.Fatalf("语义候选调用不符：calls=%d limit=%d", embedder.calls, store.lastLimit)
	}

	reader, err = NewHybridReader(loadEntries(t, first, second), embedder, store, HybridOptions{Alpha: 0})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(t, reader, publicScope(), "甲乙丙"); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("alpha=0未按关键词排序：%v", got)
	}
	if embedder.calls != 1 {
		t.Fatalf("alpha=0仍调用了embedding：%d", embedder.calls)
	}
}

func TestHybridRecallKeywordTierOutranksSemanticOnly(t *testing.T) {
	title, semanticOnly := testEntry("title", "甲乙丙"), testEntry("semantic", "无关标题")
	store := newFakeEmbeddingStore()
	store.semantic = []SemanticHit{{ID: "semantic", Similarity: 1}, {ID: "title", Similarity: 0}}
	reader, err := NewHybridReader(loadEntries(t, title, semanticOnly), &fakeEmbedder{}, store, HybridOptions{Alpha: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(t, reader, publicScope(), "甲乙丙"); !reflect.DeepEqual(got, []string{"title", "semantic"}) {
		t.Fatalf("关键词档位未压过语义：%v", got)
	}
}

func TestHybridRecallDegradesToKeyword(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeEmbedder, *fakeEmbeddingStore)
	}{
		{
			name: "embedding失败",
			setup: func(e *fakeEmbedder, _ *fakeEmbeddingStore) {
				e.failFirst = 1
				e.err = errors.New("embedding不可用")
			},
		},
		{
			name: "向量检索失败",
			setup: func(_ *fakeEmbedder, s *fakeEmbeddingStore) {
				s.errSearch = errors.New("pgvector不可用")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := loadEntries(t, testEntry("title", "甲乙丙"), testEntry("keyword", "别的标题"))
			want := hybridIDs(t, base, publicScope(), "甲乙丙")
			embedder, store := &fakeEmbedder{}, newFakeEmbeddingStore()
			tc.setup(embedder, store)
			var gotErr error
			reader, err := NewHybridReader(base, embedder, store, HybridOptions{
				Alpha: 0.5,
				OnSemanticError: func(err error) {
					gotErr = err
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := hybridIDs(t, reader, publicScope(), "甲乙丙"); !reflect.DeepEqual(got, want) {
				t.Fatalf("降级结果与纯关键词不一致：%v != %v", got, want)
			}
			if gotErr == nil {
				t.Fatal("降级未回调错误")
			}
		})
	}
}

func TestHybridRecallCancellationStopsWithoutDegrading(t *testing.T) {
	base := loadEntries(t, testEntry("title", "甲乙丙"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	embedder := embedderFunc(func(ctx context.Context, _ []string) ([][]float64, error) {
		cancel()
		return nil, ctx.Err()
	})
	called := 0
	reader, err := NewHybridReader(base, embedder, newFakeEmbeddingStore(), HybridOptions{
		Alpha:           0.5,
		OnSemanticError: func(error) { called++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Recall(ctx, Query{Scope: publicScope(), Text: "甲乙丙"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未返回context.Canceled：%v", err)
	}
	if called != 0 {
		t.Fatal("取消不应触发降级回调")
	}
}

func TestHybridRecallSemanticOnlyFiltersScopeAndCapsTop(t *testing.T) {
	entries := make([]diskEntry, 0, 6)
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		entries = append(entries, testEntry(id, "资料条目"+id))
	}
	out := testEntry("out", "范围外条目")
	out.Scenes = []string{"dm"}
	entries = append(entries, out)
	base := loadEntries(t, entries...)
	store := newFakeEmbeddingStore()
	store.semantic = []SemanticHit{
		{ID: "s1", Similarity: 0.9},
		{ID: "s2", Similarity: 0.8},
		{ID: "s3", Similarity: 0.7},
		{ID: "s4", Similarity: 0.6},
		{ID: "out", Similarity: 0.99},
		{ID: "missing", Similarity: 0.99},
	}
	embedder := &fakeEmbedder{factory: func(string) []float64 {
		vector := make([]float64, EmbeddingDimension)
		vector[0], vector[1] = 0.25, 0.75
		return vector
	}}
	reader, err := NewHybridReader(base, embedder, store, HybridOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(t, reader, publicScope(), "丙丁戊"); !reflect.DeepEqual(got, []string{"s1", "s2", "s3"}) {
		t.Fatalf("语义召回未过滤范围或未截断三条：%v", got)
	}
	if store.lastLimit != 20 {
		t.Fatalf("语义候选上限应为20：%d", store.lastLimit)
	}
	want := make([]float64, EmbeddingDimension)
	want[0], want[1] = 0.25, 0.75
	if !reflect.DeepEqual(store.lastVector, want) {
		t.Fatal("查询向量未按embedding结果传入")
	}
}

func TestHybridRecallSkipsSemanticForEmptyAndOversizeQuery(t *testing.T) {
	base := loadEntries(t, testEntry("title", "甲乙丙"))
	embedder := &fakeEmbedder{}
	reader, err := NewHybridReader(base, embedder, newFakeEmbeddingStore(), HybridOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(t, reader, publicScope(), "   "); len(got) != 0 {
		t.Fatalf("空查询不应召回：%v", got)
	}
	if got := hybridIDs(t, reader, publicScope(), strings.Repeat("甲", 5000)); len(got) != 0 {
		t.Fatalf("超长查询不应召回：%v", got)
	}
	if embedder.calls != 0 {
		t.Fatalf("空/超长查询仍调用embedding：%d", embedder.calls)
	}
}

func TestHybridRecallRejectsInvalidQueryLikeFileReader(t *testing.T) {
	base := loadEntries(t, testEntry("title", "甲乙丙"))
	reader, err := NewHybridReader(base, &fakeEmbedder{}, newFakeEmbeddingStore(), HybridOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Recall(context.Background(), Query{Scope: Scope{}, Text: "甲乙丙"}); err == nil {
		t.Fatal("空范围未拒绝")
	}
	if _, err := reader.Recall(context.Background(), Query{Scope: publicScope(), Text: string([]byte{0xff})}); err == nil {
		t.Fatal("非法UTF-8未拒绝")
	}
}

// TestHybridRecallRejectsLowRelevanceSemantic I03/T13：语义 topK 里低于下限的条目被拒。
//
// 「相对最近」不等于「相关」：随机人名/无关歌词即使排在向量第一，也不能当知识返回。
func TestHybridRecallRejectsLowRelevanceSemantic(t *testing.T) {
	base := loadEntries(t, testEntry("title", "甲乙丙"), testEntry("lyric", "无关的歌词标题"))
	store := newFakeEmbeddingStore()
	store.semantic = []SemanticHit{
		{ID: "lyric", Similarity: 0.31},
		{ID: "title", Similarity: 0.25},
	}
	reader, err := NewHybridReader(base, &fakeEmbedder{}, store, HybridOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(t, reader, publicScope(), "甲乙丙"); !reflect.DeepEqual(got, []string{"title"}) {
		t.Fatalf("低相关语义应被拒，只留关键词命中：%v", got)
	}
}

// TestHybridRecallKeepsRelevantSemantic 过阈值的语义条目仍然保留。
func TestHybridRecallKeepsRelevantSemantic(t *testing.T) {
	base := loadEntries(t, testEntry("title", "甲乙丙"), testEntry("semantic", "丙丁戊相关设定"))
	store := newFakeEmbeddingStore()
	store.semantic = []SemanticHit{{ID: "semantic", Similarity: 0.88}}
	reader, err := NewHybridReader(base, &fakeEmbedder{}, store, HybridOptions{Alpha: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if got := hybridIDs(t, reader, publicScope(), "甲乙丙"); !reflect.DeepEqual(got, []string{"title", "semantic"}) {
		t.Fatalf("高相关语义应保留在关键词之后：%v", got)
	}
}
