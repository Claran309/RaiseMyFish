package viewer

// 本文件实现观众档案的身份解析与混合检索。
//
// 检索的目标不是「找一段相似文本」，而是回答「这句话在说谁」：
//   - UID 精确命中最高（平台身份是唯一可信的锚点）；
//   - 当前昵称精确次之；
//   - 人工维护的别名再次（历史昵称/常见错字）；
//   - 编辑距离与拼音候选只用于「扩大候选」，不自动认定同一人；
//   - 发言内容命中最低（提到某人 ≠ 这个人就是某人）。
//
// 语义（向量）候选只作补充：必须过最低相关性，且永远排在词法身份命中之后。
// 低相关的语义 topK 会被直接丢弃——宁可返回「没找到」，也不用无关歌词/梗顶替。
//
// M10 起档案索引拆成三路（EmbeddingKind）：
//   - identity：昵称，用于认人（同音、改名、模糊昵称）；
//   - facts：本人发言，用于查「他说过什么 / 谁爱要白饭」；
//   - interaction：鱼的回复，只用于互动关系，**永不参与认人**。
// 否则鱼经常回「白饭」，所有被鱼回复过的人都会像「爱吃白饭的人」。
//
// 边界：
//   - 档案不含私密会话字段，查询不需要 Scope；
//   - embedding 不可用时退回纯词法，不把错误抛给调用方；
//   - 关键词/语义的 SQL 过滤都在 database 层完成，本包只做归因与排序。

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode"
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

	// defaultMinSimilarity 是语义候选的最低余弦相似度。
	// 无关文本（随机人名/歌词）通常落在 0.2~0.5，0.6 能挡住大部分噪音；
	// 阈值应由真实负例校准，不是「通用余弦真理」。
	defaultMinSimilarity = 0.6
	// defaultAmbiguityGap 是同档候选的分差阈值：分差小于它就不敢定人。
	defaultAmbiguityGap = 0.15
	// defaultNameScan 是全量昵称扫描的上限（按发言数排序的活跃档案）。
	defaultNameScan = 300
)

// identitySearchKinds 是认人允许使用的索引：昵称 + 本人事实。
// interaction 是鱼自己的话，永远不进入认人候选，否则鱼的口头禅会被算成观众特点。
var identitySearchKinds = []EmbeddingKind{KindIdentity, KindFacts}

// Tier 是命中理由的档位；数字越大越可信。
//
// 排序时档位优先于分数：「昵称精确」永远压过「发言里提过」，
// 这样「甲提过乙」不会被误当成「乙本人」。
type Tier int

const (
	// TierSemantic 只有向量相似度，没有任何词法身份理由。
	TierSemantic Tier = iota
	// TierContent 发言/回复正文里出现过查询词（是人提到，不是人本身）。
	TierContent
	// TierNear 编辑距离/拼音候选/去装饰后近似；只用于扩大候选。
	TierNear
	// TierAlias 别名表精确命中（历史昵称、常见错字等人工确认的写法）。
	TierAlias
	// TierName 当前昵称精确命中。
	TierName
	// TierUID 平台 UID 精确命中。
	TierUID
)

// String 给日志和工具输出一个人话档位名。
func (t Tier) String() string {
	switch t {
	case TierUID:
		return "uid"
	case TierName:
		return "name"
	case TierAlias:
		return "alias"
	case TierNear:
		return "near"
	case TierContent:
		return "content"
	default:
		return "semantic"
	}
}

// Status 是检索结果的状态。
//
// 调用方必须能区分「查不到这个人」和「这次查询失败」：
// 前者可以说「不认识」，后者只能说「暂时没查到」。
type Status string

const (
	// StatusFound 有高确定结果（第一个候选最可信）。
	StatusFound Status = "found"
	// StatusAmbiguous 有多个同档候选，不能替用户合并身份。
	StatusAmbiguous Status = "ambiguous"
	// StatusNotFound 确认没有匹配（可以回答「不认识」）。
	StatusNotFound Status = "not_found"
	// StatusUnavailable 查询能力不可用（不能伪装成「没有这个人」）。
	StatusUnavailable Status = "unavailable"
)

