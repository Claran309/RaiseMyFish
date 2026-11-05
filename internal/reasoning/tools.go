package reasoning

// 本文件定义 ReAct 可调用的四个工具。
//
// 边界：
//   - 工具只做「查」，不写库、不发消息、不改状态；
//   - 每个工具的输出都会被塞进下一轮 prompt，所以统一做长度裁剪，避免上下文爆炸；
//   - 数据来源以接口注入，测试用假实现，不连 PG、不调模型。

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
)

// 工具名是协议的一部分：模型按名字调用，改名等于改协议。
const (
	ToolMemorySearch    = "memory_search"
	ToolKnowledgeSearch = "knowledge_search"
	ToolPresence        = "presence"
	ToolHistory         = "history"
)

// maxObservationRunes 是单个工具结果进 prompt 的最大长度。
const maxObservationRunes = 400

// Tool 是一个可被 ReAct 调用的工具。
type Tool struct {
	Name string
	Desc string
	Run  func(ctx context.Context, args string) (string, error)
}

// Toolbox 是工具集合：按名字查、按顺序列（顺序稳定便于 prompt 复现）。
type Toolbox struct {
	tools map[string]Tool
	order []string
}

// NewToolbox 校验并组装工具集；名字为空/重复/Run 为空都算配置错误。
func NewToolbox(tools ...Tool) (*Toolbox, error) {
	box := &Toolbox{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" || t.Run == nil {
			return nil, fmt.Errorf("工具定义无效：%q", t.Name)
		}
		if _, dup := box.tools[name]; dup {
			return nil, fmt.Errorf("工具重名：%s", name)
		}
		box.tools[name] = t
		box.order = append(box.order, name)
	}
	return box, nil
}

// Names 返回工具名（注册顺序）。
func (b *Toolbox) Names() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.order...)
}

// Descriptions 生成 prompt 里的工具清单。
func (b *Toolbox) Descriptions() string {
	if b == nil || len(b.order) == 0 {
		return "（无可用工具）"
	}
	var sb strings.Builder
	for _, name := range b.order {
		t := b.tools[name]
		fmt.Fprintf(&sb, "- %s：%s\n", name, t.Desc)
	}
	return sb.String()
}

// Run 执行工具；未知工具返回错误而不是静默成功。
func (b *Toolbox) Run(ctx context.Context, name, args string) (string, error) {
	if b == nil {
		return "", fmt.Errorf("没有可用工具")
	}
	tool, ok := b.tools[name]
	if !ok {
		return "", fmt.Errorf("未知工具：%s", name)
	}
	out, err := tool.Run(ctx, args)
	if err != nil {
		return "", err
	}
	return clip(out, maxObservationRunes), nil
}

// MergeToolboxes 合并多个工具箱；重名直接报错（工具名是协议，不能含糊）。
func MergeToolboxes(boxes ...*Toolbox) (*Toolbox, error) {
	var all []Tool
	for _, box := range boxes {
		if box == nil {
			continue
		}
		for _, name := range box.order {
			all = append(all, box.tools[name])
		}
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("没有任何可用工具")
	}
	return NewToolbox(all...)
}

// ToolDeps 是四个工具的数据来源；为 nil 的工具不会被注册（而不是注册一个永远返回空的）。
type ToolDeps struct {
	// Memory 提供记忆召回（memory.Reader）。
	Memory MemorySearcher
	// Knowledge 提供知识/梗库召回（knowledge.Reader）。
	Knowledge KnowledgeSearcher
	// Presence 返回当前状态摘要（可为 nil）。
	Presence func(time.Time) string
	// History 提供直播间最近事件。
	History HistorySearcher
	// Scope 是当前事件范围；工具查询只在这个范围内进行，不越界。
	Scope memory.Scope
	// Now 允许注入时钟（测试用）；为 nil 用 time.Now。
	Now func() time.Time
}

// MemorySearcher 是 memory.Reader 的最小投影。
type MemorySearcher interface {
	Recall(ctx context.Context, scope memory.Scope) (memory.Context, error)
}

// MemoryQuerySearcher 是可选的带查询词召回能力（混合检索实现）。
//
// 未实现时 memory_search 退回 Recall，保持既有行为。
type MemoryQuerySearcher interface {
	Search(ctx context.Context, query memory.SearchQuery) (memory.Context, error)
}

// KnowledgeSearcher 是 knowledge.Reader 的最小投影。
type KnowledgeSearcher interface {
	Recall(ctx context.Context, query knowledge.Query) (knowledge.Context, error)
}

// HistorySearcher 提供某会话最近的事件。
type HistorySearcher interface {
	RecentEvents(ctx context.Context, ch event.Channel, session string, limit int) ([]event.Event, error)
}

