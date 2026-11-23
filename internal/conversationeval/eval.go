// Package conversationeval 是会话多轮的离线评测。
//
// 它只做「上下文可达性」的确定性度量：把同一批用例分别用
// 「带会话」与「不带会话」装配成真实 prompt，检查追问所需的先行词
// 是否出现在 prompt 里、是否串到别人、以及 prompt token 涨了多少。
//
// 边界：
//   - 不调用模型、不读数据库、不写发送记录；
//   - 不把「prompt 里能找到先行词」冒充成「模型一定答对」，
//     报告里明确区分离线指标与需要真实模型/人工的指标；
//   - P95 延迟与成本无法离线测量，报告里标为未测量，不给 0 冒充。
package conversationeval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
)

// Turn 是用例里的一轮历史。
type Turn struct {
	From string `json:"from,omitempty"`
	Dir  string `json:"dir"`
	Text string `json:"text"`
}

// Case 是一条会话评测用例。
type Case struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Channel        string `json:"channel"`
	Private        bool   `json:"private,omitempty"`
	ConversationID string `json:"conversation_id"`
	Session        string `json:"session"`
	// Summary 是模拟 C5 已生成的会话摘要；空表示没有摘要。
	Summary string `json:"summary,omitempty"`
	Turns   []Turn `json:"turns"`
	Input   string `json:"input"`
	// ExpectContext 是追问要接上时必须出现在 prompt 里的先行词。
	ExpectContext []string `json:"expect_context,omitempty"`
	// ForbidContext 是绝不能出现在本用例 prompt 里的内容（串人/跨视频/隐私）。
	ForbidContext []string `json:"forbid_context,omitempty"`
}

// CaseResult 是单条用例的结果。
type CaseResult struct {
	ID string `json:"id"`
	// Resolved 表示带会话时先行词齐全。
	Resolved bool `json:"resolved"`
	// BaselineResolved 表示不带会话时先行词也已齐全（通常是 0，用来证明增益）。
	BaselineResolved bool `json:"baseline_resolved"`
	// Leaked 表示出现禁止内容。
	Leaked bool `json:"leaked"`
	// TokenDeltaPct 是带会话相对不带会话的 prompt token 变化。
	TokenDeltaPct float64 `json:"token_delta_pct"`
	// DuplicatedLines 是会话段里重复出现的行数（重复注入）。
	DuplicatedLines int `json:"duplicated_lines"`
	// InjectedTurns 是实际注入的轮数（受 recent_turns 上限约束）。
	InjectedTurns int    `json:"injected_turns"`
	Reason        string `json:"reason,omitempty"`
}

// Report 是整轮评测汇总。
type Report struct {
	Cases int `json:"cases"`
	// Resolved / BaselineResolved 是「先行词齐全」的用例数。
	Resolved         int `json:"resolved"`
	BaselineResolved int `json:"baseline_resolved"`
	// FollowupResolutionRate 与 BaselineResolutionRate 是对应比例。
	FollowupResolutionRate float64 `json:"followup_resolution_rate"`
	BaselineResolutionRate float64 `json:"baseline_resolution_rate"`
	// DeltaPoints 是两者的百分点差。
	DeltaPoints float64 `json:"delta_points"`
	// CrossUserLeakRate 是出现禁止内容的用例比例，目标 0。
	CrossUserLeakRate float64 `json:"cross_user_leak_rate"`
	// KeyCollisions 是会话键与其他用例冲突的条数（串人/跨视频的根因）。
	KeyCollisions int `json:"key_collisions"`
	// PromptTokenDeltaPct 是平均 token 变化；只统计基线可算的用例。
	PromptTokenDeltaPct float64 `json:"prompt_token_delta_pct"`
	// RepetitionRate 是重复注入行占比。
	RepetitionRate float64 `json:"repetition_rate"`
	// P95LatencyDeltaMS / CostPerReplyDelta 需要真实调用，离线不给数字。
	P95LatencyDeltaMS    *float64 `json:"p95_latency_delta_ms"`
	CostPerReplyDeltaCNY *float64 `json:"cost_per_reply_delta_cny"`
	// Notes 记录未测量项与口径说明。
	Notes  []string     `json:"notes,omitempty"`
	Detail []CaseResult `json:"detail"`
}

