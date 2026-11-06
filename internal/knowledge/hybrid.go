package knowledge

// 混合召回：关键词档位 + α * 向量余弦相似度。
// embedding 或 pgvector 不可用时只回退关键词，不把错误抛给 Brain。

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
)

const (
	// 派发单固定语义候选 20 条；范围过滤在合并阶段做，越权条目不会返回。
	semanticCandidateLimit = 20
	// 超长查询不做语义检索，避免把数 MB 文本发给 embedding 服务。
	maxSemanticQueryBytes = 4 << 10
	// defaultMinSimilarity 是语义命中的最低余弦相似度。
	// 「相对最近」不等于「相关」：随机人名/无关歌词的 top1 也必须被拒。
	defaultMinSimilarity = 0.6
)

// HybridOptions 控制语义权重、相关性下限与降级回调。
type HybridOptions struct {
	Alpha           float64
	OnSemanticError func(error)
	// MinSimilarity 是语义命中的最低相关性；<=0 用默认值。
	MinSimilarity float64
}

// HybridReader 在 FileReader 的关键词召回上叠加语义候选。
type HybridReader struct {
	base            *FileReader
	embedder        Embedder
	store           EmbeddingStore
	alpha           float64
	minSimilarity   float64
	onSemanticError func(error)
}

func NewHybridReader(base *FileReader, embedder Embedder, store EmbeddingStore, opts HybridOptions) (*HybridReader, error) {
	if base == nil || embedder == nil || store == nil {
		return nil, errors.New("混合召回依赖不能为空")
	}
	if math.IsNaN(opts.Alpha) || opts.Alpha < 0 || opts.Alpha > 1 {
		return nil, errors.New("混合召回alpha必须在0至1之间")
	}
	minSim := opts.MinSimilarity
	if minSim <= 0 {
		minSim = defaultMinSimilarity
	}
	if math.IsNaN(minSim) || minSim > 1 {
		return nil, errors.New("混合召回最低相关性必须在0至1之间")
	}
	return &HybridReader{base: base, embedder: embedder, store: store, alpha: opts.Alpha, minSimilarity: minSim, onSemanticError: opts.OnSemanticError}, nil
}

// Recall 关键词与语义同时可用时按混合分排序；任一步失败都降级为纯关键词。
func (h *HybridReader) Recall(ctx context.Context, query Query) (Context, error) {
	text, st, err := h.base.prepare(ctx, query)
	if err != nil {
		return Context{}, err
	}
	if !hasWord(text) {
		return Context{Scope: query.Scope, Revision: st.revision}, nil
	}
	keywordHits, err := h.base.keywordHits(ctx, text, st, query.Scope)
	if err != nil {
		return Context{}, err
	}
	if err := checkContext(ctx); err != nil {
		return Context{}, err
	}
	// alpha=0 等价纯关键词，不调用 embedding，保证与 FileReader 的排序逐条一致。
	if h.alpha == 0 || len(strings.TrimSpace(query.Text)) == 0 || len(query.Text) > maxSemanticQueryBytes {
		sortHits(keywordHits)
		return contextFromHits(query.Scope, st.revision, keywordHits), nil
	}
	semantic, err := h.semanticHits(ctx, query.Text)
	if err != nil {
		if ctx.Err() != nil {
			return Context{}, ctx.Err()
		}
		if h.onSemanticError != nil {
			h.onSemanticError(err)
		}
		sortHits(keywordHits)
		return contextFromHits(query.Scope, st.revision, keywordHits), nil
	}
	if err := checkContext(ctx); err != nil {
		return Context{}, err
	}
	return h.merge(query.Scope, st.revision, st, keywordHits, semantic), nil
}

func (h *HybridReader) semanticHits(ctx context.Context, query string) (map[string]float64, error) {
	vectors, err := h.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, errors.New("查询embedding向量数不符")
	}
	hits, err := h.store.SemanticSearch(ctx, vectors[0], semanticCandidateLimit)
	if err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(hits))
	for _, hit := range hits {
		if hit.ID == "" || math.IsNaN(hit.Similarity) || math.IsInf(hit.Similarity, 0) {
			continue
		}
		// 相关性拒绝：低于下限的向量命中不算「查到了这个知识点」。
		if hit.Similarity < h.minSimilarity {
			continue
		}
		if old, ok := result[hit.ID]; !ok || hit.Similarity > old {
			result[hit.ID] = hit.Similarity
		}
	}
	return result, nil
}

type hybridCandidate struct {
	entry   entry
	tier    int
	longest int
	count   int
	sim     float64
	score   float64
}

func (h *HybridReader) merge(scope Scope, revision string, st *snapshotState, keywordHits []hit, semantic map[string]float64) Context {
	scoped := make(map[string]entry, len(st.entries))
	for _, e := range st.entries {
		if inScope(e.item, scope) {
			scoped[e.item.ID] = e
		}
	}
	candidates := make(map[string]*hybridCandidate, len(keywordHits)+len(semantic))
	for _, hit := range keywordHits {
		candidates[hit.entry.item.ID] = &hybridCandidate{
			entry:   hit.entry,
			tier:    hit.score.tier,
			longest: hit.score.longest,
			count:   hit.score.count,
		}
	}
	for id, similarity := range semantic {
		e, ok := scoped[id]
		if !ok {
			continue // 快照里没有或越权的向量不参与召回
		}
		candidate, exists := candidates[id]
		if !exists {
			candidate = &hybridCandidate{entry: e}
			candidates[id] = candidate
		}
		candidate.sim = similarity
	}
	merged := make([]*hybridCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.score = float64(candidate.tier) + h.alpha*candidate.sim
		merged = append(merged, candidate)
	}
	sort.Slice(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.tier != b.tier {
			return a.tier > b.tier
		}
		if a.sim != b.sim {
			return a.sim > b.sim
		}
		if a.longest != b.longest {
			return a.longest > b.longest
		}
		if a.count != b.count {
			return a.count > b.count
		}
		if a.entry.item.Priority != b.entry.item.Priority {
			return a.entry.item.Priority > b.entry.item.Priority
		}
		return a.entry.item.ID < b.entry.item.ID
	})
	result := Context{Scope: scope, Revision: revision}
	for i := 0; i < len(merged) && i < 3; i++ {
		result.Items = append(result.Items, cloneItem(merged[i].entry.item))
	}
	return result
}
