// Package embedding 调用 OpenAI 兼容的嵌入接口，不读写知识库或数据库。
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultDimension 与 migrations/0006 的 vector(1024) 对齐；改维度需要新迁移。
const DefaultDimension = 1024

// DefaultTimeout 是单次 HTTP 请求的超时。
const DefaultTimeout = 30 * time.Second

const maxResponseBytes = 16 << 20

// Kind 是调用失败的稳定分类，调用方据此决定是否重试/降级。
type Kind string

const (
	KindNetwork   Kind = "network"
	KindAuth      Kind = "auth"
	KindRateLimit Kind = "rate_limit"
	KindProtocol  Kind = "protocol"
)

// Error 不包含请求体、密钥或响应正文，只保留分类与状态码。
type Error struct {
	Kind    Kind
	Status  int
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.cause }

// KindOf 返回错误的分类；非本包错误返回空字符串。
func KindOf(err error) Kind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

// Options 是客户端的必填/可选参数。
type Options struct {
	URL        string
	Model      string
	APIKey     string
	Dimension  int
	BatchSize  int
	Timeout    time.Duration
	HTTPClient *http.Client
}

type Client struct {
	url        string
	model      string
	apiKey     string
	dimension  int
	batchSize  int
	httpClient *http.Client
}

// New 校验参数并构造客户端；不发起网络请求。
func New(opts Options) (*Client, error) {
	rawURL := strings.TrimSpace(opts.URL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("embedding 地址无效")
	}
	local := parsed.Hostname() == "localhost"
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && local) {
		return nil, errors.New("embedding 地址必须是HTTPS或本机HTTP地址")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, errors.New("embedding model 不能为空")
	}
	dimension := opts.Dimension
	if dimension == 0 {
		dimension = DefaultDimension
	}
	if dimension < 1 || dimension > 2000 {
		return nil, errors.New("embedding 维度必须在1至2000之间")
	}
	batchSize := opts.BatchSize
	if batchSize == 0 {
		batchSize = 32
	}
	if batchSize < 1 || batchSize > 2048 {
		return nil, errors.New("embedding 批量大小必须在1至2048之间")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Client{
		url:        rawURL,
		model:      strings.TrimSpace(opts.Model),
		apiKey:     strings.TrimSpace(opts.APIKey),
		dimension:  dimension,
		batchSize:  batchSize,
		httpClient: client,
	}, nil
}

type requestBody struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type responseBody struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// Embed 按 batch_size 分批调用，返回与 inputs 顺序一致的向量。
func (c *Client) Embed(ctx context.Context, inputs []string) ([][]float64, error) {
	if c == nil {
		return nil, errors.New("embedding 客户端不能为空")
	}
	if ctx == nil {
		return nil, errors.New("embedding 调用ctx不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return [][]float64{}, nil
	}
	out := make([][]float64, 0, len(inputs))
	for start := 0; start < len(inputs); start += c.batchSize {
		end := start + c.batchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		vectors, err := c.embedBatch(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vectors...)
	}
	return out, nil
}

func (c *Client) embedBatch(ctx context.Context, inputs []string) ([][]float64, error) {
	body, err := json.Marshal(requestBody{Model: c.model, Input: inputs, Dimensions: c.dimension})
	if err != nil {
		return nil, &Error{Kind: KindProtocol, Message: "编码embedding请求失败", cause: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindProtocol, Message: "构造embedding请求失败", cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Kind: KindNetwork, Message: "调用embedding服务失败", cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		kind := KindProtocol
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			kind = KindAuth
		case resp.StatusCode == http.StatusTooManyRequests:
			kind = KindRateLimit
		case resp.StatusCode >= 500:
			kind = KindNetwork
		}
		return nil, &Error{Kind: kind, Status: resp.StatusCode, Message: fmt.Sprintf("embedding服务返回HTTP %d", resp.StatusCode)}
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes))
	var parsed responseBody
	if err := decoder.Decode(&parsed); err != nil {
		return nil, &Error{Kind: KindProtocol, Message: "解析embedding响应失败", cause: err}
	}
	if len(parsed.Data) != len(inputs) {
		return nil, &Error{Kind: KindProtocol, Message: "embedding响应条数与请求不一致"}
	}
	vectors := make([][]float64, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		if len(item.Embedding) != c.dimension {
			return nil, &Error{Kind: KindProtocol, Message: fmt.Sprintf("embedding维度不符：期望%d，实际%d", c.dimension, len(item.Embedding))}
		}
		for _, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, &Error{Kind: KindProtocol, Message: "embedding包含非法数值"}
			}
		}
		vectors = append(vectors, append([]float64(nil), item.Embedding...))
	}
	return vectors, nil
}
