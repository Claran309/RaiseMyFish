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

// Has 判断某个工具是否已注册；调用方据此决定是否改用主动查证路径。
func (b *Toolbox) Has(name string) bool {
	if b == nil {
		return false
	}
	_, ok := b.tools[name]
	return ok
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
	// 结构化信封按来源/条目取舍后再进 prompt；其他输出保持原有裁剪行为。
	if rendered, ok := renderToolResult(out, maxObservationRunes); ok {
		return rendered, nil
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
//
// Scope 显式传入，避免工具把私密会话的历史带进公开回复。
type HistorySearcher interface {
	RecentEvents(ctx context.Context, scope memory.Scope, limit int) ([]event.Event, error)
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
			Desc: "查本会话/跨通道的历史弹幕原文与记忆摘要；args 传昵称或关键词（如「弓长三益」），也支持「昨天/昨晚/前天/上次」时间窗（可带主题词）；不要传整句；留空看最近记录",
			Run: func(ctx context.Context, args string) (string, error) {
				rawQuery := strings.TrimSpace(args)
				queryText, window := parseMemoryTimeWindow(rawQuery, now())
				var (
					got memory.Context
					err error
				)
				searcher, canSearch := d.Memory.(MemoryQuerySearcher)
				switch {
				case rawQuery == "" || !canSearch:
					got, err = d.Memory.Recall(ctx, scope)
				default:
					keywords := memoryKeywordVariants(cleanMemoryKeyword(queryText))
					if len(keywords) == 0 && !window.active() {
						// 清洗后没有主题词（如只说了「记得」）：仍按原词查，
						// 不静默退化成「最近记录」。
						keywords = memoryKeywordVariants(rawQuery)
					}
					query := memory.SearchQuery{
						Scope: scope, Limit: 10,
						After: window.After, Before: window.Before,
					}
					for _, keyword := range keywords {
						query.Text = keyword
						got, err = searcher.Search(ctx, query)
						if err != nil || !memoryContextEmpty(got) {
							break
						}
					}
					// 时间窗内主题词没命中时退回「只看时间窗」：
					// 「昨晚顶号的事」这类问法应该至少能看到那段时间发生过什么。
					if err == nil && memoryContextEmpty(got) && window.active() {
						query.Text = ""
						got, err = searcher.Search(ctx, query)
					}
				}
				if err != nil {
					return "", err
				}
				return formatMemory(got, ToolMemorySearch, memoryQueryKey(rawQuery), toolScopeFromMemory(scope), now(), window.label()), nil
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
				return formatKnowledge(got, ToolKnowledgeSearch, query.Text, toolScopeFromMemory(scope), now()), nil
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
				rows, err := d.History.RecentEvents(ctx, scope, limit)
				if err != nil {
					return "", err
				}
				return formatHistory(rows, ToolHistory, fmt.Sprintf("limit=%d", limit), toolScopeFromMemory(scope), now()), nil
			},
		})
	}

	if len(tools) == 0 {
		return nil, fmt.Errorf("没有任何可用工具，ReAct 无法运行")
	}
	return NewToolbox(tools...)
}

// memoryTimeWindow 是 memory_search 解析出的相对时间窗；Name 为空表示无时间窗。
type memoryTimeWindow struct {
	After  time.Time
	Before time.Time
	Name   string
}

func (w memoryTimeWindow) active() bool { return w.Name != "" }

// label 是给模型看的时间窗说明；本地时区，避免 UTC 日期与观众认知错位。
func (w memoryTimeWindow) label() string {
	if !w.active() {
		return ""
	}
	return fmt.Sprintf("%s %s ~ %s", w.Name,
		w.After.Local().Format("01-02 15:04"), w.Before.Local().Format("01-02 15:04"))
}

