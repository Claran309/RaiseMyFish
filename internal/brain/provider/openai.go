package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 2 << 20

// OpenAI 保存单一可信端点与凭据，不记录请求/响应正文。
type OpenAI struct {
	name     string
	endpoint string
	key      string
	client   *http.Client
}

// NewOpenAI 克隆client，禁重定向并设置超时；不修改调用方client。
func NewOpenAI(name, baseURL, apiKey string, client *http.Client, timeout time.Duration) (*OpenAI, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(name) == "" || strings.ContainsAny(name, ":\r\n") || strings.ContainsAny(apiKey, "\r\n") || client == nil || timeout <= 0 {
		return nil, &Error{Kind: KindInput}
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, &Error{Kind: KindInput}
	}
	copyClient := *client
	copyClient.Timeout = timeout
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return &OpenAI{name: name, endpoint: u.String(), key: apiKey, client: &copyClient}, nil
}

// Name 返回配置中的Provider名字，不包含密钥或URL。
func (p *OpenAI) Name() string { return p.name }

type chatRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	MaxTokens   int              `json:"max_tokens"`
	Temperature float64          `json:"temperature"`
	Stream      bool             `json:"stream"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
}

type chatMessage struct {
	Content   *string    `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatUsageDetails struct {
	Cached *int `json:"cached_tokens"`
}

type chatUsage struct {
	Input  *int `json:"prompt_tokens"`
	Output *int `json:"completion_tokens"`
	// 缓存命中只有服务端明确返回时才记录，不从其它字段反推。
	PromptDetails *chatUsageDetails `json:"prompt_tokens_details"`
}

// cached 返回服务端声明的缓存命中 token；没有该字段时保持 nil（unknown）。
func (u chatUsage) cached() *int {
	if u.PromptDetails == nil {
		return nil
	}
	return u.PromptDetails.Cached
}

type chatResponse struct {
	Model json.RawMessage `json:"model"`
	// 先解析账本统计，再验证正文结构，避免格式错误把已知费用变成未知。
	Choices json.RawMessage `json:"choices"`
	Usage   chatUsage       `json:"usage"`
}

// chatResult 是共享 HTTP 路径的解析结果；旧 Chat 只取 response，新路径同时取 toolCalls。
type chatResult struct {
	response  Response
	toolCalls []ToolCall
}

func classify(err error) *Error {
	if errors.Is(err, context.Canceled) {
		return &Error{Kind: KindCanceled, Cause: err}
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return &Error{Kind: KindTimeout, Retryable: true, Cause: err}
	}
	return &Error{Kind: KindNetwork, Retryable: true, Cause: err}
}

// Chat 执行一次有界请求，服务端错误体从不进入错误或日志。
func (p *OpenAI) Chat(ctx context.Context, in Request) (response Response, result error) {
	if ctx == nil {
		return response, &Error{Kind: KindInput}
	}
	if err := ctx.Err(); err != nil {
		return response, classify(err)
	}
	if strings.TrimSpace(in.Model) == "" || in.MaxTokens <= 0 || len(in.Messages) != 2 || in.Messages[0].Role != "system" || in.Messages[1].Role != "user" || math.IsNaN(in.Temperature) || math.IsInf(in.Temperature, 0) || in.Temperature < 0 || in.Temperature > 2 {
		return response, &Error{Kind: KindInput}
	}
	data, err := json.Marshal(chatRequest{Model: in.Model, Messages: in.Messages, MaxTokens: in.MaxTokens, Temperature: in.Temperature})
	if err != nil {
		return response, &Error{Kind: KindInput, Cause: err}
	}
	got, err := p.doChat(ctx, data, false)
	if err != nil {
		// 协议错误可能已经解析出可用 usage；保留给 Brain 记账，不把已知费用变成未知。
		return got.response, err
	}
	return got.response, nil
}

