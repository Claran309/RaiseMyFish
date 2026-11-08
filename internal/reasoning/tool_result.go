package reasoning

// 本文件实现工具结果的结构化信封与分段渲染。
//
// 召回类工具返回 schema=reasoning.tool_result/v1 的 JSON；Toolbox.Run 在结果进入
// ReAct prompt 之前按 400 字预算分段取舍：先给每个来源一条位置，再补后续条目，
// 避免旧实现「整串拼接后尾部统一截断」把角色、时间、证据和跨通道结果一起裁掉。
//
// 非信封输出（状态、额度、画面描述等）仍走 clip 回退，行为不变。

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
)

// toolResultSchema 是工具结果信封的协议标记（不是 contracts，仅内部渲染约定）。
const toolResultSchema = "reasoning.tool_result/v1"

// 工具结果状态：ok=有结果，empty=查询成功但没查到。
const (
	toolResultStatusOK    = "ok"
	toolResultStatusEmpty = "empty"
)

const (
	// toolItemTextRunes 是单条结果正文进入 prompt 的上限。
	// 先按条目取舍，保证一条长文不会挤掉其他来源。
	toolItemTextRunes = 60
	// toolNoticeReserveRunes 给「…另有N条未展示」预留位置。
	toolNoticeReserveRunes = 16
	// toolMinItemRunes 是分段裁剪时至少给一条结果留下的长度。
	toolMinItemRunes = 24
)

// toolResultScope 是本条查询的可信范围，渲染成「通道/会话/公开性」。
type toolResultScope struct {
	Channel string `json:"channel,omitempty"`
	Session string `json:"session,omitempty"`
	Private bool   `json:"private,omitempty"`
}

// toolResultItem 是单条结果；角色、时间、回复链和证据分开保留，不再拼成一个字符串。
type toolResultItem struct {
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
	UID      string `json:"uid,omitempty"`
	Channel  string `json:"channel,omitempty"`
	Private  bool   `json:"private,omitempty"`
	At       string `json:"at,omitempty"`
	ReplyTo  string `json:"reply_to,omitempty"`
	Text     string `json:"text"`
	Evidence string `json:"evidence,omitempty"`
	Meta     string `json:"meta,omitempty"`
}

// toolResultGroup 是一类来源（近期/跨通道/摘要/知识/弹幕/该观众发言/本鱼回复）。
type toolResultGroup struct {
	Name  string           `json:"name"`
	Items []toolResultItem `json:"items"`
}

// toolResult 是召回类工具的统一输出信封。
type toolResult struct {
	Schema  string            `json:"schema"`
	Status  string            `json:"status"`
	QueryID string            `json:"query_id"`
	Scope   *toolResultScope  `json:"scope,omitempty"`
	AsOf    string            `json:"as_of,omitempty"`
	Groups  []toolResultGroup `json:"groups,omitempty"`
	Note    string            `json:"note,omitempty"`
}

// newToolResult 构造一个空结果信封；加入条目后状态自动变为 ok。
func newToolResult(tool, args string, scope *toolResultScope, now time.Time) toolResult {
	return toolResult{
		Schema:  toolResultSchema,
		Status:  toolResultStatusEmpty,
		QueryID: toolQueryID(tool, args),
		Scope:   scope,
		AsOf:    now.Format("2006-01-02 15:04:05"),
	}
}

// addGroup 追加非空来源分组；空分组不出现，免得 prompt 里全是空标题。
func (r *toolResult) addGroup(name string, items []toolResultItem) {
	if len(items) == 0 {
		return
	}
	r.Groups = append(r.Groups, toolResultGroup{Name: name, Items: items})
	r.Status = toolResultStatusOK
}

// encode 序列化信封；字段都是字符串/布尔/切片，不会失败。
func (r toolResult) encode() string {
	b, _ := json.Marshal(r)
	return string(b)
}

// toolQueryID 生成确定且不含用户原文的查询标识：工具名 + 参数哈希。
func toolQueryID(tool, args string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(tool))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(args))
	return fmt.Sprintf("%s:%08x", tool, h.Sum32())
}

// renderToolResult 识别信封并按预算渲染；非信封输出返回 ok=false 由调用方回退。
func renderToolResult(raw string, budget int) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}
	var r toolResult
	if err := json.Unmarshal([]byte(trimmed), &r); err != nil || r.Schema != toolResultSchema {
		return "", false
	}
	return r.render(budget), true
}

// render 输出「状态/查询/范围/as_of + 分段结果」。
func (r toolResult) render(budget int) string {
	head := []string{fmt.Sprintf("状态=%s | 查询=%s | 范围=%s | as_of=%s",
		r.Status, r.QueryID, r.scopeLabel(), r.AsOf)}
	if r.Note != "" {
		head = append(head, r.Note)
	}
	entries := r.entries()
	if len(entries) == 0 {
		return strings.Join(head, "\n")
	}
	if out, complete := renderToolEntries(head, entries, budget, 0); complete {
		return out
	}
	out, _ := renderToolEntries(head, entries, budget, toolNoticeReserveRunes)
	return out
}

