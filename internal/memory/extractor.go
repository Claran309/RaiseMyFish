package memory

// 本文件实现「可插拔提炼器」：规则版（纯函数）与 LLM 版（语义理解）。
//
// 设计取舍：
//   - LLM 只对「值得多花一次调用」的事件跑（@鱼 / 长文本 / 强互动），普通弹幕走规则版省成本；
//   - LLM 输出解析失败、超时、限流 → **一律回落规则版**，绝不让提炼断掉；
//   - 但模型**明确否决**（archive=false）不是失败：一条都不写，也不回退规则版，
//     否则「模型说不值得记」会被规则版的兜底覆盖掉；
//   - 提炼记录的主体（from_id/from_name）只从可信事件取，模型只决定「记什么」；
//   - 限流是进程内滑动小时窗口，超出即回落规则版（不是报错）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/llmmeta"
)

// Extractor 是可插拔的提炼器。
type Extractor interface {
	Extract(ctx context.Context, ev event.Event, opts ExtractOptions) ([]Candidate, error)
}

// RuleExtractor 是纯规则版（现有四条规则），无网络、无状态。
type RuleExtractor struct{}

// NewRuleExtractor 返回规则版提炼器。
func NewRuleExtractor() *RuleExtractor { return &RuleExtractor{} }

// Extract 直接复用纯函数 Extract。
func (RuleExtractor) Extract(_ context.Context, ev event.Event, opts ExtractOptions) ([]Candidate, error) {
	return Extract(ev, opts), nil
}

// LLM 是提炼器需要的最小模型能力（由 provider 适配，测试用 mock）。
type LLM interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// LLMExtractPrompt 是固定的提炼提示词。
const LLMExtractPrompt = `你是记忆提炼器。读一条直播弹幕和最近上下文，判断这条弹幕里是否包含"值得长期记住且以后还能用的稳定信息"。

【该记】（高门槛，必须满足）：
1. 明确的稳定偏好：「我喜欢/讨厌 X」「我爱吃 X」「常玩 X」
2. 真实的身份/关系自述：职业、所在地、和主播的关系（但「我是梁文峰」这种反差玩笑不算）
3. 明确承诺：「明天给你带 X」「答应过 X」
4. 长期有效的习惯/事实：「每天早上 X 点起」「养了两只猫」

【不该记】（一律 archive=false）：
- 玩笑、钓鱼、整活、玩梗（「我是梁文峰」「按 alt+f4」「我是盲人」）
- 恶搞/挑衅/命令（「主播我命令你 X」「展示 alt+f4」）
- 情绪废话/反问/感叹（「那我想被不骂呢」「怎么会这样」）
- 一次性闲聊、当时说的话、无信息量的附和
- 对主播的临时要求/起哄

只输出一行 JSON，不要多余文字：
{"archive": true/false, "records":[{"kind":"fact|promise|preference|first_seen","title":"不超过60字","summary":"不超过200字","importance":0.0-1.0}], "confidence":0.0-1.0}
没有值得记的就输出 {"archive": false, "records": [], "confidence": 0.0}。
注意：confidence 只在「确定是稳定真实的」时给 >=0.7，否则 archive=false。`

// LLMExtractOptions 是 LLM 提炼器的参数。
type LLMExtractOptions struct {
	// MaxPerHour 是每小时最多调用几次模型；<=0 表示不限。
	MaxPerHour int
	// ContextLimit 是带给模型的最近上下文条数（<=0 用 5）。
	ContextLimit int
	// Now 可注入时钟（测试用）。
	Now func() time.Time
}

// LLMExtractor 在「值得跑」的事件上用模型提炼，其余回落规则版。
type LLMExtractor struct {
	llm   LLM
	rules *RuleExtractor
	opts  LLMExtractOptions

	mu    sync.Mutex
	calls []time.Time
}

