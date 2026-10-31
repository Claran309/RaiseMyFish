// Package provider 定义单次模型调用接口，重试统一交Brain处理。
package provider

import (
	"context"
	"fmt"
)

// Message 不包含工具定义或任意平台元数据。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request 是P0纯文本请求。
type Request struct {
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature float64
}

// Usage 的nil字段表示服务端未提供统计。
type Usage struct {
	InputTokens  *int
	OutputTokens *int
}

// Response 保留可用统计，空文本由Brain统一决定是否补一次。
type Response struct {
	Text  string
	Usage Usage
	Model string
}

// Provider 每次Chat最多执行一次HTTP请求。
type Provider interface {
	Name() string
	Chat(context.Context, Request) (Response, error)
}

// Kind 是可安全记录的错误类别。
type Kind string

const (
	KindInput    Kind = "input"
	KindHTTP     Kind = "http"
	KindTimeout  Kind = "timeout"
	KindCanceled Kind = "canceled"
	KindProtocol Kind = "protocol"
	KindNetwork  Kind = "network"
)

// Error 保留原始错误链，展示时只输出类别和状态码。
type Error struct {
	Kind       Kind
	StatusCode int
	Retryable  bool
	Cause      error
}

func (e *Error) Error() string {
	return fmt.Sprintf("模型调用失败（类别%s，HTTP状态%d）", e.Kind, e.StatusCode)
}
func (e *Error) Unwrap() error { return e.Cause }