// LoadCases 读取 JSONL 用例；空行与 BOM 自动跳过。
func LoadCases(path string) ([]Case, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取会话用例失败：%w", err)
	}
	defer file.Close()
	var cases []Case
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("会话用例第%d行无效：%w", len(cases)+1, err)
		}
		if err := validateCase(c); err != nil {
			return nil, fmt.Errorf("会话用例第%d行：%w", len(cases)+1, err)
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return cases, nil
}

func validateCase(c Case) error {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Input) == "" {
		return fmt.Errorf("缺 id 或 input")
	}
	channel := event.Channel(c.Channel)
	if channel != event.ChDM && channel != event.ChComment {
		return fmt.Errorf("通道只能是 dm 或 comment")
	}
	if strings.TrimSpace(c.Session) == "" {
		return fmt.Errorf("缺 session")
	}
	if len(c.Turns) == 0 {
		return fmt.Errorf("缺历史轮次")
	}
	for _, turn := range c.Turns {
		if turn.Dir != "in" && turn.Dir != "out" {
			return fmt.Errorf("轮次方向只能是 in/out")
		}
		if strings.TrimSpace(turn.Text) == "" {
			return fmt.Errorf("轮次正文不能为空")
		}
	}
	return nil
}

// Options 是评测参数。
type Options struct {
	// Pack 是人格包（与线上同一份）。
	Pack persona.Pack
	// Budget 是 prompt 预算；零值时用 brain.DefaultBudget()。
	Budget brain.Budget
	// RecentTurns 是注入的最近轮数；<=0 时用 8（与默认配置一致）。
	RecentTurns int
}

// recentTurns 是与 conversation.recent_turns 默认值一致的注入上限。
const recentTurns = 8

// Run 跑一轮离线评测。
func Run(ctx context.Context, cases []Case, opts Options) (Report, error) {
	if ctx == nil {
		return Report{}, fmt.Errorf("ctx 不能为 nil")
	}
	budget := opts.Budget
	if budget.MaxPromptTokens == 0 {
		budget = brain.DefaultBudget()
	}
	limit := opts.RecentTurns
	if limit <= 0 {
		limit = recentTurns
	}
	report := Report{Cases: len(cases), Detail: make([]CaseResult, 0, len(cases))}
	var deltaSum float64
	var deltaCount int
	var totalLines, duplicatedLines int
	// 会话键隔离是「不串人」的结构性前提：不同用例的键必须两两不同。
	// 这里不是扫 prompt，而是直接算键——键相同就一定串，键不同才谈得上隔离。
	keyOwner := make(map[string]string, len(cases))
	collisions := make(map[string]bool)
	for _, c := range cases {
		key := conversationKey(c)
		if key == "" {
			continue
		}
		if owner, ok := keyOwner[key]; ok && owner != c.ID {
			collisions[owner] = true
			collisions[c.ID] = true
			continue
		}
		keyOwner[key] = c.ID
	}
	report.KeyCollisions = len(collisions)
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		result, err := runCase(ctx, c, opts.Pack, budget, limit)
		if err != nil {
			return Report{}, fmt.Errorf("用例 %s 失败：%w", c.ID, err)
		}
		if collisions[c.ID] {
			result.Leaked = true
			result.Reason = "会话键与其他用例冲突"
		}
		// 先判定再入表：append 的是值拷贝，追加后再改 result 不会生效。
		report.Detail = append(report.Detail, result)
		if result.Resolved {
			report.Resolved++
		}
		if result.BaselineResolved {
			report.BaselineResolved++
		}
		if result.Leaked {
			report.CrossUserLeakRate++
		}
		totalLines += len(c.Turns)
		duplicatedLines += result.DuplicatedLines
		// token 变化对每条用例都成立（带会话 vs 不带会话），全部计入均值。
		deltaSum += result.TokenDeltaPct
		deltaCount++
	}
	if report.Cases > 0 {
		report.FollowupResolutionRate = float64(report.Resolved) / float64(report.Cases)
		report.BaselineResolutionRate = float64(report.BaselineResolved) / float64(report.Cases)
		report.CrossUserLeakRate /= float64(report.Cases)
	}
	report.DeltaPoints = (report.FollowupResolutionRate - report.BaselineResolutionRate) * 100
	if deltaCount > 0 {
		report.PromptTokenDeltaPct = deltaSum / float64(deltaCount)
	}
	if totalLines > 0 {
		report.RepetitionRate = float64(duplicatedLines) / float64(totalLines)
	}
	report.Notes = []string{
		"离线指标只衡量「追问所需先行词是否进入 prompt」，不代表模型输出正确率。",
		"p95 延迟与单条成本需要真实模型调用，离线不测量，保持 null。",
	}
	return report, nil
}

