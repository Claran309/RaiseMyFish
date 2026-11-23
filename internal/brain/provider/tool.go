package provider

import (
	"context"
	"encoding/json"
)

// ToolDefinition 是 OpenAI 兼容的 tools 声明。
//
// 该类型只用于「支持原生工具调用」的可选接口 ToolProvider；
// 既有 Provider 契约保持两消息、无 tools 的行为不变。
type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction 描述一个工具的名称、用途与参数 JSON Schema。
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall 是模型返回的一次工具调用。
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 是工具名与原始 JSON 参数。
//
// Arguments 保持字符串形态：不同网关可能返回 JSON 字符串或对象，
// 非标准格式由上层按协议错误处理，不在 Provider 内猜测。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolRequest 是一次带 tools 的模型请求。
type ToolRequest struct {
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature float64
	Tools       []ToolDefinition
}

// ToolResponse 保留正文、工具调用、用量与服务端模型标识。
type ToolResponse struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
	Model     string
}

// ToolProvider 是 Provider 的可选扩展；不实现它的旧 Provider 继续走文本协议。
type ToolProvider interface {
	Name() string
	ChatWithTools(context.Context, ToolRequest) (ToolResponse, error)
}
