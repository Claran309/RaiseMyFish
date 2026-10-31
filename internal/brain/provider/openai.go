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
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}
type chatChoice struct {
	Message struct {
		Content   *string `json:"content"`
		ToolCalls []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
	} `json:"message"`
}
type chatResponse struct {
	// 先解析账本统计，再验证正文结构，避免格式错误把已知费用变成未知。
	Choices json.RawMessage `json:"choices"`
	Usage   struct {
		Input  *int `json:"prompt_tokens"`
		Output *int `json:"completion_tokens"`
	} `json:"usage"`
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		return response, &Error{Kind: KindInput, Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if p.key != "" {
		req.Header.Set("Authorization", "Bearer "+p.key)
	}
	res, err := p.client.Do(req)
	if err != nil {
		return response, classify(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil && result == nil {
			result = &Error{Kind: KindProtocol, Cause: err}
		}
	}()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return response, &Error{Kind: KindHTTP, StatusCode: res.StatusCode, Retryable: res.StatusCode >= 500 && res.StatusCode <= 599}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return response, classify(err)
	}
	if len(body) > maxResponseBytes {
		return response, &Error{Kind: KindProtocol}
	}
	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return response, &Error{Kind: KindProtocol}
	}
	for _, n := range []*int{decoded.Usage.Input, decoded.Usage.Output} {
		if n != nil && (*n < 0 || int64(*n) > 2147483647) {
			return response, &Error{Kind: KindProtocol}
		}
	}
	response.Usage = Usage{InputTokens: decoded.Usage.Input, OutputTokens: decoded.Usage.Output}
	var choices []chatChoice
	if err := json.Unmarshal(decoded.Choices, &choices); err != nil {
		return response, &Error{Kind: KindProtocol}
	}
	if len(choices) != 1 || len(choices[0].Message.ToolCalls) > 0 {
		return response, &Error{Kind: KindProtocol}
	}
	if choices[0].Message.Content != nil {
		response.Text = *choices[0].Message.Content
	}
	return response, nil
}
