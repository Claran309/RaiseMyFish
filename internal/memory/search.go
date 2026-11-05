package memory

// 本文件实现带查询词的内存召回：关键词档位 + α * 向量余弦相似度。
//
// 边界：
//   - 隐私与范围过滤仍由 repository 的 SQL 负责，本层只做排序与降级；
//   - embedding 不可用时退回纯关键词，不把错误抛给调用方（可用性优先）；
//   - 空查询词直接复用 Recall（最近消息 + 摘要），保持既有行为。

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
)

const (
	// defaultSearchLimit 是 memory_search 未指定条数时的默认值。
	defaultSearchLimit = 10
	// maxSearchLimit 与 repository 的召回上限保持一致。
	maxSearchLimit = 100
	// semanticCandidateLimit 是向量候选数；最终仍按 limit 截断。
	semanticCandidateLimit = 20
	// maxSemanticQueryBytes 限制送进 embedding 的查询长度，超长只走关键词。
	maxSemanticQueryBytes = 4 << 10
)

// SearchQuery 是一次带文本的记忆检索请求。
//
// Scope 必须来自可信调用方（当前事件），不能取自模型输出或工具参数。
type SearchQuery struct {
	Scope Scope
	Text  string
	Limit int
}

// SemanticRecord 是一条向量命中的记忆摘要与余弦相似度。
type SemanticRecord struct {
	Record     Record
	Similarity float64
}

// SearchRecordSource 是混合检索需要的最小查询能力，由 database 层实现。
//
// 两个方法都必须在 SQL 层完成有效期、公开/私密范围过滤。
type SearchRecordSource interface {
	KeywordRecords(ctx context.Context, scope Scope, keyword string, limit int) ([]Record, error)
	SemanticRecords(ctx context.Context, scope Scope, vector []float64, limit int) ([]SemanticRecord, error)
}

// Embedder 由 OpenAI 兼容客户端实现；测试可注入 mock。
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float64, error)
}

// HybridSearchOptions 控制语义权重与降级回调。
type HybridSearchOptions struct {
	Alpha           float64
	OnSemanticError func(error)
}

// HybridSearcher 在既有 Reader 上叠加带查询词的混合检索。
//
// 它同时实现 Reader：Recall/Shared 原样委托，服务端其余召回路径不受影响。
type HybridSearcher struct {
	base            Reader
	source          SearchRecordSource
	embedder        Embedder
	alpha           float64
	onSemanticError func(error)
}

var _ Reader = (*HybridSearcher)(nil)

// NewHybridSearcher 构造混合检索；embedder 允许为 nil（纯关键词）。
func NewHybridSearcher(base Reader, source SearchRecordSource, embedder Embedder, opts HybridSearchOptions) (*HybridSearcher, error) {
	if base == nil || source == nil {
		return nil, errors.New("混合检索依赖不能为空")
	}
	if math.IsNaN(opts.Alpha) || opts.Alpha < 0 || opts.Alpha > 1 {
		return nil, errors.New("混合检索alpha必须在0至1之间")
	}
	return &HybridSearcher{
		base:            base,
		source:          source,
		embedder:        embedder,
		alpha:           opts.Alpha,
		onSemanticError: opts.OnSemanticError,
	}, nil
}

// Recall 保持既有召回口径（最近消息 + 摘要 + 跨通道）。
func (h *HybridSearcher) Recall(ctx context.Context, scope Scope) (Context, error) {
	return h.base.Recall(ctx, scope)
}

// Shared 原样委托共享事实查询。
func (h *HybridSearcher) Shared(ctx context.Context, scope Scope) (SharedState, error) {
	return h.base.Shared(ctx, scope)
}

