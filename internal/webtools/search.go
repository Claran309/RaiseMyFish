// Package webtools 提供 ReAct 只读工具的外部服务客户端（当前只有搜索）。
//
// 支持三种协议：
//   - generic：POST {"query": "..."} → {"results": [{"title","snippet","url"}]}（默认）
//   - serper：POST {"q": "..."} + X-API-KEY → {"organic": [{"title","snippet","link"}]}
//   - bing：GET ?q=... + Ocp-Apim-Subscription-Key → {"webPages": {"value": [...]}}
//
// 未配置搜索源时返回说明而不是编造结果；瞬时失败最多重试 1 次；
// 配置 allowed_domains 时只保留域名命中的结果。
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
	// maxAttempts 是含首次在内的总尝试次数：失败最多重试 1 次。
	maxAttempts = 2
	// bingResultCount 是 Bing 单次返回条数（平台上限 50）。
	bingResultCount = 10
)

// Searcher 把查询词变成一段可直接喂给模型的文本。
type Searcher interface {
	Search(ctx context.Context, query string) (string, error)
}

// Options 是 HTTP 搜索源的装配参数；URL 为空表示未配置。
type Options struct {
	URL string
	// Key 按协议放入对应请求头（generic: Bearer / serper: X-API-KEY / bing: Ocp-Apim-Subscription-Key）。
	Key string
	// Protocol 是搜索协议：generic（默认）/ serper / bing。
	Protocol string
	// AllowedDomains 是结果 URL 域名白名单（域名相同或子域）；空表示不限。
	AllowedDomains []string
	Timeout        time.Duration
	// Client 仅用于测试注入；nil 时按 Timeout 新建。
	Client *http.Client
}

// HTTP 按所选协议调用搜索服务。
type HTTP struct {
	url      string
	key      string
	protocol string
	allowed  []string
	client   *http.Client
}

var _ Searcher = (*HTTP)(nil)

// NewHTTP 校验端点/协议/白名单并装配客户端；空 URL 合法，Search 时返回「未配置」。
func NewHTTP(opts Options) (*HTTP, error) {
	raw := strings.TrimSpace(opts.URL)
	if raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "http" && u.Scheme != "https") {
			return nil, errors.New("搜索服务地址必须是HTTP或HTTPS地址且不能包含用户信息或查询参数")
		}
	}
	protocol := strings.ToLower(strings.TrimSpace(opts.Protocol))
	if protocol == "" {
		protocol = "generic"
	}
	switch protocol {
	case "generic", "serper", "bing":
	default:
		return nil, errors.New("搜索协议必须是 generic、serper 或 bing")
	}
	allowed := make([]string, 0, len(opts.AllowedDomains))
	for _, rawDomain := range opts.AllowedDomains {
		domain, ok := normalizeDomain(rawDomain)
		if !ok {
			return nil, errors.New("搜索域名白名单格式无效")
		}
		allowed = append(allowed, domain)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &HTTP{url: raw, key: strings.TrimSpace(opts.Key), protocol: protocol, allowed: allowed, client: client}, nil
}

type searchRequest struct {
	Query string `json:"query"`
}

type serperRequest struct {
	Q string `json:"q"`
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
	var parsed searchResponse
	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, retryable, err := h.attempt(ctx, query)
		if err == nil {
			parsed = result
			break
		}
		// 只对瞬时失败重试一次；4xx/解析错误立刻返回，避免放大风控与浪费配额。
		if attempt+1 >= maxAttempts || !retryable || ctx.Err() != nil {
			return "", err
		}
	}
	return formatResults(filterResults(parsed.Results, h.allowed)), nil
}

