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
	"time"

	"RaiseMyFish/internal/llmmeta"
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
	// defaultMinSimilarity 是语义召回的最低余弦相似度。
	// 「相对最近」不等于「相关」：无关内容的 top1 不能因为排第一就被塞进上下文。
	defaultMinSimilarity = 0.6
)

// SearchQuery 是一次带文本的记忆检索请求。
//
// Scope 必须来自可信调用方（当前事件），不能取自模型输出或工具参数。
type SearchQuery struct {
	Scope Scope
	Text  string
	Limit int
	// After/Before 是按事实/事件发生时间过滤的半开区间 [After, Before)；
	// 零值表示该侧不限。有时间窗时不走向量路：向量索引没有可靠的事实时间过滤。
	After  time.Time
	Before time.Time
}

// hasTimeWindow 表示查询带了时间范围。
func (q SearchQuery) hasTimeWindow() bool {
	return !q.After.IsZero() || !q.Before.IsZero()
}

// validateWindow 校验半开区间；两端都可缺省，但都给出时起点必须早于终点。
func (q SearchQuery) validateWindow() error {
	if !q.After.IsZero() && !q.Before.IsZero() && !q.After.Before(q.Before) {
		return errors.New("记忆时间窗起点必须早于终点")
	}
	return nil
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
	// SemanticRecords 只返回「索引指纹匹配且源 hash 未过期」的向量命中；
	// 查询与入库必须使用同一个指纹，否则旧向量空间的条目不会参与排序。
	SemanticRecords(ctx context.Context, scope Scope, vector []float64, fingerprint string, limit int) ([]SemanticRecord, error)
}

// SearchMessageSource 是可选的原始消息检索能力，由 database.MemoryRepo 实现。
//
// 摘要可能还没提炼出来（memory_records 为空），但 events 里已有原始弹幕；
// 没有这一路会出现「数据明明在、鱼却说不知道」。实现方必须在 SQL 层做范围过滤。
type SearchMessageSource interface {
	KeywordMessages(ctx context.Context, scope Scope, keyword string, limit int) ([]Message, error)
}

// TimeSearchRecordSource 是可选的「关键词 + 时间窗」摘要召回能力。
//
// keyword 允许为空：只看某个时间窗内发生过什么也应当能召回。
// 实现方必须在 SQL 层按 valid_from 过滤，并保持与 KeywordRecords 相同的可见性口径。
type TimeSearchRecordSource interface {
	KeywordRecordsBetween(ctx context.Context, scope Scope, keyword string, after, before time.Time, limit int) ([]Record, error)
}

// TimeSearchMessageSource 是可选的「关键词 + 时间窗」原始消息召回能力。
//
// 实现方必须在 SQL 层按 events.at 过滤，并保持与 KeywordMessages 相同的可见性口径。
type TimeSearchMessageSource interface {
	KeywordMessagesBetween(ctx context.Context, scope Scope, keyword string, after, before time.Time, limit int) ([]Message, error)
}

// Embedder 由 OpenAI 兼容客户端实现；测试可注入 mock。
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float64, error)
}

// HybridSearchOptions 控制语义权重、相关性下限与降级回调。
type HybridSearchOptions struct {
	Alpha           float64
	OnSemanticError func(error)
	// MinSimilarity 是语义召回的最低相关性；<=0 用默认值。
	MinSimilarity float64
	// Fingerprint 是当前向量空间指纹；embedder 非 nil 时必填。
	// 空指纹意味着无法确认查询向量与库内向量同源，宁可拒绝装配也不静默混用。
	Fingerprint string
}

// HybridSearcher 在既有 Reader 上叠加带查询词的混合检索。
//
// 它同时实现 Reader：Recall/Shared 原样委托，服务端其余召回路径不受影响。
type HybridSearcher struct {
	base            Reader
	source          SearchRecordSource
	embedder        Embedder
	alpha           float64
	minSimilarity   float64
	fingerprint     string
	onSemanticError func(error)
}

var (
	_ Reader      = (*HybridSearcher)(nil)
	_ QueryReader = (*HybridSearcher)(nil)
	_ FocusReader = (*HybridSearcher)(nil)
)

// NewHybridSearcher 构造混合检索；embedder 允许为 nil（纯关键词）。
func NewHybridSearcher(base Reader, source SearchRecordSource, embedder Embedder, opts HybridSearchOptions) (*HybridSearcher, error) {
	if base == nil || source == nil {
		return nil, errors.New("混合检索依赖不能为空")
	}
	if math.IsNaN(opts.Alpha) || opts.Alpha < 0 || opts.Alpha > 1 {
		return nil, errors.New("混合检索alpha必须在0至1之间")
	}
	minSim := opts.MinSimilarity
	if minSim <= 0 {
		minSim = defaultMinSimilarity
	}
	if math.IsNaN(minSim) || minSim > 1 {
		return nil, errors.New("混合检索最低相关性必须在0至1之间")
	}
	fingerprint := strings.TrimSpace(opts.Fingerprint)
	if embedder != nil && fingerprint == "" {
		return nil, errors.New("混合检索缺少索引指纹：无法确认向量空间")
	}
	return &HybridSearcher{
		base:            base,
		source:          source,
		embedder:        embedder,
		alpha:           opts.Alpha,
		minSimilarity:   minSim,
		fingerprint:     fingerprint,
		onSemanticError: opts.OnSemanticError,
	}, nil
}

// Recall 保持既有召回口径（最近消息 + 摘要 + 跨通道）。
func (h *HybridSearcher) Recall(ctx context.Context, scope Scope) (Context, error) {
	return h.base.Recall(ctx, scope)
}