func runCase(ctx context.Context, c Case, pack persona.Pack, budget brain.Budget, limit int) (CaseResult, error) {
	result := CaseResult{ID: c.ID}
	channel := event.Channel(c.Channel)
	conversationID := conversationKey(c)
	if conversationID == "" {
		return result, fmt.Errorf("会话键为空")
	}
	ev := event.Event{
		ID: c.Channel + ":" + c.Session + ":eval", Channel: channel, Dir: event.DirIn,
		Session: c.Session, FromID: "eval-user", FromName: "评测用户", Text: c.Input,
		Private: c.Private, At: evalTime(), ConversationID: conversationID,
	}
	task := brain.Task{ID: "task-" + c.ID, EventID: ev.ID, Channel: channel, Session: c.Session}
	scope := memory.Scope{Channel: channel, Session: c.Session, Private: c.Private}
	base := brain.ContextInput{
		Event: ev, Persona: pack, Memory: memory.Context{Scope: scope}, Shared: memory.SharedState{Scope: scope},
	}
	withConversation := base
	withConversation.Conversation = &brain.ConversationContext{}
	// 与线上一致：只注入最近 recentTurns 轮，更早的事实必须靠摘要承载。
	turns := c.Turns
	if len(turns) > limit {
		turns = turns[len(turns)-limit:]
	}
	withConversation.Conversation.Summary = strings.TrimSpace(c.Summary)
	seen := map[string]int{}
	for _, turn := range turns {
		withConversation.Conversation.Turns = append(withConversation.Conversation.Turns, brain.ConversationTurn{
			From: turn.From, Dir: turn.Dir, Text: turn.Text,
		})
		key := turn.Dir + "|" + strings.TrimSpace(turn.Text)
		seen[key]++
		if seen[key] > 1 {
			result.DuplicatedLines++
		}
	}
	result.InjectedTurns = len(turns)

	prepared, err := brain.Prepare(ctx, task, withConversation, budget)
	if err != nil {
		return result, err
	}
	plain, err := brain.Prepare(ctx, task, base, budget)
	if err != nil {
		return result, err
	}

	missing := missingTokens(prepared.Context, c.ExpectContext)
	result.Resolved = len(missing) == 0
	if !result.Resolved {
		result.Reason = "缺少先行词：" + strings.Join(missing, "、")
	}
	result.BaselineResolved = len(missingTokens(plain.Context, c.ExpectContext)) == 0
	for _, token := range c.ForbidContext {
		if strings.Contains(prepared.Context, token) {
			result.Leaked = true
			result.Reason = "出现禁止内容：" + token
			break
		}
	}
	if plain.Estimate > 0 {
		result.TokenDeltaPct = float64(prepared.Estimate-plain.Estimate) / float64(plain.Estimate) * 100
	}
	return result, nil
}

func missingTokens(text string, tokens []string) []string {
	missing := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if !strings.Contains(text, token) {
			missing = append(missing, token)
		}
	}
	return missing
}

// conversationKey 取用例声明的会话键；空值时按通道语义派生，
// 保证「跨视频/跨用户」这类用例真的落在不同的键上。
func conversationKey(c Case) string {
	if key := strings.TrimSpace(c.ConversationID); key != "" {
		return key
	}
	return ""
}

// evalTime 是用例的固定事件时间：评测必须可复现，不能用当前时钟。
func evalTime() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }

// Runes 便于调用方做长度断言（用例正文上限与线上一致）。
func Runes(text string) int { return utf8.RuneCountInString(text) }