// Candidate 是一条带理由的档案候选。
type Candidate struct {
	store.ViewerProfileHit
	// Tier 是命中档位；Reason 是具体理由（uid_exact/name_exact/alias_exact/near_name/phonetic/content/semantic）。
	Tier   Tier
	Reason string
}

// Result 是一次身份解析的结果。
type Result struct {
	Status Status
	// Candidates 已按可信度排序，最多 limit 个；ambiguous 时最多 3 个。
	Candidates []Candidate
	// Reason 说明状态成因（给工具层与日志，不直接给模型当事实）。
	Reason string
	// Degraded 表示语义路径不可用、只走了词法。
	Degraded bool
}

// Embedder 由 OpenAI 兼容客户端实现；测试可注入 mock。
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float64, error)
}

// ProfileSource 是按索引用途查询档案的能力，由 database.ViewerRepo 实现。
//
// kind 决定查哪一路索引：identity=昵称、facts=本人发言、interaction=鱼的回复。
// 认人只允许查 identity/facts；interaction 留给互动查询，不能拿来认人。
type ProfileSource interface {
	KeywordProfiles(ctx context.Context, kind EmbeddingKind, keyword string, limit int) ([]store.ViewerProfileHit, error)
	// SemanticProfiles 只返回「该路指纹匹配且源 hash 未过期」的向量命中，
	// 避免换了模型或昵称/发言已更新后仍用旧向量排序。
	SemanticProfiles(ctx context.Context, kind EmbeddingKind, vector []float64, fingerprint string, limit int) ([]store.ViewerProfileHit, error)
}

// uidSource 是按 UID 精确取档案的可选能力（database.ViewerRepo 已实现）。
//
// 用类型断言而不是扩大 ProfileSource：其他实现（测试桩、MCP 只读源）不必为了
// UID 精确查询去实现一个用不到的方法。
type uidSource interface {
	GetProfile(ctx context.Context, uid string) (store.ViewerProfile, error)
}

// namePoolSource 是可选的「活跃档案池」，供音近/别名/拼音在全量昵称里找人。
//
// 只靠 ILIKE 无法命中「今夜琴身温柔」→「今夜琴声温柔」这种不共享子串的错字，
// 所以需要一份按发言数排序的昵称池做编辑距离比较。database.ViewerRepo.TopViewers 满足。
type namePoolSource interface {
	TopViewers(ctx context.Context, limit int) ([]store.ViewerProfile, error)
}

// Alias 与 AliasStatus 的定义见 aliases.go：别名必须带确认状态，
// 只有 confirmed 参与认人；rejected 作为负例抑制同一别名指向。

// SearchOptions 控制语义权重、阈值与身份扩展。
type SearchOptions struct {
	Alpha           float64
	OnSemanticError func(error)
	// MinSimilarity 是语义候选的最低相似度；<=0 用默认值 defaultMinSimilarity。
	MinSimilarity float64
	// AmbiguityGap 是同档候选的定人分差；<=0 用默认值 defaultAmbiguityGap。
	AmbiguityGap float64
	// Aliases 是静态人工别名表（可空）；AliasSource 非 nil 时以它为准。
	Aliases []Alias
	// Phonetic 是可选的静态拼音扩展钩子：输入一个词，返回同音候选词。
	// AliasSource 非 nil 时以它的同音快照为准；两者都没有时只用编辑距离与子串近似。
	Phonetic func(string) []string
	// AliasSource 提供可热更新的别名/同音快照（生产传 AliasFile）。
	AliasSource AliasSource
	// MaxNameScan 是昵称池扫描上限；<=0 用默认值 defaultNameScan。
	MaxNameScan int
	// Fingerprint 是当前向量空间指纹；embedder 非 nil 时必填。
	Fingerprint string
}

// Searcher 组合词法身份解析与语义补充。
type Searcher struct {
	source          ProfileSource
	embedder        Embedder
	alpha           float64
	onSemanticError func(error)
	minSimilarity   float64
	ambiguityGap    float64
	fingerprint     string
	aliases         []Alias
	phonetic        func(string) []string
	aliasSource     AliasSource
	maxNameScan     int
}