// scopeLabel 把范围讲成人能读的一行。
func (r toolResult) scopeLabel() string {
	if r.Scope == nil || strings.TrimSpace(r.Scope.Channel) == "" {
		return "跨通道"
	}
	label := r.Scope.Channel
	if session := strings.TrimSpace(r.Scope.Session); session != "" {
		label += "/" + clip(session, 24)
	}
	if r.Scope.Private {
		label += "/私密"
	} else {
		label += "/公开"
	}
	return label
}

// toolEntry 是渲染队列里的一行，保留来源名用于组间轮转。
type toolEntry struct {
	Group string
	Line  string
}

// entries 在各分组之间轮转：每组先出第一条，再补第二条，
// 保证多来源结果都能进 prompt，而不是第一组占满预算、后面的组被裁没。
func (r toolResult) entries() []toolEntry {
	var out []toolEntry
	for i := 0; ; i++ {
		added := false
		for _, g := range r.Groups {
			if i >= len(g.Items) {
				continue
			}
			added = true
			out = append(out, toolEntry{Group: g.Name, Line: renderToolItem(g.Name, g.Items[i])})
		}
		if !added {
			break
		}
	}
	return out
}

// renderToolItem 把一条结果排成「[来源] 角色 名字: 正文 [时间 通道#证据 ↩回复]」。
func renderToolItem(group string, it toolResultItem) string {
	var sb strings.Builder
	if name := strings.TrimSpace(group); name != "" {
		fmt.Fprintf(&sb, "[%s] ", clip(name, 12))
	}
	role := strings.TrimSpace(it.Role)
	name := strings.TrimSpace(it.Name)
	switch {
	case role != "" && name != "":
		sb.WriteString(role)
		sb.WriteString(" ")
		sb.WriteString(name)
		sb.WriteString(": ")
	case role != "":
		sb.WriteString(role)
		sb.WriteString(": ")
	case name != "":
		sb.WriteString(name)
		sb.WriteString(": ")
	}
	sb.WriteString(clip(it.Text, toolItemTextRunes))
	if meta := renderToolMeta(it); meta != "" {
		sb.WriteString(" [")
		sb.WriteString(meta)
		sb.WriteString("]")
	}
	return sb.String()
}

// renderToolMeta 只放短字段，避免元信息把正文预算吃掉。
func renderToolMeta(it toolResultItem) string {
	var parts []string
	if it.At != "" {
		parts = append(parts, clip(it.At, 16))
	}
	src := strings.TrimSpace(it.Channel)
	if src != "" && it.Evidence != "" {
		src += "#" + clip(it.Evidence, 12)
	} else if src == "" && it.Evidence != "" {
		src = "#" + clip(it.Evidence, 12)
	}
	if src != "" {
		parts = append(parts, src)
	}
	if it.Private {
		parts = append(parts, "私密")
	}
	if it.ReplyTo != "" {
		parts = append(parts, "↩"+clip(it.ReplyTo, 12))
	}
	if it.Meta != "" {
		parts = append(parts, clip(it.Meta, 40))
	}
	return strings.Join(parts, " ")
}

// renderToolEntries 按预算装行；放不下的条目先保住可识别的片段，再标「另有N条未展示」。
func renderToolEntries(head []string, entries []toolEntry, budget, reserve int) (string, bool) {
	lines := append([]string(nil), head...)
	used := 0
	for i, line := range lines {
		if i > 0 {
			used++
		}
		used += utf8.RuneCountInString(line)
	}
	if used > budget {
		return clip(strings.Join(lines, " | "), budget), len(entries) == 0
	}

	limit := budget - reserve
	shown := 0
	complete := true
	for _, e := range entries {
		remain := limit - used - 1
		if remain <= 0 {
			complete = false
			break
		}
		if lineRunes := utf8.RuneCountInString(e.Line); lineRunes <= remain {
			lines = append(lines, e.Line)
			used += 1 + lineRunes
			shown++
			continue
		}
		if remain < toolMinItemRunes {
			complete = false
			break
		}
		// 单条超长：至少展示开头一段（clip 自带「…」），
		// 保证关键人物/时间不因整条被丢弃而消失。
		lines = append(lines, clip(e.Line, remain))
		used += 1 + remain + 1
		shown++
		complete = false
		break
	}

	if !complete {
		notice := fmt.Sprintf("…另有%d条未展示", len(entries)-shown)
		if used+1+utf8.RuneCountInString(notice) <= budget {
			lines = append(lines, notice)
		}
	}
	return strings.Join(lines, "\n"), complete && shown == len(entries)
}

// dirRole 把事件方向翻译成模型能理解的角色。
func dirRole(d event.Dir) string {
	switch d {
	case event.DirIn:
		return "观众"
	case event.DirOut:
		return "本鱼"
	default:
		return ""
	}
}

// shortTime 压缩到「月-日 时:分」；零值不显示。
func shortTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("01-02 15:04")
}