// ChatWithTools 是 Provider 的可选原生工具调用扩展。
//
// 约束：
//   - 首条消息必须是 system；tool 消息必须带 tool_call_id；
//   - 工具名不能为空，parameters 必须是合法 JSON；
//   - 旧 Chat 路径完全不受影响。
func (p *OpenAI) ChatWithTools(ctx context.Context, in ToolRequest) (out ToolResponse, result error) {
	if ctx == nil {
		return out, &Error{Kind: KindInput}
	}
	if err := ctx.Err(); err != nil {
		return out, classify(err)
	}
	if strings.TrimSpace(in.Model) == "" || in.MaxTokens <= 0 || len(in.Messages) == 0 || in.Messages[0].Role != "system" || math.IsNaN(in.Temperature) || math.IsInf(in.Temperature, 0) || in.Temperature < 0 || in.Temperature > 2 {
		return out, &Error{Kind: KindInput}
	}
	for _, msg := range in.Messages {
		switch msg.Role {
		case "system", "user", "assistant":
		case "tool":
			if strings.TrimSpace(msg.ToolCallID) == "" {
				return out, &Error{Kind: KindInput}
			}
		default:
			return out, &Error{Kind: KindInput}
		}
		if len(msg.ToolCalls) > 0 {
			for _, call := range msg.ToolCalls {
				if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" {
					return out, &Error{Kind: KindInput}
				}
			}
		}
	}
	for i := range in.Tools {
		if in.Tools[i].Type == "" {
			in.Tools[i].Type = "function"
		}
		if in.Tools[i].Type != "function" || strings.TrimSpace(in.Tools[i].Function.Name) == "" {
			return out, &Error{Kind: KindInput}
		}
		if len(in.Tools[i].Function.Parameters) > 0 && !json.Valid(in.Tools[i].Function.Parameters) {
			return out, &Error{Kind: KindInput}
		}
	}
	data, err := json.Marshal(chatRequest{
		Model:       in.Model,
		Messages:    in.Messages,
		MaxTokens:   in.MaxTokens,
		Temperature: in.Temperature,
		Tools:       in.Tools,
	})
	if err != nil {
		return out, &Error{Kind: KindInput, Cause: err}
	}
	got, err := p.doChat(ctx, data, true)
	if err != nil {
		// 与 Chat 一致：即使协议失败，也把已解析的 usage/model 交给调用方记账。
		return ToolResponse{Text: got.response.Text, ToolCalls: got.toolCalls, Usage: got.response.Usage, Model: got.response.Model}, err
	}
	return ToolResponse{Text: got.response.Text, ToolCalls: got.toolCalls, Usage: got.response.Usage, Model: got.response.Model}, nil
}

// doChat 是 Chat 与 ChatWithTools 共用的单次 HTTP 路径。
// allowToolCalls 控制是否接受 tool_calls；旧路径仍将其视为协议错误。
func (p *OpenAI) doChat(ctx context.Context, data []byte, allowToolCalls bool) (result chatResult, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		return result, &Error{Kind: KindInput, Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if p.key != "" {
		req.Header.Set("Authorization", "Bearer "+p.key)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return result, classify(err)
	}
	defer func() {
		if closeErr := res.Body.Close(); closeErr != nil && err == nil {
			err = &Error{Kind: KindProtocol, Cause: closeErr}
		}
	}()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// 仅原生工具路径把 400/404/405/422 视为「不支持 tools」的候选；
		// 认证、限流、5xx 仍按原错误分类，避免误降级。
		if allowToolCalls && (res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed || res.StatusCode == http.StatusUnprocessableEntity) {
			return result, &Error{Kind: KindUnsupported, StatusCode: res.StatusCode}
		}
		return result, &Error{Kind: KindHTTP, StatusCode: res.StatusCode, Retryable: res.StatusCode >= 500 && res.StatusCode <= 599}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return result, classify(err)
	}
	if len(body) > maxResponseBytes {
		return result, &Error{Kind: KindProtocol}
	}
	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return result, &Error{Kind: KindProtocol}
	}
	for _, n := range []*int{decoded.Usage.Input, decoded.Usage.Output, decoded.Usage.cached()} {
		if n != nil && (*n < 0 || int64(*n) > 2147483647) {
			return result, &Error{Kind: KindProtocol}
		}
	}
	result.response.Usage = Usage{InputTokens: decoded.Usage.Input, OutputTokens: decoded.Usage.Output, CachedInputTokens: decoded.Usage.cached()}
	// model是可选审计元数据；字段异常不影响已解析正文和用量。
	if len(decoded.Model) > 0 {
		var model string
		if json.Unmarshal(decoded.Model, &model) == nil && len(model) <= 256 && !strings.ContainsAny(model, "\r\n") {
			result.response.Model = model
		}
	}
	var choices []chatChoice
	if err := json.Unmarshal(decoded.Choices, &choices); err != nil {
		return result, &Error{Kind: KindProtocol}
	}
	if len(choices) != 1 {
		return result, &Error{Kind: KindProtocol}
	}
	if len(choices[0].Message.ToolCalls) > 0 && !allowToolCalls {
		return result, &Error{Kind: KindProtocol}
	}
	result.toolCalls = choices[0].Message.ToolCalls
	if choices[0].Message.Content != nil {
		result.response.Text = *choices[0].Message.Content
	}
	return result, nil
}