// NewSearcher 构造档案检索；embedder 允许为 nil（纯关键词）。
func NewSearcher(source ProfileSource, embedder Embedder, opts SearchOptions) (*Searcher, error) {
	if source == nil {
		return nil, errors.New("档案检索依赖不能为空")
	}
	if math.IsNaN(opts.Alpha) || opts.Alpha < 0 || opts.Alpha > 1 {
		return nil, errors.New("档案检索alpha必须在0至1之间")
	}
	minSim := opts.MinSimilarity
	if minSim <= 0 {
		minSim = defaultMinSimilarity
	}
	if math.IsNaN(minSim) || minSim > 1 {
		return nil, errors.New("档案检索最低相关性必须在0至1之间")
	}
	gap := opts.AmbiguityGap
	if gap <= 0 {
		gap = defaultAmbiguityGap
	}
	scan := opts.MaxNameScan
	if scan <= 0 {
		scan = defaultNameScan
	}
	fingerprint := strings.TrimSpace(opts.Fingerprint)
	if embedder != nil && fingerprint == "" {
		return nil, errors.New("档案检索缺少索引指纹：无法确认向量空间")
	}
	return &Searcher{
		source:          source,
		embedder:        embedder,
		alpha:           opts.Alpha,
		onSemanticError: opts.OnSemanticError,
		minSimilarity:   minSim,
		ambiguityGap:    gap,
		fingerprint:     fingerprint,
		aliases:         opts.Aliases,
		phonetic:        opts.Phonetic,
		aliasSource:     opts.AliasSource,
		maxNameScan:     scan,
	}, nil
}