// parseMemoryTimeWindow 从查询词里剥离「昨天/昨晚/前天/上次」并换算时间窗。
//
// 只认这几个明确说法：更模糊的「最近」保持原有语义（最近记录），
// 不擅自按时间窗召回。半开区间 [After, Before)，与 repository 口径一致。
func parseMemoryTimeWindow(query string, now time.Time) (string, memoryTimeWindow) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", memoryTimeWindow{}
	}
	local := now.In(time.Local)
	startOfDay := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	}
	yesterday := startOfDay(local.AddDate(0, 0, -1))
	today := startOfDay(local)
	switch {
	case strings.Contains(trimmed, "前天"):
		return stripMemoryTimeWord(trimmed, "前天"), memoryTimeWindow{
			After: yesterday.AddDate(0, 0, -1), Before: yesterday, Name: "前天",
		}
	case strings.Contains(trimmed, "昨晚"):
		before := today.Add(6 * time.Hour)
		if before.After(local) {
			before = local
		}
		return stripMemoryTimeWord(trimmed, "昨晚"), memoryTimeWindow{
			After: yesterday.Add(18 * time.Hour), Before: before, Name: "昨晚",
		}
	case strings.Contains(trimmed, "昨天"):
		return stripMemoryTimeWord(trimmed, "昨天"), memoryTimeWindow{
			After: yesterday, Before: today, Name: "昨天",
		}
	case strings.Contains(trimmed, "昨日"):
		return stripMemoryTimeWord(trimmed, "昨日"), memoryTimeWindow{
			After: yesterday, Before: today, Name: "昨天",
		}
	case strings.Contains(trimmed, "上次"):
		return stripMemoryTimeWord(trimmed, "上次"), memoryTimeWindow{
			After: local.Add(-7 * 24 * time.Hour), Before: local, Name: "上次（最近7天）",
		}
	}
	return trimmed, memoryTimeWindow{}
}

func stripMemoryTimeWord(text, word string) string {
	return strings.TrimSpace(strings.Replace(text, word, " ", 1))
}

// cleanMemoryKeyword 去掉问句外壳与时间词残留，留下可检索的主题词。
func cleanMemoryKeyword(text string) string {
	name := strings.TrimSpace(text)
	leads := []string{"你还记不记得", "还记不记得", "记不记得", "你还记得", "还记得", "记得", "我想知道", "有没有人", "有没有", "是不是", "那个", "这件", "那件"}
	tails := []string{"的事情", "的事", "这件事", "那件事", "这个事", "那个事", "什么", "啥", "吗", "呢", "嘛", "吧", "啊", "呀", "呗", "了"}
	for {
		changed := false
		for _, lead := range leads {
			if strings.HasPrefix(name, lead) {
				name = strings.TrimSpace(strings.TrimPrefix(name, lead))
				changed = true
			}
		}
		for _, tail := range tails {
			if strings.HasSuffix(name, tail) {
				name = strings.TrimSpace(strings.TrimSuffix(name, tail))
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return strings.Trim(name, " 　，。！？、；：,.!?;:…—－-“”\"'")
}

// memoryKeywordVariants 生成少量口语变体（去「过/了」），
// 让「有没有喂过饭」也能命中原话里的「喂饭」。
func memoryKeywordVariants(keyword string) []string {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil
	}
	out := []string{keyword}
	seen := map[string]bool{keyword: true}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] || len(out) >= 3 {
			return
		}
		seen[value] = true
		out = append(out, value)
	}
	if strings.Contains(keyword, "过") {
		add(strings.ReplaceAll(keyword, "过", ""))
	}
	if strings.Contains(keyword, "了") {
		add(strings.ReplaceAll(keyword, "了", ""))
	}
	return out
}

// memoryContextEmpty 判断召回结果是否为空；只看结果正文，不看范围信息。
func memoryContextEmpty(c memory.Context) bool {
	return len(c.Recent) == 0 && len(c.CrossChannel) == 0 && len(c.Records) == 0
}

// formatMemory 把记忆召回结果转成结构化信封：近期、跨通道、摘要分组保留角色与时间。
func formatMemory(c memory.Context, tool, args string, scope *toolResultScope, now time.Time, windowLabel string) string {
	res := newToolResult(tool, args, scope, now)
	res.addGroup("近期", messageItems(c.Recent))
	res.addGroup("跨通道", messageItems(c.CrossChannel))
	res.addGroup("摘要", recordItems(c.Records))
	if windowLabel != "" {
		res.Note = "时间窗：" + windowLabel
	}
	if res.Status == toolResultStatusEmpty {
		if res.Note != "" {
			res.Note += "；"
		}
		res.Note += "没有相关记忆"
	}
	return res.encode()
}