// NewLLMExtractor 装配 LLM 提炼器；llm 为 nil 时退化成纯规则版。
func NewLLMExtractor(llm LLM, opts LLMExtractOptions) (*LLMExtractor, error) {
	if opts.ContextLimit <= 0 {
		opts.ContextLimit = 5
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxPerHour < 0 {
		return nil, errors.New("LLM 提炼每小时上限不能为负")
	}
	return &LLMExtractor{llm: llm, rules: NewRuleExtractor(), opts: opts}, nil
}

// Extract 按触发策略决定走 LLM 还是规则；任何异常都回落规则版。
//
// 隐私边界先于一切模型调用：未授权的私密事件直接丢弃，不拼 prompt、
// 不占限流额度、不调用 Complete。规则版自己也会拦，但这里必须再拦一次，
// 否则规则结果为空后仍会带着私密正文进入 LLM 分支。
func (e *LLMExtractor) Extract(ctx context.Context, ev event.Event, opts ExtractOptions) ([]Candidate, error) {
	if ev.Private && !opts.AllowPrivate {
		return nil, nil
	}
	ruleResult := Extract(ev, opts)
	if e == nil || e.llm == nil {
		return ruleResult, nil
	}
	if !shouldUseLLM(ev, opts) {
		return ruleResult, nil
	}
	if !e.reserve() {
		return ruleResult, nil // 限流：回落规则，不报错
	}
	out, err := e.llmOut(ctx, ev, opts)
	if errors.Is(err, errArchiveRejected) {
		// 模型明确否决：一条都不写，也不拿规则版兜底。
		return nil, nil
	}
	if err != nil || len(out) == 0 {
		// 解析失败/模型失败：回落规则版（降级是设计，不是错误）
		return ruleResult, nil //nolint:nilerr // 有意吞掉：LLM 只是增强
	}
	return out, nil
}

// shouldUseLLM 是触发策略：只对值得多花一次调用的事件跑模型。
//
//   - 文本里 @ 了本鱼（互动性强，语义容易误解）；
//   - 长文本（>20 字，规则词表覆盖不到）；
//   - 强互动词（摸/抱/亲 等）。
func shouldUseLLM(ev event.Event, _ ExtractOptions) bool {
	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return false
	}
	if utf8.RuneCountInString(text) > 20 {
		return true
	}
	if strings.Contains(text, "@") || strings.Contains(text, "＠") {
		return true
	}
	for _, w := range []string{"摸", "抱", "亲", "蹭", "贴贴"} {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// reserve 占用一次模型调用额度（滑动小时窗口）。
func (e *LLMExtractor) reserve() bool {
	if e.opts.MaxPerHour <= 0 {
		return true
	}
	now := e.opts.Now()
	cutoff := now.Add(-time.Hour)
	e.mu.Lock()
	defer e.mu.Unlock()
	kept := e.calls[:0]
	for _, at := range e.calls {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	e.calls = kept
	if len(e.calls) >= e.opts.MaxPerHour {
		return false
	}
	e.calls = append(e.calls, now)
	return true
}

// llmOut 调模型并解析 JSON；失败返回错误由调用方回落规则版。
func (e *LLMExtractor) llmOut(ctx context.Context, ev event.Event, opts ExtractOptions) ([]Candidate, error) {
	if ctx == nil {
		return nil, errors.New("缺少上下文")
	}
	// 账本关联：这次提炼归属到被提炼的事件（只带 ID，正文不离开 prompt）。
	ctx = llmmeta.WithCall(ctx, llmmeta.Call{Purpose: llmmeta.PurposeMemoryExtract, CorrelationID: ev.ID})
	user := buildLLMInput(ev, opts)
	raw, err := e.llm.Complete(ctx, LLMExtractPrompt, user)
	if err != nil {
		return nil, err
	}
	return parseLLMRecords(raw, ev, opts)
}

// buildLLMInput 拼「事件正文 + 昵称 + 最近上下文」。
func buildLLMInput(ev event.Event, opts ExtractOptions) string {
	var sb strings.Builder
	if name := strings.TrimSpace(ev.FromName); name != "" {
		fmt.Fprintf(&sb, "昵称：%s\n", name)
	}
	if owner := strings.TrimSpace(opts.OwnerName); owner != "" {
		fmt.Fprintf(&sb, "（主播/主人昵称：%s）\n", owner)
	}
	if len(opts.Context) > 0 {
		sb.WriteString("最近上下文（旧→新）：\n")
		for _, prev := range opts.Context {
			fmt.Fprintf(&sb, "- %s: %s\n", strings.TrimSpace(prev.FromName), strings.TrimSpace(prev.Text))
		}
	}
	fmt.Fprintf(&sb, "弹幕：%s\n", strings.TrimSpace(ev.Text))
	return sb.String()
}

// errArchiveRejected 表示模型**明确否决**归档（archive=false）。
//
// 它和「解析失败」必须分开：解析失败可以回落规则版兜底，
// 但模型看过上下文后说「不值得记」，规则版再写一遍等于无视判定。
var errArchiveRejected = errors.New("模型明确拒绝归档")

// llmRecord 是模型返回的单个记录。
//
// 刻意不包含 from_id/from_name：主体必须来自可信事件，
// 模型输出里就算带了也一律忽略（提示词里也没有这两个字段）。
type llmRecord struct {
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Summary    string  `json:"summary"`
	Importance float64 `json:"importance"`
}

type llmReply struct {
	Archive    bool        `json:"archive"`
	Records    []llmRecord `json:"records"`
	Confidence float64     `json:"confidence"`
}

// parseLLMRecords 解析模型输出并校验成 Candidate。
//
// 两条硬规则（M04）：
//  1. 来源只从事件取（AssertedBy=发言人，主体身份按可信 UID 判定），
//     模型只负责「记什么」，不负责「谁的」；
//  2. archive=false 返回 errArchiveRejected，由调用方决定「一条都不写」。
func parseLLMRecords(raw string, ev event.Event, opts ExtractOptions) ([]Candidate, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, errors.New("模型输出为空")
	}
	// 容忍代码块与前后废话
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimPrefix(text, "json")
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			text = text[start : end+1]
		}
	}
	var reply llmReply
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		return nil, err
	}
	if !reply.Archive {
		// 明确否决：不产候选，也不允许回退规则版。
		return nil, errArchiveRejected
	}
	if len(reply.Records) == 0 {
		// archive=true 但没有记录：属于模型输出不完整，按解析失败处理。
		return nil, nil
	}
	out := make([]Candidate, 0, len(reply.Records))
	for _, r := range reply.Records {
		kind := normalizeKind(r.Kind)
		title := truncateRunes(strings.TrimSpace(r.Title), maxTitleRunes)
		summary := truncateRunes(strings.TrimSpace(r.Summary), maxSummaryRunes)
		if title == "" || summary == "" {
			continue
		}
		importance := r.Importance
		if importance < 0 || importance > 1 {
			importance = factImportance
		}
		confidence := reply.Confidence
		if confidence <= 0 || confidence > 1 {
			confidence = 0.7
		}
		fromID, fromName, assertedID, assertedName, subject := candidateProvenance(ev, opts, kind, title, summary)
		out = append(out, Candidate{
			Kind: kind, Title: title, Summary: summary,
			Importance: importance, Confidence: confidence,
			// 来源由可信事件带出：模型编造的 from_id/from_name 不会被采信。
			FromID: fromID, FromName: fromName,
			AssertedByID: assertedID, AssertedByName: assertedName, Subject: subject,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("模型返回的记录不可用")
	}
	return out, nil
}

// normalizeKind 把模型给的 kind 收敛到已知四类；未知按 fact 处理。
func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case KindPromise:
		return KindPromise
	case KindPreference:
		return KindPreference
	case KindFirstSeen:
		return KindFirstSeen
	default:
		return KindFact
	}
}