// Search 保留旧的「返回命中列表」签名；新调用方应使用 Find 以拿到状态与档位。
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]store.ViewerProfileHit, error) {
	result, err := s.Find(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	hits := make([]store.ViewerProfileHit, 0, len(result.Candidates))
	for _, c := range result.Candidates {
		hits = append(hits, c.ViewerProfileHit)
	}
	return hits, nil
}

// Find 执行一次身份解析：UID → 昵称 → 别名 → 近似 → 内容 → 语义。
//
// 语义失败只降级到词法；词法也空时返回 unavailable（不是 not_found）。
func (s *Searcher) Find(ctx context.Context, query string, limit int) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	text := strings.TrimSpace(query)
	if text == "" {
		return Result{}, errors.New("档案检索查询词不能为空")
	}
	if utf8.RuneCountInString(text) > maxSearchQueryRunes {
		return Result{}, errors.New("档案检索查询词不能超过200字")
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	// 1) UID 精确：平台身份是最强证据，命中即定人，不再扩散候选。
	// 未命中按「没找到」继续走词法：仓储统一返回 store.ErrViewerNotFound（包装 ErrNotFound），
	// 两者都必须识别，不能把陌生 UID 变成工具错误。
	if uid, ok := parseUID(text); ok {
		profile, err := s.profileByUID(ctx, uid)
		if err == nil {
			return Result{
				Status:     StatusFound,
				Candidates: []Candidate{{ViewerProfileHit: hitOf(profile), Tier: TierUID, Reason: "uid_exact"}},
				Reason:     "uid_exact",
			}, nil
		}
		if !isViewerNotFound(err) && !errors.Is(err, store.ErrInvalid) {
			return Result{}, err
		}
	}

	// 2) 词法身份解析（含别名与近似）。别名快照每次 Find 取一次，
	// 热更新在两次查询之间生效，本轮内保持一致。
	aliases := s.aliasIndex()
	candidates, err := s.lexicalCandidates(ctx, text, limit, aliases)
	if err != nil {
		return Result{}, err
	}

	// 3) 语义补充：仅作为词法之外的候选，且必须过最低相关性。
	degraded := false
	if s.embedder != nil && s.alpha > 0 {
		semantic, serr := s.semanticCandidates(ctx, text)
		switch {
		case serr == nil:
			s.mergeSemantic(candidates, semantic)
		case ctx.Err() != nil:
			return Result{}, ctx.Err()
		default:
			degraded = true
			s.degrade(serr)
		}
	}

	ordered := s.orderCandidates(candidates, limit)
	if len(ordered) == 0 {
		if degraded {
			return Result{Status: StatusUnavailable, Reason: "semantic_unavailable", Degraded: true}, nil
		}
		return Result{Status: StatusNotFound, Reason: "no_match"}, nil
	}
	return s.decide(ordered, limit, degraded), nil
}

// decide 根据排序结果给出 found/ambiguous。
//
// 歧义只在「身份候选」之间判断（near 及以上）：内容命中常常有多条，
// 那是证据而不是身份，不该因此拒绝回答。
func (s *Searcher) decide(ordered []Candidate, limit int, degraded bool) Result {
	top := ordered[0]
	if len(ordered) > 1 && top.Tier >= TierNear {
		second := ordered[1]
		if second.Tier == top.Tier && top.Score-second.Score < s.ambiguityGap {
			max := 3
			if limit < max {
				max = limit
			}
			if max > len(ordered) {
				max = len(ordered)
			}
			return Result{
				Status:     StatusAmbiguous,
				Candidates: ordered[:max],
				Reason:     "tier_gap_below_threshold",
				Degraded:   degraded,
			}
		}
	}
	max := limit
	if max > len(ordered) {
		max = len(ordered)
	}
	return Result{
		Status:     StatusFound,
		Candidates: ordered[:max],
		Reason:     top.Reason,
		Degraded:   degraded,
	}
}

// profileByUID 走可选接口；没有实现时返回不可用，由调用方当「没找到」继续。
func (s *Searcher) profileByUID(ctx context.Context, uid string) (store.ViewerProfile, error) {
	source, ok := s.source.(uidSource)
	if !ok {
		return store.ViewerProfile{}, store.ErrViewerNotFound
	}
	return source.GetProfile(ctx, uid)
}

// isViewerNotFound 判断仓储是否表达「没有这个档案」。
//
// 生产仓储返回 ErrViewerNotFound（包装 ErrNotFound）；其他 ViewerRepo 实现
// 按 store 契约返回 ErrNotFound 也必须识别。
func isViewerNotFound(err error) bool {
	return errors.Is(err, store.ErrViewerNotFound) || errors.Is(err, store.ErrNotFound)
}

// aliasIndex 返回本次认人要用的别名/同音快照。
//
// 生产走 AliasSource（文件热更新）；测试或调用方仍可直接传静态表/钩子。
func (s *Searcher) aliasIndex() AliasIndex {
	if s.aliasSource != nil {
		return s.aliasSource.AliasIndex()
	}
	idx := AliasIndex{Phonetic: s.phonetic}
	for _, a := range s.aliases {
		switch a.Status {
		case "", AliasConfirmed:
			idx.Confirmed = append(idx.Confirmed, a)
		case AliasRejected:
			idx.Rejected = append(idx.Rejected, a)
		}
	}
	return idx
}

// namePool 取活跃档案池；没有实现时返回 nil（跳过全量近似扫描）。
//
// 查询失败必须上抛：昵称池读不出来时，近似匹配就没有可信依据，
// 不能让调用方误以为「查过了，确实没这个人」。
func (s *Searcher) namePool(ctx context.Context) ([]store.ViewerProfile, error) {
	source, ok := s.source.(namePoolSource)
	if !ok {
		return nil, nil
	}
	profiles, err := source.TopViewers(ctx, s.maxNameScan)
	if err != nil {
		return nil, err
	}
	return profiles, nil
}

// lexicalCandidates 汇总词法与身份候选，按 uid 去重并保留最高档位。
func (s *Searcher) lexicalCandidates(ctx context.Context, text string, limit int, aliases AliasIndex) (map[string]*Candidate, error) {
	candidates := make(map[string]*Candidate)
	keys := keywordCandidates(text)
	keys = append(keys, decorationVariants(text)...)

	// 2.1 SQL 关键词命中：只能证明「出现过」，档位由下面的归因决定。
	// 只查 identity（昵称）与 facts（本人发言）；鱼的回复不参与认人。
	for _, key := range keys {
		for _, kind := range identitySearchKinds {
			hits, err := s.source.KeywordProfiles(ctx, kind, key, limit)
			if err != nil {
				return nil, err
			}
			for _, h := range hits {
				if strings.TrimSpace(h.Profile.UID) == "" {
					continue
				}
				tier, reason := s.identityTier(h.Profile, key, true, aliases)
				consider(candidates, h, tier, reason)
			}
		}
	}

	// 2.2 全量昵称池：处理不共享子串的错字/谐音/别名。
	const maxNameCandidates = 40
	profiles, err := s.namePool(ctx)
	if err != nil {
		return nil, err
	}
	scanned := 0
	for _, p := range profiles {
		if strings.TrimSpace(p.UID) == "" {
			continue
		}
		// keys 里的每个写法都可能是身份指代，取最高档位。
		tier, reason := TierSemantic, ""
		for _, key := range keys {
			if t, r := s.identityTier(p, key, false, aliases); t > tier {
				tier, reason = t, r
			}
		}
		if tier < TierNear {
			continue
		}
		consider(candidates, hitOf(p), tier, reason)
		scanned++
		if scanned >= maxNameCandidates {
			break
		}
	}
	return candidates, nil
}

// identityTier 判定一个档案在某个查询词下的命中档位与理由。
//
// contentFallback=true 表示这条记录来自 SQL 关键词命中（至少要算内容档）；
// false 表示来自昵称池扫描，没有理由就不要。
func (s *Searcher) identityTier(p store.ViewerProfile, key string, contentFallback bool, aliases AliasIndex) (Tier, string) {
	normKey := normalizeIdentity(key)
	name := normalizeIdentity(p.Name)

	if normKey != "" {
		if name != "" && name == normKey {
			return TierName, "name_exact"
		}
		// 人工拒绝的「别名→此人」不再产生别名/近似/同音候选；
		// 当前昵称精确命中仍然有效（那是本人现在的名字，不是猜测的关联）。
		if rejectedPair(p, normKey, aliases.Rejected) {
			if contentFallback {
				return TierContent, "content"
			}
			return TierSemantic, ""
		}
		if aliasMatches(p, normKey, aliases.Confirmed) {
			return TierAlias, "alias_exact"
		}
		if nearName(name, normKey) {
			return TierNear, "near_name"
		}
		if phoneticMatches(p.Name, key, aliases) {
			return TierNear, "phonetic"
		}
	}

	if contentFallback {
		// SQL 已证明 name/recent_texts/recent_replies 至少一处 ILIKE 命中。
		return TierContent, "content"
	}
	return TierSemantic, ""
}

// aliasMatches 判断查询词是否是这条档案的已确认别名。
func aliasMatches(p store.ViewerProfile, normKey string, confirmed []Alias) bool {
	for _, a := range confirmed {
		if normKey == "" || normalizeIdentity(a.Alias) != normKey {
			continue
		}
		if a.UID != "" && a.UID == p.UID {
			return true
		}
		if a.UID == "" && normalizeIdentity(a.Name) == normalizeIdentity(p.Name) {
			return true
		}
	}
	return false
}

// rejectedPair 判断「查询词→这条档案」是否被人工拒绝过（负例）。
func rejectedPair(p store.ViewerProfile, normKey string, rejected []Alias) bool {
	for _, a := range rejected {
		if normKey == "" || normalizeIdentity(a.Alias) != normKey {
			continue
		}
		if a.UID != "" && a.UID == p.UID {
			return true
		}
		if a.UID == "" && normalizeIdentity(a.Name) == normalizeIdentity(p.Name) {
			return true
		}
	}
	return false
}

// phoneticMatches 走同音组快照；没有数据就明确不命中。
func phoneticMatches(name, key string, aliases AliasIndex) bool {
	if aliases.Phonetic == nil {
		return false
	}
	normName := normalizeIdentity(name)
	for _, variant := range aliases.Phonetic(key) {
		if v := normalizeIdentity(variant); v != "" && v == normName {
			return true
		}
	}
	return false
}

// semanticCandidates 按用途分别取 identity / facts 向量候选，再按 uid 合并。
//
// 两路共用同一个查询向量：三路索引由同一个 embedding 模型生成，向量空间一致。
// interaction 不参与认人，所以这里永远不查它。
func (s *Searcher) semanticCandidates(ctx context.Context, text string) ([]store.ViewerProfileHit, error) {
	vector, err := s.embedQuery(ctx, text)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]store.ViewerProfileHit, semanticCandidateLimit*len(identitySearchKinds))
	for _, kind := range identitySearchKinds {
		hits, err := s.source.SemanticProfiles(ctx, kind, vector, s.fingerprint, semanticCandidateLimit)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			uid := strings.TrimSpace(h.Profile.UID)
			if uid == "" {
				continue
			}
			if math.IsNaN(h.Similarity) || math.IsInf(h.Similarity, 0) {
				continue
			}
			// 相关性拒绝：低分候选直接丢弃，允许最终返回 0 条。
			if h.Similarity < s.minSimilarity {
				continue
			}
			existing, ok := merged[uid]
			if !ok || h.Similarity > existing.Similarity {
				merged[uid] = h
			}
		}
	}
	filtered := make([]store.ViewerProfileHit, 0, len(merged))
	for _, h := range merged {
		filtered = append(filtered, h)
	}
	return filtered, nil
}

