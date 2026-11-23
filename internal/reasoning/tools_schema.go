package reasoning

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 本文件是原生 tool calling 的工具参数 Schema 与校验。
//
// 文本 ReAct 仍按原来的自由文本 args 工作；这里的校验只用于
// ToolProvider 的原生 JSON 参数路径，不能反过来限制旧协议。

// ToolArgError 是参数校验失败的结构化错误；日志只记字段与原因，不记完整参数正文。
type ToolArgError struct {
	Field  string
	Reason string
}

func (e *ToolArgError) Error() string {
	if e.Field == "" {
		return "工具参数错误：" + e.Reason
	}
	return fmt.Sprintf("工具参数错误（%s）：%s", e.Field, e.Reason)
}

type toolProperty struct {
	Type      string   `json:"type"`
	Enum      []string `json:"enum,omitempty"`
	MaxLength int      `json:"maxLength,omitempty"`
}

type toolSchema struct {
	Type       string                  `json:"type"`
	Properties map[string]toolProperty `json:"properties"`
	Required   []string                `json:"required"`
}

// toolArgSchemas 为实际注册的工具提供最小可用 Schema。
// 统一使用 args 字符串，与现有工具实现（自由文本参数）保持一致；
// 后续若要为单个工具拆结构化字段，可在这里替换而不改 Toolbox。
var toolArgSchemas = map[string]json.RawMessage{
	ToolMemorySearch:    json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"昵称、关键词或时间窗（昨天/昨晚/前天/上次），可留空看最近记录"}}}`),
	ToolKnowledgeSearch: json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"要查的词或整句，例如梗名、设定名"}}}`),
	ToolPresence:        json.RawMessage(`{"type":"object","properties":{}}`),
	ToolHistory:         json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"条数（1-20，默认10）"}}}`),
	ToolViewerMemory:    json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"观众昵称或 UID"}}}`),
	ToolViewerSearch:    json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"观众昵称、别名或近似名"}}}`),
	ToolReplyHistory:    json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"要回看的回复对象或关键词"}}}`),
	ToolLiveStatus:      json.RawMessage(`{"type":"object","properties":{}}`),
	ToolQuota:           json.RawMessage(`{"type":"object","properties":{}}`),
	ToolNow:             json.RawMessage(`{"type":"object","properties":{}}`),
	ToolVision:          json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"要看的内容或问题"}}}`),
	ToolWebSearch:       json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"搜索关键词"}}}`),
	ToolWatchLive:       json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"可选：想看的内容或问题"}}}`),
	ToolSendDanmaku:     json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"要发送的弹幕正文，1-40字","maxLength":40}}}`),
	ToolGift:            json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"礼物名与数量，例如「小心心 1」"}}}`),
	ToolFollow:          json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"要关注的人或留空表示当前主播"}}}`),
	ToolLike:            json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"点赞对象或留空表示当前直播间"}}}`),
	ToolDM:              json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"收件人与正文，例如「昵称：内容」"}}}`),
	ToolPostDynamic:     json.RawMessage(`{"type":"object","properties":{"args":{"type":"string","description":"动态正文"}}}`),
}

// toolSchemaFor 返回工具 Schema；未登记的工具使用最小 object schema。
func toolSchemaFor(name string) json.RawMessage {
	if raw, ok := toolArgSchemas[name]; ok {
		return append(json.RawMessage(nil), raw...)
	}
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// ValidateToolArgs 校验原生路径传入的 JSON 参数。
//
// 规则刻意保持最小：顶层 object、required、string/number/integer/boolean、
// enum 与 maxLength；不实现完整 JSON Schema，也不把校验失败当成工具执行失败。
func ValidateToolArgs(name, args string) error {
	var raw map[string]json.RawMessage
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		raw = map[string]json.RawMessage{}
	} else if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return &ToolArgError{Reason: "参数不是合法 JSON 对象"}
	}
	var spec toolSchema
	if err := json.Unmarshal(toolSchemaFor(name), &spec); err != nil {
		return &ToolArgError{Reason: "工具 Schema 配置无效"}
	}
	for _, required := range spec.Required {
		if _, ok := raw[required]; !ok {
			return &ToolArgError{Field: required, Reason: "缺少必填参数"}
		}
	}
	for field, value := range raw {
		prop, ok := spec.Properties[field]
		if !ok {
			continue
		}
		switch prop.Type {
		case "", "string":
			var s string
			if err := json.Unmarshal(value, &s); err != nil {
				return &ToolArgError{Field: field, Reason: "需要字符串"}
			}
			if prop.MaxLength > 0 && len([]rune(s)) > prop.MaxLength {
				return &ToolArgError{Field: field, Reason: fmt.Sprintf("超过 %d 字", prop.MaxLength)}
			}
			if len(prop.Enum) > 0 {
				ok := false
				for _, allowed := range prop.Enum {
					if s == allowed {
						ok = true
						break
					}
				}
				if !ok {
					return &ToolArgError{Field: field, Reason: "不在允许值内"}
				}
			}
		case "number":
			var n float64
			if err := json.Unmarshal(value, &n); err != nil {
				return &ToolArgError{Field: field, Reason: "需要数字"}
			}
		case "integer":
			var n int
			if err := json.Unmarshal(value, &n); err != nil {
				return &ToolArgError{Field: field, Reason: "需要整数"}
			}
		case "boolean":
			var b bool
			if err := json.Unmarshal(value, &b); err != nil {
				return &ToolArgError{Field: field, Reason: "需要布尔值"}
			}
		}
	}
	return nil
}

// ToolArgsText 把原生 JSON 参数还原成现有工具实现使用的自由文本 args。
//
// 兼容三种形态：{"args":"..."}、单字段对象（取第一个字符串值）、其它对象（紧凑 JSON）。
// 这样即使某个工具以后扩了结构化字段，旧 Tool.Run 也不会被阻断。
func ToolArgsText(name, args string) (string, error) {
	if err := ValidateToolArgs(name, args); err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "{}" {
		return "", nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return "", &ToolArgError{Reason: "参数不是合法 JSON 对象"}
	}
	if value, ok := raw["args"]; ok {
		var s string
		if err := json.Unmarshal(value, &s); err == nil {
			return s, nil
		}
	}
	if len(raw) == 1 {
		for _, value := range raw {
			var s string
			if err := json.Unmarshal(value, &s); err == nil {
				return s, nil
			}
		}
	}
	return trimmed, nil
}
