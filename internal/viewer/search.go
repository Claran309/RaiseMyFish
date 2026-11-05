package viewer

// 本文件实现观众档案的混合检索：关键词 + α * 向量余弦相似度。
//
// 典型用途：ReAct / fishctl 输入「那个爱要白饭的人」，
// 先按昵称与最近发言做关键词匹配，再叠语义候选，返回对应 uid 档案。
//
// 边界：
//   - 档案不含私密会话字段，查询不需要 Scope；
//   - embedding 不可用时退回纯关键词，不把错误抛给调用方；
//   - 关键词/语义的 SQL 过滤都在 database 层完成。

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"RaiseMyFish/internal/store"
)

const (
	defaultSearchLimit = 5
	maxSearchLimit     = 50
	// semanticCandidateLimit 是向量候选数；最终仍按 limit 截断。
	semanticCandidateLimit = 20
	// maxSearchQueryRunes 防止把超长文本送进 embedding 服务。
	maxSearchQueryRunes = 200
)

// Embedder 由 OpenAI 兼容客户端实现；测试可注入 mock。
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float64, error)
}

// ProfileSource 是检索需要的最小查询能力，由 database.ViewerRepo 实现。
type ProfileSource interface {
	KeywordProfiles(ctx context.Context, keyword string, limit int) ([]store.ViewerProfileHit, error)
	SemanticProfiles(ctx context.Context, vector []float64, limit int) ([]store.ViewerProfileHit, error)
}

// SearchOptions 控制语义权重与降级回调。
type SearchOptions struct {
	Alpha           float64
	OnSemanticError func(error)
}

// Searcher 组合两路档案查询。
type Searcher struct {
	source          ProfileSource
	embedder        Embedder
	alpha           float64
	onSemanticError func(error)
}

// NewSearcher 构造档案检索；embedder 允许为 nil（纯关键词）。
func NewSearcher(source ProfileSource, embedder Embedder, opts SearchOptions) (*Searcher, error) {
	if source == nil {
		return nil, errors.New("档案检索依赖不能为空")
	}
	if math.IsNaN(opts.Alpha) || opts.Alpha < 0 || opts.Alpha > 1 {
		return nil, errors.New("档案检索alpha必须在0至1之间")
	}
	return &Searcher{source: source, embedder: embedder, alpha: opts.Alpha, onSemanticError: opts.OnSemanticError}, nil
}

// Search 执行混合检索；语义失败时只回关键词且不返回错误。
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]store.ViewerProfileHit, error) {
	if ctx == nil {
		return nil, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(query)
	if text == "" {
		return nil, errors.New("档案检索查询词不能为空")
	}
	if utf8.RuneCountInString(text) > maxSearchQueryRunes {
		return nil, errors.New("档案检索查询词不能超过200字")
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	keyword, err := s.keywordHits(ctx, text, limit)
	if err != nil {
		return nil, err
	}
	if s.embedder == nil || s.alpha == 0 {
		return sortHits(keyword, nil, 0, limit), nil
	}
	vector, err := s.embedQuery(ctx, text)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.degrade(err)
		return sortHits(keyword, nil, 0, limit), nil
	}
	semantic, err := s.source.SemanticProfiles(ctx, vector, semanticCandidateLimit)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.degrade(err)
		return sortHits(keyword, nil, 0, limit), nil
	}
	return sortHits(keyword, semantic, s.alpha, limit), nil
}

// keywordHits 先用原句查，查不到再剥掉「是谁/吗/那个」这类问句外壳。
//
// 观众问「弓长三益是谁」时昵称本身是「弓长三益」，整句 ILIKE 永远命不中。
func (s *Searcher) keywordHits(ctx context.Context, text string, limit int) ([]store.ViewerProfileHit, error) {
	for _, candidate := range keywordCandidates(text) {
		hits, err := s.source.KeywordProfiles(ctx, candidate, limit)
		if err != nil {
			return nil, err
		}
		if len(hits) > 0 {
			return hits, nil
		}
	}
	return nil, nil
}

// keywordCandidates 返回由精确到宽松的去重关键词（原句优先）。
func keywordCandidates(text string) []string {
	seen := make(map[string]bool, 2)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(text)
	add(stripQuestionShell(text))
	return out
}

// stripQuestionShell 去掉问句的前后缀；只作为原句无命中时的兜底。
func stripQuestionShell(text string) string {
	s := strings.TrimSpace(text)
	for {
		before := s
		s = strings.Trim(s, " \t\r\n？?！!。.，,、～~：:\"'“”‘’")
		for _, suffix := range []string{"是谁呀", "是谁啊", "是谁呢", "是哪位", "是哪个", "是谁", "的人", "吗", "呢"} {
			if strings.HasSuffix(s, suffix) && utf8.RuneCountInString(s) > utf8.RuneCountInString(suffix) {
				s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
				break
			}
		}
		for _, prefix := range []string{"请问", "你知道", "你还记得", "还记得", "那个"} {
			if strings.HasPrefix(s, prefix) && utf8.RuneCountInString(s) > utf8.RuneCountInString(prefix) {
				s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
				break
			}
		}
		if s == before {
			return s
		}
	}
}

func (s *Searcher) embedQuery(ctx context.Context, text string) ([]float64, error) {
	vectors, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, errors.New("查询embedding向量数不符")
	}
	return vectors[0], nil
}

func (s *Searcher) degrade(err error) {
	if s.onSemanticError != nil {
		s.onSemanticError(err)
	}
}

type candidate struct {
	hit     store.ViewerProfileHit
	keyword bool
	sim     float64
	score   float64
}

// sortHits 合并两路候选：score = 关键词档位 + alpha * sim。
func sortHits(keyword, semantic []store.ViewerProfileHit, alpha float64, limit int) []store.ViewerProfileHit {
	candidates := make(map[string]*candidate, len(keyword)+len(semantic))
	for _, hit := range keyword {
		if strings.TrimSpace(hit.Profile.UID) == "" {
			continue
		}
		c := candidates[hit.Profile.UID]
		if c == nil {
			c = &candidate{hit: hit}
			candidates[hit.Profile.UID] = c
		}
		c.keyword = true
	}
	for _, hit := range semantic {
		if strings.TrimSpace(hit.Profile.UID) == "" || math.IsNaN(hit.Similarity) || math.IsInf(hit.Similarity, 0) {
			continue
		}
		c := candidates[hit.Profile.UID]
		if c == nil {
			c = &candidate{hit: hit}
			candidates[hit.Profile.UID] = c
		}
		if hit.Similarity > c.sim {
			c.sim = hit.Similarity
			c.hit.Profile = hit.Profile
		}
	}

	merged := make([]*candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.keyword {
			c.score += 1
		}
		c.score += alpha * c.sim
		c.hit.Similarity = c.sim
		c.hit.Score = c.score
		merged = append(merged, c)
	}
	sort.Slice(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.keyword != b.keyword {
			return a.keyword
		}
		if a.sim != b.sim {
			return a.sim > b.sim
		}
		if a.hit.Profile.Hits != b.hit.Profile.Hits {
			return a.hit.Profile.Hits > b.hit.Profile.Hits
		}
		if !a.hit.Profile.LastSeen.Equal(b.hit.Profile.LastSeen) {
			return a.hit.Profile.LastSeen.After(b.hit.Profile.LastSeen)
		}
		return a.hit.Profile.UID < b.hit.Profile.UID
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	hits := make([]store.ViewerProfileHit, 0, len(merged))
	for _, c := range merged {
		hits = append(hits, c.hit)
	}
	return hits
}
