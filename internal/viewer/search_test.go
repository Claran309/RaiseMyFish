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
	gotKeywords []string
	gotLimit    int
	gotVector   []float64
	kwCalls     int
	semCalls    int
}

func (f *fakeProfileSource) KeywordProfiles(_ context.Context, keyword string, limit int) ([]store.ViewerProfileHit, error) {
	f.kwCalls++
	f.gotKeyword = keyword
	f.gotKeywords = append(f.gotKeywords, keyword)
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
	// 第一个候选词必须是原句：先按原句查，查不到才剥问句外壳。
	if len(source.gotKeywords) == 0 || source.gotKeywords[0] != "那个爱要白饭的人" {
		t.Errorf("首个关键词应为原句：%v", source.gotKeywords)
	}
	if source.gotLimit != 10 {
		t.Errorf("关键词 limit = %d，期望 10", source.gotLimit)
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

// ---------- I01 / I02 / I03：身份解析、音近与相关性拒绝 ----------

// identitySource 是带 UID 精确与昵称池的假档案源。
//
// KeywordProfiles 按「昵称或最近发言包含关键词」真实过滤，
// 这样「内容命中」与「昵称命中」的档位差异能被测出来。
type identitySource struct {
	pool        []store.ViewerProfile
	semantic    []store.ViewerProfileHit
	keywordErr  error
	semanticErr error
	gotKeywords []string
	semCalls    int
}

func (f *identitySource) KeywordProfiles(_ context.Context, keyword string, limit int) ([]store.ViewerProfileHit, error) {
	f.gotKeywords = append(f.gotKeywords, keyword)
	if f.keywordErr != nil {
		return nil, f.keywordErr
	}
	var hits []store.ViewerProfileHit
	for _, p := range f.pool {
		if profileMentions(p, keyword) {
			hits = append(hits, store.ViewerProfileHit{Profile: p})
		}
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (f *identitySource) SemanticProfiles(context.Context, []float64, int) ([]store.ViewerProfileHit, error) {
	f.semCalls++
	if f.semanticErr != nil {
		return nil, f.semanticErr
	}
	return f.semantic, nil
}

// GetProfile 实现 uidSource：UID 精确命中不需要走 embedding。
func (f *identitySource) GetProfile(_ context.Context, uid string) (store.ViewerProfile, error) {
	for _, p := range f.pool {
		if p.UID == uid {
			return p, nil
		}
	}
	return store.ViewerProfile{}, store.ErrViewerNotFound
}

// TopViewers 实现 namePoolSource：近似匹配要在全量昵称里找。
func (f *identitySource) TopViewers(_ context.Context, limit int) ([]store.ViewerProfile, error) {
	if limit > 0 && len(f.pool) > limit {
		return f.pool[:limit], nil
	}
	return f.pool, nil
}

// profileMentions 模拟 SQL 的 name/recent_texts/recent_replies ILIKE。
func profileMentions(p store.ViewerProfile, keyword string) bool {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return false
	}
	if strings.Contains(strings.ToLower(p.Name), keyword) {
		return true
	}
	for _, text := range append(append([]string{}, p.RecentTexts...), p.RecentReplies...) {
		if strings.Contains(strings.ToLower(text), keyword) {
			return true
		}
	}
	return false
}

// namedProfile 造一份带最近发言的档案。
func namedProfile(uid, name string, texts ...string) store.ViewerProfile {
	p := viewerProfile(uid, name)
	if len(texts) > 0 {
		p.RecentTexts = texts
	}
	return p
}

// TestFindPrefersUIDExact I01/T12：纯数字查询按 UID 精确命中，且不花 embedding。
func TestFindPrefersUIDExact(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{namedProfile("2002", "白饭町")}}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(1)}}
	s := newTestSearcher(t, source, embedder, SearchOptions{Alpha: 0.5})

	got, err := s.Find(context.Background(), "2002", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || len(got.Candidates) != 1 {
		t.Fatalf("UID 精确应为单条 found：%+v", got)
	}
	if got.Candidates[0].Profile.UID != "2002" || got.Candidates[0].Tier != TierUID {
		t.Errorf("UID 精确结果不符：%+v", got.Candidates[0])
	}
	if embedder.calls != 0 || source.semCalls != 0 {
		t.Errorf("UID 精确不应调语义：embed=%d sem=%d", embedder.calls, source.semCalls)
	}
}

// TestFindPrefersNameExactOverContent I01：昵称精确压过「发言里提到」。
func TestFindPrefersNameExactOverContent(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{
		namedProfile("1001", "弓长三益", "今天也在"),
		namedProfile("1002", "路人甲", "昨天弓长三益说了个笑话"),
	}}
	s := newTestSearcher(t, source, nil, SearchOptions{})

	got, err := s.Find(context.Background(), "弓长三益", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || len(got.Candidates) < 2 {
		t.Fatalf("应返回两条候选：%+v", got)
	}
	if got.Candidates[0].Profile.UID != "1001" || got.Candidates[0].Tier != TierName {
		t.Errorf("昵称精确应排第一：%+v", got.Candidates[0])
	}
	if got.Candidates[1].Profile.UID != "1002" || got.Candidates[1].Tier != TierContent {
		t.Errorf("提到名字的人应落在内容档：%+v", got.Candidates[1])
	}
}

// TestFindContentOnlyIsNotIdentity I01：只有内容命中时不能声称「这就是他本人」。
func TestFindContentOnlyIsNotIdentity(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{
		namedProfile("1002", "路人甲", "昨天弓长三益说了个笑话"),
	}}
	s := newTestSearcher(t, source, nil, SearchOptions{})

	got, err := s.Find(context.Background(), "弓长三益", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Tier != TierContent {
		t.Fatalf("内容命中的档位应可识别：%+v", got.Candidates)
	}
	if got.Candidates[0].Reason != "content" {
		t.Errorf("理由 = %q，期望 content", got.Candidates[0].Reason)
	}
}

// TestFindMatchesTypoByEditDistance I02/T11：不共享子串的错字靠编辑距离找到。
func TestFindMatchesTypoByEditDistance(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{namedProfile("3003", "今夜琴声温柔")}}
	s := newTestSearcher(t, source, nil, SearchOptions{})

	got, err := s.Find(context.Background(), "今夜琴身温柔", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || len(got.Candidates) != 1 {
		t.Fatalf("应命中一条近似候选：%+v", got)
	}
	if got.Candidates[0].Profile.UID != "3003" || got.Candidates[0].Tier != TierNear {
		t.Errorf("近似档位不符：%+v", got.Candidates[0])
	}
	if got.Candidates[0].Reason != "near_name" {
		t.Errorf("理由 = %q，期望 near_name", got.Candidates[0].Reason)
	}
}

// TestFindAliasExact I02/T11：别名表命中，且不自动改写原始昵称。
func TestFindAliasExact(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{namedProfile("5005", "弓长三益")}}
	s := newTestSearcher(t, source, nil, SearchOptions{
		Aliases: []Alias{{Alias: "长弓三亿", UID: "5005"}},
	})

	got, err := s.Find(context.Background(), "长弓三亿", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || len(got.Candidates) != 1 {
		t.Fatalf("应命中一条别名候选：%+v", got)
	}
	c := got.Candidates[0]
	if c.Profile.UID != "5005" || c.Tier != TierAlias || c.Reason != "alias_exact" {
		t.Errorf("别名命中不符：%+v", c)
	}
	if c.Profile.Name != "弓长三益" {
		t.Errorf("别名不能覆盖原始昵称：%q", c.Profile.Name)
	}
}

// TestFindKeepsTwoHomophonesAmbiguous I02/T11：两个同音候选必须保留歧义，不合并。
func TestFindKeepsTwoHomophonesAmbiguous(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{
		namedProfile("7001", "琴声温柔"),
		namedProfile("7002", "琴身温柔"),
	}}
	s := newTestSearcher(t, source, nil, SearchOptions{})

	got, err := s.Find(context.Background(), "琴深温柔", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusAmbiguous {
		t.Fatalf("同档两条应判歧义：status=%s %+v", got.Status, got.Candidates)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("应保留两个候选：%+v", got.Candidates)
	}
	uids := map[string]bool{}
	for _, c := range got.Candidates {
		uids[c.Profile.UID] = true
	}
	if !uids["7001"] || !uids["7002"] {
		t.Errorf("候选不应被合并：%+v", got.Candidates)
	}
}

// TestFindRejectsDecorationWrappedName I01/T11：「X奇迹与你X 是谁」要认出本人。
func TestFindRejectsDecorationWrappedName(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{
		namedProfile("9009", "X奇迹与你X", "今天也在"),
	}}
	s := newTestSearcher(t, source, nil, SearchOptions{})

	got, err := s.Find(context.Background(), "X奇迹与你X 是谁", 3)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || len(got.Candidates) == 0 {
		t.Fatalf("应命中该观众：%+v", got)
	}
	if got.Candidates[0].Profile.UID != "9009" || got.Candidates[0].Tier != TierName {
		t.Errorf("装饰昵称应算精确命中：%+v", got.Candidates[0])
	}
}