// attempt 发一次请求；retryable 表示该错误是否值得再试。
func (h *HTTP) attempt(ctx context.Context, query string) (searchResponse, bool, error) {
	req, err := h.newRequest(ctx, query)
	if err != nil {
		return searchResponse{}, false, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return searchResponse{}, true, &safeError{"搜索请求失败", err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return searchResponse{}, true, &safeError{"读取搜索响应失败", err}
	}
	if len(data) > maxResponseBytes {
		return searchResponse{}, false, errors.New("搜索响应超过1MiB上限")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// 不回显响应正文，避免把查询内容或服务内部信息带进日志。
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError
		return searchResponse{}, retryable, fmt.Errorf("搜索服务返回状态码%d", resp.StatusCode)
	}
	parsed, err := parseResults(h.protocol, data)
	if err != nil {
		return searchResponse{}, false, err
	}
	return parsed, false, nil
}

func (h *HTTP) newRequest(ctx context.Context, query string) (*http.Request, error) {
	buildError := func(err error) error { return &safeError{"构造搜索请求失败", err} }
	switch h.protocol {
	case "serper":
		payload, err := json.Marshal(serperRequest{Q: query})
		if err != nil {
			return nil, buildError(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(payload))
		if err != nil {
			return nil, buildError(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if h.key != "" {
			req.Header.Set("X-API-KEY", h.key)
		}
		return req, nil
	case "bing":
		endpoint, err := url.Parse(h.url)
		if err != nil {
			return nil, buildError(err)
		}
		values := endpoint.Query()
		values.Set("q", query)
		values.Set("count", fmt.Sprint(bingResultCount))
		endpoint.RawQuery = values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, buildError(err)
		}
		req.Header.Set("Accept", "application/json")
		if h.key != "" {
			req.Header.Set("Ocp-Apim-Subscription-Key", h.key)
		}
		return req, nil
	default:
		payload, err := json.Marshal(searchRequest{Query: query})
		if err != nil {
			return nil, buildError(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(payload))
		if err != nil {
			return nil, buildError(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if h.key != "" {
			req.Header.Set("Authorization", "Bearer "+h.key)
		}
		return req, nil
	}
}

// parseResults 按协议把响应归一成通用结果结构。
func parseResults(protocol string, data []byte) (searchResponse, error) {
	switch protocol {
	case "serper":
		var payload struct {
			Organic []struct {
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
				Link    string `json:"link"`
			} `json:"organic"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return searchResponse{}, &safeError{"解析搜索响应失败", err}
		}
		out := searchResponse{Results: make([]searchResult, 0, len(payload.Organic))}
		for _, item := range payload.Organic {
			out.Results = append(out.Results, searchResult{Title: item.Title, Snippet: item.Snippet, URL: item.Link})
		}
		return out, nil
	case "bing":
		var payload struct {
			WebPages struct {
				Value []struct {
					Name    string `json:"name"`
					Snippet string `json:"snippet"`
					URL     string `json:"url"`
				} `json:"value"`
			} `json:"webPages"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return searchResponse{}, &safeError{"解析搜索响应失败", err}
		}
		out := searchResponse{Results: make([]searchResult, 0, len(payload.WebPages.Value))}
		for _, item := range payload.WebPages.Value {
			out.Results = append(out.Results, searchResult{Title: item.Name, Snippet: item.Snippet, URL: item.URL})
		}
		return out, nil
	default:
		var parsed searchResponse
		if err := json.Unmarshal(data, &parsed); err != nil {
			return searchResponse{}, &safeError{"解析搜索响应失败", err}
		}
		return parsed, nil
	}
}

// normalizeDomain 规整白名单条目：小写域名，不接受协议、端口、路径与通配符。
func normalizeDomain(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/:@*?# \t\r\n\x00[]") {
		return "", false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", false
			}
		}
	}
	return value, true
}

// filterResults 只保留域名命中白名单的结果；空白名单不过滤。
func filterResults(results []searchResult, allowed []string) []searchResult {
	if len(allowed) == 0 {
		return results
	}
	out := make([]searchResult, 0, len(results))
	for _, r := range results {
		if domainAllowed(r.URL, allowed) {
			out = append(out, r)
		}
	}
	return out
}

// domainAllowed 支持精确域名与子域（bilibili.com 命中 www.bilibili.com）。
func domainAllowed(rawURL string, allowed []string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, domain := range allowed {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
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