// messageItems 把消息投影成结果条目；证据用事件 ID，回复链只有事件里才有。
func messageItems(messages []memory.Message) []toolResultItem {
	items := make([]toolResultItem, 0, len(messages))
	for _, m := range messages {
		items = append(items, toolResultItem{
			Role:     dirRole(m.Dir),
			Name:     m.FromName,
			UID:      m.FromID,
			Channel:  string(m.Channel),
			Private:  m.Private,
			At:       shortTime(m.At),
			Text:     m.Text,
			Evidence: m.EventID,
		})
	}
	return items
}

// recordItems 保留摘要的生效时间、来源事件与评分，供模型判断可信度。
func recordItems(records []memory.Record) []toolResultItem {
	items := make([]toolResultItem, 0, len(records))
	for _, r := range records {
		items = append(items, toolResultItem{
			Role:     "摘要",
			Channel:  string(r.Channel),
			Private:  r.Private,
			At:       shortTime(r.ValidFrom),
			Text:     r.Summary,
			Evidence: strings.Join(r.EventIDs, ","),
			Meta:     fmt.Sprintf("重要%.2f 置信%.2f", r.Importance, r.Confidence),
		})
	}
	return items
}

// formatKnowledge 把知识命中转成结构化信封：ID 作名字，来源作证据。
func formatKnowledge(c knowledge.Context, tool, args string, scope *toolResultScope, now time.Time) string {
	res := newToolResult(tool, args, scope, now)
	items := make([]toolResultItem, 0, len(c.Items))
	for _, item := range c.Items {
		entry := toolResultItem{
			Role:     "资料",
			Name:     item.ID,
			Text:     item.Text,
			Private:  item.Private,
			Evidence: item.Source,
			Meta:     knowledgeMeta(item),
		}
		if item.Origin != nil {
			entry.Channel = string(item.Origin.Channel)
		}
		items = append(items, entry)
	}
	res.addGroup("知识", items)
	if res.Status == toolResultStatusEmpty {
		res.Note = "没有命中知识"
	}
	return res.encode()
}

// knowledgeMeta 只放场景与优先级这类短字段。
func knowledgeMeta(item knowledge.Item) string {
	var parts []string
	if len(item.Scenes) > 0 {
		parts = append(parts, "场景="+strings.Join(item.Scenes, ","))
	}
	if item.Priority != 0 {
		parts = append(parts, fmt.Sprintf("优先级=%d", item.Priority))
	}
	return strings.Join(parts, " ")
}

// formatHistory 把最近事件转成结构化信封：保留角色、时间、回复链和事件 ID。
func formatHistory(rows []event.Event, tool, args string, scope *toolResultScope, now time.Time) string {
	res := newToolResult(tool, args, scope, now)
	items := make([]toolResultItem, 0, len(rows))
	for _, e := range rows {
		items = append(items, toolResultItem{
			Role:     dirRole(e.Dir),
			Name:     e.FromName,
			UID:      e.FromID,
			Channel:  string(e.Channel),
			Private:  e.Private,
			At:       shortTime(e.At),
			ReplyTo:  e.ReplyTo,
			Text:     e.Text,
			Evidence: e.ID,
		})
	}
	res.addGroup("弹幕", items)
	if res.Status == toolResultStatusEmpty {
		res.Note = "最近没有弹幕"
	}
	return res.encode()
}

// toolScopeFromMemory 把记忆范围投影成结果范围。
func toolScopeFromMemory(scope memory.Scope) *toolResultScope {
	return &toolResultScope{Channel: string(scope.Channel), Session: scope.Session, Private: scope.Private}
}

// memoryQueryKey 用实际查询词生成稳定标识；空参数表示「最近记录」。
func memoryQueryKey(query string) string {
	if strings.TrimSpace(query) == "" {
		return "recent"
	}
	return query
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