// Search 执行混合检索；任一步语义失败都降级为纯关键词且不返回错误。
func (h *HybridSearcher) Search(ctx context.Context, query SearchQuery) (Context, error) {
	if ctx == nil {
		return Context{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	if err := ValidateScope(query.Scope); err != nil {
		return Context{}, err
	}
	text := strings.TrimSpace(query.Text)
	// 空查询等同「没有具体问题」：复用既有最近召回，不额外消耗 embedding。
	if text == "" {
		return h.base.Recall(ctx, query.Scope)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	keywordRecords, err := h.source.KeywordRecords(ctx, query.Scope, text, limit)
	if err != nil {
		return Context{}, err
	}
	if h.embedder == nil || h.alpha == 0 || len(query.Text) > maxSemanticQueryBytes {
		result := Context{Scope: query.Scope, Records: sortSearchRecords(keywordRecords, nil, 0, limit)}
		if err := result.Validate(); err != nil {
			return Context{}, errors.New("关键词记忆召回数据无效")
		}
		return result, nil
	}

	vector, err := h.embedQuery(ctx, text)
	if err != nil {
		if ctx.Err() != nil {
			return Context{}, ctx.Err()
		}
		h.degrade(err)
		result := Context{Scope: query.Scope, Records: sortSearchRecords(keywordRecords, nil, 0, limit)}
		if err := result.Validate(); err != nil {
			return Context{}, errors.New("关键词记忆召回数据无效")
		}
		return result, nil
	}
	semantic, err := h.source.SemanticRecords(ctx, query.Scope, vector, semanticCandidateLimit)
	if err != nil {
		if ctx.Err() != nil {
			return Context{}, ctx.Err()
		}
		h.degrade(err)
		result := Context{Scope: query.Scope, Records: sortSearchRecords(keywordRecords, nil, 0, limit)}
		if err := result.Validate(); err != nil {
			return Context{}, errors.New("关键词记忆召回数据无效")
		}
		return result, nil
	}

	result := Context{Scope: query.Scope, Records: sortSearchRecords(keywordRecords, semantic, h.alpha, limit)}
	if err := result.Validate(); err != nil {
		return Context{}, errors.New("混合记忆召回数据无效")
	}
	return result, nil
}

func (h *HybridSearcher) embedQuery(ctx context.Context, text string) ([]float64, error) {
	vectors, err := h.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, errors.New("查询embedding向量数不符")
	}
	return vectors[0], nil
}

func (h *HybridSearcher) degrade(err error) {
	if h.onSemanticError != nil {
		h.onSemanticError(err)
	}
}

// searchCandidate 汇总关键词与语义两路命中，score = 关键词档位 + alpha * sim。
type searchCandidate struct {
	record  Record
	keyword bool
	sim     float64
	score   float64
}

// sortSearchRecords 合并两路候选并排序；semantic 为 nil 表示纯关键词。
func sortSearchRecords(keyword []Record, semantic []SemanticRecord, alpha float64, limit int) []Record {
	candidates := make(map[string]*searchCandidate, len(keyword)+len(semantic))
	for _, record := range keyword {
		if strings.TrimSpace(record.ID) == "" {
			continue
		}
		c := candidates[record.ID]
		if c == nil {
			c = &searchCandidate{record: record}
			candidates[record.ID] = c
		}
		c.keyword = true
	}
	for _, hit := range semantic {
		if strings.TrimSpace(hit.Record.ID) == "" || math.IsNaN(hit.Similarity) || math.IsInf(hit.Similarity, 0) {
			continue
		}
		c := candidates[hit.Record.ID]
		if c == nil {
			c = &searchCandidate{record: hit.Record}
			candidates[hit.Record.ID] = c
		}
		// 同一 ID 只保留最高相似度，避免重复候选稀释排序。
		if hit.Similarity > c.sim {
			c.sim = hit.Similarity
		}
	}

	merged := make([]*searchCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.keyword {
			c.score += 1
		}
		c.score += alpha * c.sim
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
		if a.record.Importance != b.record.Importance {
			return a.record.Importance > b.record.Importance
		}
		if !a.record.ValidFrom.Equal(b.record.ValidFrom) {
			return a.record.ValidFrom.After(b.record.ValidFrom)
		}
		return a.record.ID < b.record.ID
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	records := make([]Record, 0, len(merged))
	for _, c := range merged {
		records = append(records, c.record)
	}
	return records
}