// mergeSemantic 把语义候选并入词法候选：已有条目只抬分数，不降档位。
func (s *Searcher) mergeSemantic(candidates map[string]*Candidate, semantic []store.ViewerProfileHit) {
	for _, h := range semantic {
		if c, ok := candidates[h.Profile.UID]; ok {
			if h.Similarity > c.Similarity {
				c.Similarity = h.Similarity
			}
			continue
		}
		candidates[h.Profile.UID] = &Candidate{
			ViewerProfileHit: h,
			Tier:             TierSemantic,
			Reason:           "semantic",
		}
	}
}

// consider 合并一条候选，保留更高档位（同档保留更高相似度）。
func consider(candidates map[string]*Candidate, hit store.ViewerProfileHit, tier Tier, reason string) {
	uid := hit.Profile.UID
	if existing, ok := candidates[uid]; ok {
		if tier > existing.Tier {
			existing.Tier, existing.Reason = tier, reason
		}
		// 昵称以最近一条快照为准（改名后要显示新名字），其余保留已有结果。
		if hit.Profile.LastSeen.After(existing.ViewerProfileHit.Profile.LastSeen) {
			existing.ViewerProfileHit.Profile = hit.Profile
		}
		if hit.Similarity > existing.Similarity {
			existing.Similarity = hit.Similarity
		}
		return
	}
	candidates[uid] = &Candidate{ViewerProfileHit: hit, Tier: tier, Reason: reason}
}

