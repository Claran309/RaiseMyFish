// Package webtools 提供 ReAct 只读工具的外部服务客户端（当前只有搜索）。
//
// 协议是通用形态：POST {"query": "..."} → {"results": [{"title","snippet","url"}]}，
// 可接 Serper/Bing/自建服务；未配置搜索源时返回说明而不是编造结果。
package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// defaultTimeout 是单次搜索的硬上限；调用方可用 Options.Timeout 覆盖。
	defaultTimeout = 10 * time.Second
	// maxResponseBytes 防止异常服务把内存打满（1MiB）。
	maxResponseBytes = 1 << 20
	// maxResultRunes 是喂给模型的搜索文本上限；超出直接截断。
	maxResultRunes = 500
)

// Searcher 把查询词变成一段可直接喂给模型的文本。
type Searcher interface {
	Search(ctx context.Context, query string) (string, error)
}

// Options 是 HTTP 搜索源的装配参数；URL 为空表示未配置。
type Options struct {
	URL     string
	Key     string // 可选：作为 Authorization: Bearer
	Timeout time.Duration
	// Client 仅用于测试注入；nil 时按 Timeout 新建。
	Client *http.Client
}

// HTTP 按通用协议调用搜索服务。
type HTTP struct {
	url    string
	key    string
	client *http.Client
}

var _ Searcher = (*HTTP)(nil)

// NewHTTP 校验端点并装配客户端；空 URL 合法，Search 时返回「未配置」。
func NewHTTP(opts Options) (*HTTP, error) {
	raw := strings.TrimSpace(opts.URL)
	if raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "http" && u.Scheme != "https") {
			return nil, errors.New("搜索服务地址必须是HTTP或HTTPS地址且不能包含用户信息或查询参数")
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &HTTP{url: raw, key: strings.TrimSpace(opts.Key), client: client}, nil
}

type searchRequest struct {
	Query string `json:"query"`
}

type searchResponse struct {
	Results []searchResult `json:"results"`
}

type searchResult struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	URL     string `json:"url"`
}

// Search 返回格式化后的结果文本；未配置搜索源时返回说明而不是错误。
func (h *HTTP) Search(ctx context.Context, query string) (string, error) {
	if ctx == nil {
		return "", errors.New("搜索上下文不能为空")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("查询词不能为空")
	}
	if h == nil || h.url == "" {
		return "未配置搜索源，无法联网查证", nil
	}
	payload, err := json.Marshal(searchRequest{Query: query})
	if err != nil {
		return "", &safeError{"构造搜索请求失败", err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(payload))
	if err != nil {
		return "", &safeError{"构造搜索请求失败", err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if h.key != "" {
		req.Header.Set("Authorization", "Bearer "+h.key)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", &safeError{"搜索请求失败", err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", &safeError{"读取搜索响应失败", err}
	}
	if len(data) > maxResponseBytes {
		return "", errors.New("搜索响应超过1MiB上限")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// 不回显响应正文，避免把查询内容或服务内部信息带进日志。
		return "", fmt.Errorf("搜索服务返回状态码%d", resp.StatusCode)
	}
	var parsed searchResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", &safeError{"解析搜索响应失败", err}
	}
	return formatResults(parsed.Results), nil
}

// formatResults 把结构化结果压成一段纯文本，并按 rune 截断。
func formatResults(results []searchResult) string {
	var b strings.Builder
	n := 0
	for _, r := range results {
		title := strings.TrimSpace(r.Title)
		snippet := strings.TrimSpace(r.Snippet)
		if title == "" && snippet == "" {
			continue
		}
		n++
		if n > 1 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. ", n)
		if title != "" {
			b.WriteString(title)
			if snippet != "" {
				b.WriteString("：")
			}
		}
		b.WriteString(snippet)
		if link := strings.TrimSpace(r.URL); link != "" {
			b.WriteString("（")
			b.WriteString(link)
			b.WriteString("）")
		}
	}
	if n == 0 {
		return "没有搜到相关结果"
	}
	return clipRunes(b.String(), maxResultRunes)
}

func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// safeError 让日志只看到稳定文案，同时保留 errors.Is/As 可追溯的原因链。
type safeError struct {
	message string
	cause   error
}

func (e *safeError) Error() string { return e.message }
func (e *safeError) Unwrap() error { return e.cause }