// TestFindRejectsLowRelevanceSemantic I03/T13：无关语义候选必须被拒，允许 0 结果。
func TestFindRejectsLowRelevanceSemantic(t *testing.T) {
	source := &identitySource{
		pool: []store.ViewerProfile{},
		semantic: []store.ViewerProfileHit{
			{Profile: namedProfile("8001", "路人乙"), Similarity: 0.35},
		},
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(2)}}
	s := newTestSearcher(t, source, embedder, SearchOptions{Alpha: 0.5})

	got, err := s.Find(context.Background(), "随便一个陌生人名", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusNotFound || len(got.Candidates) != 0 {
		t.Fatalf("低相关语义应被丢弃：status=%s %+v", got.Status, got.Candidates)
	}
}

// TestFindSemanticUnavailableIsNotNotFound I03：查询失败不能伪装成「没有这个人」。
func TestFindSemanticUnavailableIsNotNotFound(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{}}
	embedder := &fakeQueryEmbedder{err: errors.New("embedding 服务挂了")}
	s := newTestSearcher(t, source, embedder, SearchOptions{Alpha: 0.5})

	got, err := s.Find(context.Background(), "弓长三益", 5)
	if err != nil {
		t.Fatalf("降级不应报错：%v", err)
	}
	if got.Status != StatusUnavailable || !got.Degraded {
		t.Fatalf("应为 unavailable：%+v", got)
	}
}