// BuildTools 按可用依赖装配工具集。
func BuildTools(d ToolDeps) (*Toolbox, error) {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	var tools []Tool

	if d.Memory != nil {
		scope := d.Scope
		tools = append(tools, Tool{
			Name: ToolMemorySearch,
			Desc: "查本会话/跨通道的历史记忆与摘要；args 传查询词或空",
			Run: func(ctx context.Context, args string) (string, error) {
				queryText := strings.TrimSpace(args)
				var (
					got memory.Context
					err error
				)
				if queryText != "" {
					if searcher, ok := d.Memory.(MemoryQuerySearcher); ok {
						got, err = searcher.Search(ctx, memory.SearchQuery{Scope: scope, Text: queryText, Limit: 10})
					} else {
						got, err = d.Memory.Recall(ctx, scope)
					}
				} else {
					got, err = d.Memory.Recall(ctx, scope)
				}
				if err != nil {
					return "", err
				}
				return formatMemory(got), nil
			},
		})
	}

	if d.Knowledge != nil {
		scope := d.Scope
		tools = append(tools, Tool{
			Name: ToolKnowledgeSearch,
			Desc: "查梗库/设定；args 传要查的词或整句",
			Run: func(ctx context.Context, args string) (string, error) {
				query := knowledge.Query{
					Scope: knowledge.Scope{Channel: scope.Channel, Session: scope.Session, Private: scope.Private},
					Text:  strings.TrimSpace(args),
				}
				got, err := d.Knowledge.Recall(ctx, query)
				if err != nil {
					return "", err
				}
				return formatKnowledge(got), nil
			},
		})
	}

	if d.Presence != nil {
		check := d.Presence
		tools = append(tools, Tool{
			Name: ToolPresence,
			Desc: "查本鱼此刻在做什么（在看直播/在回弹幕等）；args 忽略",
			Run: func(_ context.Context, _ string) (string, error) {
				if summary := check(now()); strings.TrimSpace(summary) != "" {
					return summary, nil
				}
				return "没有进行中的状态", nil
			},
		})
	}

	if d.History != nil {
		scope := d.Scope
		tools = append(tools, Tool{
			Name: ToolHistory,
			Desc: "查本房间最近几条弹幕；args 可传条数（1-20，默认 10）",
			Run: func(ctx context.Context, args string) (string, error) {
				limit := parseLimit(args, 10, 20)
				rows, err := d.History.RecentEvents(ctx, scope.Channel, scope.Session, limit)
				if err != nil {
					return "", err
				}
				return formatHistory(rows), nil
			},
		})
	}

	if len(tools) == 0 {
		return nil, fmt.Errorf("没有任何可用工具，ReAct 无法运行")
	}
	return NewToolbox(tools...)
}

// formatMemory 把记忆召回结果压成一行行文本。
func formatMemory(c memory.Context) string {
	var parts []string
	for _, m := range c.Recent {
		parts = append(parts, m.FromName+": "+m.Text)
	}
	for _, r := range c.Records {
		parts = append(parts, "摘要: "+r.Summary)
	}
	if len(parts) == 0 {
		return "没有相关记忆"
	}
	return strings.Join(parts, " / ")
}

// formatKnowledge 把知识命中压成「ID: 正文」。
func formatKnowledge(c knowledge.Context) string {
	var parts []string
	for _, item := range c.Items {
		parts = append(parts, item.ID+": "+item.Text)
	}
	if len(parts) == 0 {
		return "没有命中知识"
	}
	return strings.Join(parts, " / ")
}

// formatHistory 把最近事件压成「昵称: 正文」。
func formatHistory(rows []event.Event) string {
	var parts []string
	for _, e := range rows {
		parts = append(parts, e.FromName+": "+e.Text)
	}
	if len(parts) == 0 {
		return "最近没有弹幕"
	}
	return strings.Join(parts, " / ")
}

// parseLimit 解析条数参数，非法时回落到默认值。
// MCP 调用传 JSON 对象（{"limit":n}），先走 parseLimitArg；旧格式仍按数字前缀解析。
func parseLimit(args string, fallback, max int) int {
	args = strings.TrimSpace(args)
	if args == "" {
		return fallback
	}
	if strings.HasPrefix(args, "{") {
		n, err := parseLimitArg(args, fallback, max)
		if err != nil {
			return fallback
		}
		return n
	}
	var n int
	if _, err := fmt.Sscanf(args, "%d", &n); err != nil || n < 1 {
		return fallback
	}
	if n > max {
		return max
	}
	return n
}

// clip 按 rune 截断，避免把 UTF-8 切坏。
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}