// hitOf 把一份档案包成 0 相似度的命中。
func hitOf(p store.ViewerProfile) store.ViewerProfileHit {
	return store.ViewerProfileHit{Profile: p}
}

// orderCandidates 按「档位 → 分数 → 相似度 → 发言数 → 最近出现 → uid」排序。
//
// 分数口径与旧实现一致：词法命中给 1 分基础分，再叠 alpha×相似度。
func (s *Searcher) orderCandidates(candidates map[string]*Candidate, limit int) []Candidate {
	ordered := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Tier == TierSemantic {
			c.Score = s.alpha * c.Similarity
		} else {
			c.Score = 1 + s.alpha*c.Similarity
		}
		ordered = append(ordered, *c)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Tier != b.Tier {
			return a.Tier > b.Tier
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Similarity != b.Similarity {
			return a.Similarity > b.Similarity
		}
		if a.Profile.Hits != b.Profile.Hits {
			return a.Profile.Hits > b.Profile.Hits
		}
		if !a.Profile.LastSeen.Equal(b.Profile.LastSeen) {
			return a.Profile.LastSeen.After(b.Profile.LastSeen)
		}
		return a.Profile.UID < b.Profile.UID
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

// keywordCandidates 返回由精确到宽松的去重关键词（原句优先）。
func keywordCandidates(text string) []string {
	seen := make(map[string]bool, 4)
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

// decorationVariants 生成「去掉首尾装饰」的写法用于身份比对。
//
// 只处理首尾（不删句内符号）：观众名常见「X奇迹与你X」「☆弓长三益☆」，
// 但正文里的标点是语义的一部分，不能一起抹掉。
func decorationVariants(text string) []string {
	var out []string
	for _, candidate := range keywordCandidates(text) {
		trimmed := strings.TrimFunc(candidate, isDecorationRune)
		if trimmed == "" || trimmed == candidate {
			continue
		}
		if utf8.RuneCountInString(trimmed) < 2 {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// isDecorationRune 判断首尾是否是可安全剥离的装饰字符。
//
// X/x 只在首尾同名出现时才被当作装饰（见 decorationVariants 的成对处理），
// 这里先统一按「非文字符号」处理。
func isDecorationRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return false
	}
	return true
}

// normalizeIdentity 规范化身份文本：全角转半角、去首尾空白与装饰、忽略大小写。
//
// 保留原始昵称，不修改库中数据；规范化只用于比较。
func normalizeIdentity(text string) string {
	text = strings.TrimSpace(text)
	text = foldWidth(text)
	text = strings.TrimFunc(text, isDecorationRune)
	text = stripPairDecoration(text)
	return strings.ToLower(text)
}

// stripPairDecoration 去掉首尾成对的同一 ASCII 字母（如「X奇迹与你X」）。
func stripPairDecoration(text string) string {
	runes := []rune(text)
	if len(runes) < 3 {
		return text
	}
	first, last := runes[0], runes[len(runes)-1]
	if first == last && first < 0x80 && unicode.IsLetter(first) {
		return strings.TrimSpace(string(runes[1 : len(runes)-1]))
	}
	return text
}

// foldWidth 把全角 ASCII（！～）与全角空格折成半角。
func foldWidth(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\u3000':
			b.WriteRune(' ')
		case r >= '\uFF01' && r <= '\uFF5E':
			b.WriteRune(r - 0xFEE0)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseUID 判断查询词是不是纯数字 UID（允许全角数字）。
func parseUID(text string) (string, bool) {
	text = strings.TrimSpace(foldWidth(text))
	if text == "" || len(text) > 32 {
		return "", false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	if text[0] == '0' {
		return "", false
	}
	return text, true
}

// nearName 判断两个规范化昵称是否「近似」。
//
// 规则刻意保守：只允许小编辑距离或子串包含，宁可漏认也不自动合并身份。
func nearName(a, b string) bool {
	if a == "" || b == "" || a == b {
		return false
	}
	ar, br := []rune(a), []rune(b)
	short, long := ar, br
	if len(short) > len(long) {
		short, long = long, short
	}
	// 两字昵称不做近似：太容易与其他名字撞上（「白饭」vs「白饭哥」），
	// 宁可漏认，也不自动把两个人并成一个。
	if len(short) < 3 {
		return false
	}
	// 子串包含：短名是长名的一部分（「弓长三益」⊂「弓长三益的狗」）。
	// 只允许 3 字以上的短名，避免描述词把所有相关昵称都拉成候选。
	if strings.Contains(string(long), string(short)) {
		return true
	}
	// 编辑距离：4 字以内最多 1，8 字以内最多 2，更长最多 3
	if abs(len(ar)-len(br)) > 3 {
		return false
	}
	maxDist := 1
	switch {
	case len(long) > 8:
		maxDist = 3
	case len(long) > 4:
		maxDist = 2
	}
	return editDistance(ar, br) <= maxDist
}

// editDistance 是两行滚动数组的 Levenshtein（避免为去重再引一个依赖）。
func editDistance(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func minInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	min := values[0]
	for _, v := range values[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
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