// RecallWithQuery 把问题文本转发给底层召回；底层不支持时退回 Recall。
func (h *HybridSearcher) RecallWithQuery(ctx context.Context, scope Scope, query string) (Context, error) {
	if reader, ok := h.base.(QueryReader); ok {
		return reader.RecallWithQuery(ctx, scope, query)
	}
	return h.base.Recall(ctx, scope)
}

// Focus 原样转发焦点轮次查询；底层不支持时返回空，不影响混合检索。
func (h *HybridSearcher) Focus(ctx context.Context, query FocusQuery) ([]Message, error) {
	if reader, ok := h.base.(FocusReader); ok {
		return reader.Focus(ctx, query)
	}
	return nil, nil
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
	if err := query.validateWindow(); err != nil {
		return Context{}, err
	}
	text := strings.TrimSpace(query.Text)
	// 空查询且没有时间窗，等同「没有具体问题」：复用既有最近召回，不额外消耗 embedding。
	if text == "" && !query.hasTimeWindow() {
		return h.base.Recall(ctx, query.Scope)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	// 时间窗只走关键词/原文两路：向量命中没有可靠的事实时间过滤，
	// 混进来会把窗外内容塞进「昨天」的回答里。
	if query.hasTimeWindow() {
		return h.searchWindow(ctx, query, text, limit)
	}

	keywordRecords, err := h.source.KeywordRecords(ctx, query.Scope, text, limit)
	if err != nil {
		return Context{}, err
	}
	// 原始弹幕是摘要的兜底：提炼还没跑过时，events 仍然能被关键词搜到。
	messages, err := h.keywordMessages(ctx, query.Scope, text, limit)
	if err != nil {
		return Context{}, err
	}
	if h.embedder == nil || h.alpha == 0 || len(query.Text) > maxSemanticQueryBytes {
		return buildSearchContext(query.Scope, messages, keywordRecords, nil, 0, limit, "关键词记忆召回数据无效")
	}

	vector, err := h.embedQuery(ctx, text)
	if err != nil {
		if ctx.Err() != nil {
			return Context{}, ctx.Err()
		}
		h.degrade(err)
		return buildSearchContext(query.Scope, messages, keywordRecords, nil, 0, limit, "关键词记忆召回数据无效")
	}
	semantic, err := h.source.SemanticRecords(ctx, query.Scope, vector, h.fingerprint, semanticCandidateLimit)
	if err != nil {
		if ctx.Err() != nil {
			return Context{}, ctx.Err()
		}
		h.degrade(err)
		return buildSearchContext(query.Scope, messages, keywordRecords, nil, 0, limit, "关键词记忆召回数据无效")
	}

	semantic = filterSemanticRecords(semantic, h.minSimilarity)
	return buildSearchContext(query.Scope, messages, keywordRecords, semantic, h.alpha, limit, "混合记忆召回数据无效")
}

// searchWindow 按「关键词 + 时间窗」召回摘要与原始消息；关键词可以为空。
//
// 时间窗检索依赖可选的 TimeSearch*Source；未实现时明确报错，
// 避免把不带时间过滤的旧结果伪装成「昨天」。
func (h *HybridSearcher) searchWindow(ctx context.Context, query SearchQuery, text string, limit int) (Context, error) {
	recordSource, ok := h.source.(TimeSearchRecordSource)
	if !ok {
		return Context{}, errors.New("记忆源不支持时间窗检索")
	}
	records, err := recordSource.KeywordRecordsBetween(ctx, query.Scope, text, query.After, query.Before, limit)
	if err != nil {
		return Context{}, err
	}
	var messages []Message
	if source, ok := h.source.(TimeSearchMessageSource); ok {
		messages, err = source.KeywordMessagesBetween(ctx, query.Scope, text, query.After, query.Before, limit)
		if err != nil {
			return Context{}, err
		}
	}
	return buildSearchContext(query.Scope, messages, records, nil, 0, limit, "时间窗记忆召回数据无效")
}

// filterSemanticRecords 丢掉低于相关性下限的向量命中。
//
// 允许结果为空：查不到相关内容时，宁可不注入，也不拿「最相近的无关条目」凑数。
func filterSemanticRecords(records []SemanticRecord, minSimilarity float64) []SemanticRecord {
	filtered := make([]SemanticRecord, 0, len(records))
	for _, record := range records {
		if record.Similarity < minSimilarity {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

// keywordMessages 查询原始消息；来源不支持时返回 nil，不影响摘要检索。
func (h *HybridSearcher) keywordMessages(ctx context.Context, scope Scope, keyword string, limit int) ([]Message, error) {
	source, ok := h.source.(SearchMessageSource)
	if !ok {
		return nil, nil
	}
	return source.KeywordMessages(ctx, scope, keyword, limit)
}

// buildSearchContext 组装并二次校验检索结果。
func buildSearchContext(scope Scope, messages []Message, keyword []Record, semantic []SemanticRecord, alpha float64, limit int, invalid string) (Context, error) {
	result := Context{Scope: scope, Recent: messages, Records: sortSearchRecords(keyword, semantic, alpha, limit)}
	if err := result.Validate(); err != nil {
		return Context{}, errors.New(invalid)
	}
	return result, nil
}

func (h *HybridSearcher) embedQuery(ctx context.Context, text string) ([]float64, error) {
	// 账本用途：查询向量化；关联标识只写索引名。
	ctx = llmmeta.WithCall(ctx, llmmeta.Call{Purpose: llmmeta.PurposeEmbedQuery, CorrelationID: "memory"})
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