// TestFindDegradesToLexical I03：语义挂了但词法有命中，仍返回 found + 降级标记。
func TestFindDegradesToLexical(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{namedProfile("1001", "弓长三益")}}
	embedder := &fakeQueryEmbedder{err: errors.New("embedding 服务挂了")}
	s := newTestSearcher(t, source, embedder, SearchOptions{Alpha: 0.5})

	got, err := s.Find(context.Background(), "弓长三益", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || !got.Degraded || len(got.Candidates) == 0 {
		t.Fatalf("应降级为词法 found：%+v", got)
	}
}

// TestFindAcceptsHighRelevanceSemantic I03：过阈值的语义候选仍可用（作为最低档）。
func TestFindAcceptsHighRelevanceSemantic(t *testing.T) {
	source := &identitySource{
		pool: []store.ViewerProfile{},
		semantic: []store.ViewerProfileHit{
			{Profile: namedProfile("8100", "爱要白饭的人"), Similarity: 0.82},
		},
	}
	embedder := &fakeQueryEmbedder{vectors: [][]float64{vectorWith(3)}}
	s := newTestSearcher(t, source, embedder, SearchOptions{Alpha: 0.5})

	got, err := s.Find(context.Background(), "那个总说要白饭的家伙", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if got.Status != StatusFound || len(got.Candidates) != 1 {
		t.Fatalf("高相关语义应保留：%+v", got)
	}
	if got.Candidates[0].Tier != TierSemantic || got.Candidates[0].Reason != "semantic" {
		t.Errorf("语义候选档位不符：%+v", got.Candidates[0])
	}
}

// TestSearchCompatibilityKeepsHits Search 仍返回旧结构的命中列表。
func TestSearchCompatibilityKeepsHits(t *testing.T) {
	source := &identitySource{pool: []store.ViewerProfile{namedProfile("1001", "弓长三益")}}
	s := newTestSearcher(t, source, nil, SearchOptions{})

	hits, err := s.Search(context.Background(), "弓长三益", 5)
	if err != nil {
		t.Fatalf("检索失败：%v", err)
	}
	if len(hits) != 1 || hits[0].Profile.UID != "1001" {
		t.Fatalf("兼容层结果不符：%+v", hits)
	}
	if hits[0].Score != 1 {
		t.Errorf("词法命中基础分应为 1：%v", hits[0].Score)
	}
}
