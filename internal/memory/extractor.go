package memory

// 本文件实现「可插拔提炼器」：规则版（纯函数）与 LLM 版（语义理解）。
//
// 设计取舍：
//   - LLM 只对「值得多花一次调用」的事件跑（@鱼 / 长文本 / 强互动），普通弹幕走规则版省成本；
//   - LLM 输出解析失败、超时、限流 → **一律回落规则版**，绝不让提炼断掉；
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
const LLMExtractPrompt = `你是记忆提炼器。读一条直播弹幕和最近上下文，判断这条弹幕里是否包含"值得长期记住的事实"（对方说的关于自己的事、做出的承诺、明确偏好）。
只输出一行 JSON，不要多余文字：
{"archive": true/false, "records":[{"kind":"fact|promise|preference|first_seen","title":"不超过60字","summary":"不超过200字","importance":0.0-1.0}], "confidence":0.0-1.0}
没有值得记的就输出 {"archive": false, "records": [], "confidence": 0.0}。`

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
func (e *LLMExtractor) Extract(ctx context.Context, ev event.Event, opts ExtractOptions) ([]Candidate, error) {
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
	user := buildLLMInput(ev, opts)
	raw, err := e.llm.Complete(ctx, LLMExtractPrompt, user)
	if err != nil {
		return nil, err
	}
	return parseLLMRecords(raw, ev)
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
	fmt.Fprintf(&sb, "弹幕：%s\n", strings.TrimSpace(ev.Text))
	return sb.String()
}

// llmRecord 是模型返回的单个记录。
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
func parseLLMRecords(raw string, ev event.Event) ([]Candidate, error) {
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
	if !reply.Archive || len(reply.Records) == 0 {
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
		out = append(out, Candidate{Kind: kind, Title: title, Summary: summary, Importance: importance, Confidence: confidence})
	}
	if len(out) == 0 {
		return nil, errors.New("模型返回的记录不可用")
	}
	_ = ev
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
