// Package webtools 提供 ReAct 只读工具的外部服务客户端（当前只有搜索）。
//
// 支持四种协议：
//   - generic：POST {"query": "..."} → {"results": [{"title","snippet","url"}]}（默认）
//   - serper：POST {"q": "..."} + X-API-KEY → {"organic": [{"title","snippet","link"}]}
//   - bing：GET ?q=... + Ocp-Apim-Subscription-Key → {"webPages": {"value": [...]}}
//   - searxng：GET ?q=...&format=json，公共实例不返回 JSON 时退化 HTML 解析
//
// searxng 支持 search_fallbacks 多实例轮换：429/验证码/网络错误换下一个实例，
// 总尝试不超过 6 次、单实例 8 秒；全失败明确报「搜索暂不可用」。
// 未配置搜索源时返回说明而不是编造结果；配置 allowed_domains 时只保留域名命中的结果。
package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

const (
	// defaultTimeout 是单次搜索的硬上限；调用方可用 Options.Timeout 覆盖。
	defaultTimeout = 10 * time.Second
	// searxngTimeout 是单个公共实例的总超时（JSON 尝试 + HTML 兜底共用）。
	searxngTimeout = 8 * time.Second
	// maxResponseBytes 防止异常服务把内存打满（1MiB）。
	maxResponseBytes = 1 << 20
	// maxResultRunes 是喂给模型的搜索文本上限；超出直接截断。
	maxResultRunes = 500
	// maxAttempts 是旧单实例协议的重试上限：失败最多重试 1 次。
	maxAttempts = 2
	// maxInstanceAttempts 是多实例轮换的总尝试上限。
	maxInstanceAttempts = 6
	// maxSearchFallbacks 是备用实例数量上限，与配置层保持一致。
	maxSearchFallbacks = 8
	// bingResultCount 是 Bing 单次返回条数（平台上限 50）。
	bingResultCount = 10
	// maxRedirects 是允许的同 host 重定向次数；超过说明服务端在打转。
	maxRedirects = 5
	// searxngLanguage 让公共实例优先返回中文结果。
	searxngLanguage = "zh-CN"
	// searxngUserAgent 用常见浏览器 UA：公共实例的风控会拒绝 Go 默认 UA。
	searxngUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// 搜索状态文案：让模型能区分「没配置」「调用失败」「确实没结果」。
//
// 这三种状态都不能被讲成「查无此事」：未配置/失败只说明本轮拿不到证据，
// 零结果也只说明搜索源没返回，不代表事实不存在。
const (
	searchStatusUnconfigured = "搜索能力：未配置搜索源，本轮没有联网，无法查证；不要声称已联网，也不要把未配置说成「查无此事」。"
	searchStatusDone         = "搜索状态：已完成（已联网）。"
	searchStatusNoResult     = "搜索状态：已完成（已联网），但没有搜到相关结果；这只表示搜索源没有返回结果，不代表事实不存在。"
	searchStatusFailed       = "搜索状态：失败（已联网尝试但没有拿到结果；这不代表没有相关事实）"
	searchStatusUnavailable  = "搜索暂不可用（所有实例失败）"
)

// errCrossHostRedirect 表示搜索服务想把请求带到别的 host（含端口）去。
//
// O12：Go 的默认重定向只剥离 Authorization/Cookie 等，**不剥离自定义头**——
// serper 的 X-API-KEY、bing 的 Ocp-Apim-Subscription-Key 会跟着 302 跑到第三方。
var errCrossHostRedirect = errors.New("拒绝跨域重定向")

// errInsecureRedirect 表示从 HTTPS 降级到 HTTP：同 host 也不允许，密钥会明文上路。
var errInsecureRedirect = errors.New("拒绝降级到非HTTPS")

// Searcher 把查询词变成一段可直接喂给模型的文本。
type Searcher interface {
	Search(ctx context.Context, query string) (string, error)
}

// Options 是 HTTP 搜索源的装配参数；URL 为空表示未配置。
type Options struct {
	URL string
	// Fallbacks 是备用实例地址；按顺序在主实例失败后尝试。
	Fallbacks []string
	// Key 按协议放入对应请求头（generic: Bearer / serper: X-API-KEY / bing: Ocp-Apim-Subscription-Key）。
	Key string
	// Protocol 是搜索协议：generic（默认）/ serper / bing / searxng。
	Protocol string
	// AllowedDomains 是结果 URL 域名白名单（域名相同或子域）；空表示不限。
	AllowedDomains []string
	Timeout        time.Duration
	// Client 仅用于测试注入；nil 时按 Timeout 新建。
	Client *http.Client
	// Logger 记录每次实例尝试（实例/状态/耗时）；nil 时不打日志。
	Logger *zap.Logger
}

// HTTP 按所选协议调用搜索服务。
type HTTP struct {
	url       string
	fallbacks []string
	key       string
	protocol  string
	allowed   []string
	timeout   time.Duration
	logger    *zap.Logger
	client    *http.Client
}

var _ Searcher = (*HTTP)(nil)

// NewHTTP 校验端点/协议/白名单并装配客户端；空 URL 合法，Search 时返回「未配置」。
func NewHTTP(opts Options) (*HTTP, error) {
	raw := strings.TrimSpace(opts.URL)
	if raw != "" {
		if err := validateEndpoint(raw); err != nil {
			return nil, err
		}
	}
	protocol := strings.ToLower(strings.TrimSpace(opts.Protocol))
	if protocol == "" {
		protocol = "generic"
	}
	switch protocol {
	case "generic", "serper", "bing", "searxng":
	default:
		return nil, errors.New("搜索协议必须是 generic、serper、bing 或 searxng")
	}
	if len(opts.Fallbacks) > maxSearchFallbacks {
		return nil, errors.New("搜索备用地址最多8个")
	}
	fallbacks := make([]string, 0, len(opts.Fallbacks))
	for _, rawFallback := range opts.Fallbacks {
		value := strings.TrimSpace(rawFallback)
		if value == "" {
			return nil, errors.New("搜索备用地址不能为空")
		}
		if err := validateEndpoint(value); err != nil {
			return nil, fmt.Errorf("搜索备用地址无效：%w", err)
		}
		if value == raw || containsString(fallbacks, value) {
			continue
		}
		fallbacks = append(fallbacks, value)
	}
	if raw == "" && len(fallbacks) > 0 {
		return nil, errors.New("配置搜索备用地址前必须先配置搜索服务地址")
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
		if protocol == "searxng" {
			timeout = searxngTimeout
		} else {
			timeout = defaultTimeout
		}
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	// 复制一份再改：不污染调用方传进来的 client（共享同一个 Transport 没问题）。
	copied := *client
	copied.CheckRedirect = sameHostRedirect
	return &HTTP{
		url: raw, fallbacks: fallbacks, key: strings.TrimSpace(opts.Key),
		protocol: protocol, allowed: allowed, timeout: timeout, logger: opts.Logger, client: &copied,
	}, nil
}

// validateEndpoint 校验搜索端点：纯 HTTP(S) 地址，不带用户信息、查询或片段。
func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("搜索服务地址必须是HTTP或HTTPS地址且不能包含用户信息或查询参数")
	}
	return nil
}

// containsString 判断切片是否已包含目标值（备用实例去重）。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// sameHostRedirect 只允许跳到同一个 host:port，其他一律拒绝。
//
// 为什么不能用默认策略：net/http 只在跨域时剥离 Authorization/Cookie，
// 自定义鉴权头（X-API-KEY / Ocp-Apim-Subscription-Key）原样保留。
// 搜索服务一旦返回 302 到别的域名，密钥就跟着走了。
//
// 边界：这里的 URL 来自配置而不是用户输入，所以不构成「用户可控 SSRF」；
// 风险是「服务端被劫持/配置错误时把密钥放出去」，因此宁可让这次搜索失败。
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= maxRedirects {
		return fmt.Errorf("重定向次数超过%d次", maxRedirects)
	}
	origin := via[0].URL
	if !strings.EqualFold(req.URL.Host, origin.Host) {
		return fmt.Errorf("%w：%s → %s", errCrossHostRedirect, origin.Host, req.URL.Host)
	}
	// 同 host 也不许从 HTTPS 降到 HTTP：密钥虽然不出域，但会明文传输。
	if origin.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("%w：%s → %s", errInsecureRedirect, origin.Scheme, req.URL.Scheme)
	}
	return nil
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
		return searchStatusUnconfigured, nil
	}
	// searxng 与配置了备用实例的协议走多实例轮换；其余保持旧的单实例重试语义。
	if h.protocol == "searxng" || len(h.fallbacks) > 0 {
		return h.searchInstances(ctx, query)
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
			return "", fmt.Errorf("%s：%w", searchStatusFailed, err)
		}
	}
	return formatResults(filterResults(parsed.Results, h.allowed)), nil
}

// searchInstances 按主实例 → 备用实例顺序轮换，总尝试不超过 6 次。
//
// 任意实例成功即返回；失败只换下一个实例，不在同实例上重试，避免放大风控。
func (h *HTTP) searchInstances(ctx context.Context, query string) (string, error) {
	endpoints := append([]string{h.url}, h.fallbacks...)
	var lastErr error
	for i, endpoint := range endpoints {
		if i >= maxInstanceAttempts {
			break
		}
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("%s：%w", searchStatusFailed, err)
		}
		started := time.Now()
		var parsed searchResponse
		var status string
		var err error
		if h.protocol == "searxng" {
			parsed, status, err = h.attemptSearxng(ctx, endpoint, query)
		} else {
			parsed, _, err = h.attemptAt(ctx, endpoint, query)
			status = "ok"
			if err != nil {
				status = "error"
			}
		}
		elapsed := time.Since(started)
		h.logAttempt(endpoint, status, len(parsed.Results), elapsed, err)
		if err != nil {
			lastErr = err
			continue
		}
		// 白名单作用于合并去重后的最终 URL。
		return formatResults(filterResults(dedupeResults(parsed.Results), h.allowed)), nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用实例")
	}
	return "", fmt.Errorf("%s：%w", searchStatusUnavailable, lastErr)
}

// attempt 是旧单实例路径：发一次请求；retryable 表示该错误是否值得再试。
func (h *HTTP) attempt(ctx context.Context, query string) (searchResponse, bool, error) {
	return h.attemptAt(ctx, h.url, query)
}

// attemptAt 向指定实例发一次非 searxng 请求。
func (h *HTTP) attemptAt(ctx context.Context, endpoint, query string) (searchResponse, bool, error) {
	req, err := h.newRequest(ctx, endpoint, query)
	if err != nil {
		return searchResponse{}, false, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		// 跨域重定向是确定性失败，重试只会再被拒一次。
		if errors.Is(err, errCrossHostRedirect) || errors.Is(err, errInsecureRedirect) {
			return searchResponse{}, false, &safeError{"搜索服务试图不安全重定向，已拒绝（避免泄漏密钥）", err}
		}
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

// attemptSearxng 先试 JSON，403/非 JSON 退化 HTML；单实例总时长受 h.timeout 限制。
//
// 返回的 status 仅用于日志（ok_json / ok_html / rate_limited / timeout / ...）。
func (h *HTTP) attemptSearxng(ctx context.Context, endpoint, query string) (searchResponse, string, error) {
	instanceCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	body, status, err := h.fetchSearxng(instanceCtx, endpoint, query, true)
	if err != nil {
		return searchResponse{}, failureStatus(err), err
	}
	if status == http.StatusTooManyRequests {
		return searchResponse{}, "rate_limited", fmt.Errorf("搜索实例返回状态码%d", status)
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		parsed, parseErr := parseSearxngJSON(body)
		if parseErr == nil && parsed.Results != nil {
			return parsed, "ok_json", nil
		}
		// 返回的是验证页/HTML/错误 JSON：退化为 HTML 解析。
	} else if status != http.StatusForbidden {
		return searchResponse{}, "http_error", fmt.Errorf("搜索实例返回状态码%d", status)
	}

	htmlBody, htmlStatus, err := h.fetchSearxng(instanceCtx, endpoint, query, false)
	if err != nil {
		return searchResponse{}, failureStatus(err), err
	}
	if htmlStatus == http.StatusTooManyRequests {
		return searchResponse{}, "rate_limited", fmt.Errorf("搜索实例返回状态码%d", htmlStatus)
	}
	if htmlStatus < http.StatusOK || htmlStatus >= http.StatusMultipleChoices {
		return searchResponse{}, "http_error", fmt.Errorf("搜索实例返回状态码%d", htmlStatus)
	}
	parsed := parseSearxngHTML(htmlBody)
	// 结果为空且页面含验证码/机器人校验文案：视为该实例本轮不可用。
	if len(parsed.Results) == 0 && looksBlockedHTML(htmlBody) {
		return searchResponse{}, "blocked", errors.New("搜索实例要求人机验证")
	}
	return parsed, "ok_html", nil
}

// fetchSearxng 发一次 GET；formatJSON=false 时不带 format 参数，拿 HTML。
func (h *HTTP) fetchSearxng(ctx context.Context, endpoint, query string, formatJSON bool) ([]byte, int, error) {
	req, err := h.searxngRequest(ctx, endpoint, query, formatJSON)
	if err != nil {
		return nil, 0, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		if errors.Is(err, errCrossHostRedirect) || errors.Is(err, errInsecureRedirect) {
			return nil, 0, &safeError{"搜索服务试图不安全重定向，已拒绝（避免泄漏密钥）", err}
		}
		return nil, 0, &safeError{"搜索请求失败", err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, &safeError{"读取搜索响应失败", err}
	}
	if len(data) > maxResponseBytes {
		return nil, resp.StatusCode, errors.New("搜索响应超过1MiB上限")
	}
	return data, resp.StatusCode, nil
}

// searxngRequest 构造 SearXNG GET：q + language，JSON 尝试额外带 format=json。
func (h *HTTP) searxngRequest(ctx context.Context, endpoint, query string, formatJSON bool) (*http.Request, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, &safeError{"构造搜索请求失败", err}
	}
	values := u.Query()
	values.Set("q", query)
	values.Set("language", searxngLanguage)
	if formatJSON {
		values.Set("format", "json")
	} else {
		values.Del("format")
	}
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &safeError{"构造搜索请求失败", err}
	}
	if formatJSON {
		req.Header.Set("Accept", "application/json, text/html;q=0.9")
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
	}
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.5")
	req.Header.Set("User-Agent", searxngUserAgent)
	return req, nil
}

// failureStatus 把实例失败归类成日志状态。
func failureStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "network_error"
}

// logAttempt 记录一次实例尝试；只打实例主机，不打查询词与响应正文。
func (h *HTTP) logAttempt(endpoint, status string, results int, elapsed time.Duration, err error) {
	if h.logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("action", "web_search"),
		zap.String("instance", endpointLabel(endpoint)),
		zap.String("status", status),
		zap.Duration("elapsed", elapsed),
	}
	if status == "ok_json" || status == "ok_html" || status == "ok" {
		fields = append(fields, zap.Int("results", results))
	}
	if err != nil {
		fields = append(fields, zap.String("error", err.Error()))
	}
	h.logger.Info("联网搜索实例尝试", fields...)
}

// endpointLabel 返回实例主机名，避免把路径/查询带进日志。
func endpointLabel(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "invalid"
	}
	return u.Host
}

func (h *HTTP) newRequest(ctx context.Context, endpoint, query string) (*http.Request, error) {
	buildError := func(err error) error { return &safeError{"构造搜索请求失败", err} }
	switch h.protocol {
	case "serper":
		payload, err := json.Marshal(serperRequest{Q: query})
		if err != nil {
			return nil, buildError(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
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
		target, err := url.Parse(endpoint)
		if err != nil {
			return nil, buildError(err)
		}
		values := target.Query()
		values.Set("q", query)
		values.Set("count", fmt.Sprint(bingResultCount))
		target.RawQuery = values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
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
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
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

// parseSearxngJSON 解析 SearXNG 的 results[].title/url/content。
//
// results 字段缺失时返回 nil 切片，调用方据此退化 HTML（JSON 错误页也走这条路）。
func parseSearxngJSON(data []byte) (searchResponse, error) {
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return searchResponse{}, &safeError{"解析搜索响应失败", err}
	}
	if payload.Results == nil {
		return searchResponse{}, nil
	}
	out := searchResponse{Results: make([]searchResult, 0, len(payload.Results))}
	for _, item := range payload.Results {
		out.Results = append(out.Results, searchResult{Title: item.Title, Snippet: item.Content, URL: item.URL})
	}
	return out, nil
}

var (
	// articleResultRE 匹配 SearXNG 结果卡片；只认 class 里带 result 的 article。
	articleResultRE = regexp.MustCompile(`(?is)<article\b[^>]*\bclass\s*=\s*"[^"]*\bresult\b[^"]*"[^>]*>(.*?)</article>`)
	headingRE       = regexp.MustCompile(`(?is)<h3\b[^>]*>(.*?)</h3>`)
	anchorRE        = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	hrefAttrRE      = regexp.MustCompile(`(?is)\bhref\s*=\s*"([^"]*)"`)
	contentBlockRE  = regexp.MustCompile(`(?is)<p\b[^>]*\bclass\s*=\s*"[^"]*\bcontent\b[^"]*"[^>]*>(.*?)</p>`)
	htmlTagRE       = regexp.MustCompile(`(?is)<[^>]*>`)
)

// parseSearxngHTML 从公共实例的 HTML 结果页提取标题/链接/摘要。
//
// 公共 SearXNG 默认不开放 format=json，只能走这条兜底：
// <article class="result"> 内含 <h3><a href="...">标题</a></h3> 与 <p class="content">摘要</p>。
func parseSearxngHTML(data []byte) searchResponse {
	var out searchResponse
	for _, match := range articleResultRE.FindAllSubmatch(data, -1) {
		if len(match) < 2 {
			continue
		}
		block := string(match[1])
		titleBlock := block
		if heading := headingRE.FindStringSubmatch(block); heading != nil {
			titleBlock = heading[1]
		}
		href, title := firstAnchor(titleBlock)
		if href == "" {
			href, title = firstAnchor(block)
		}
		if href == "" && title == "" {
			continue
		}
		snippet := ""
		if content := contentBlockRE.FindStringSubmatch(block); content != nil {
			snippet = cleanHTMLText(content[1])
		}
		out.Results = append(out.Results, searchResult{Title: title, Snippet: snippet, URL: href})
	}
	return out
}

// firstAnchor 返回第一个有 href 且可见文本非空的链接；全为空时保留首个 href。
func firstAnchor(fragment string) (string, string) {
	var fallbackHref string
	for _, match := range anchorRE.FindAllStringSubmatch(fragment, -1) {
		if len(match) < 3 {
			continue
		}
		hrefMatch := hrefAttrRE.FindStringSubmatch(match[1])
		if hrefMatch == nil {
			continue
		}
		href := html.UnescapeString(strings.TrimSpace(hrefMatch[1]))
		if href == "" {
			continue
		}
		if fallbackHref == "" {
			fallbackHref = href
		}
		if text := cleanHTMLText(match[2]); text != "" {
			return href, text
		}
	}
	return fallbackHref, ""
}

// cleanHTMLText 去标签、解实体并压缩空白。
func cleanHTMLText(fragment string) string {
	text := htmlTagRE.ReplaceAllString(fragment, " ")
	return strings.Join(strings.Fields(html.UnescapeString(text)), " ")
}

// looksBlockedHTML 判断空结果页是否其实是验证码/机器人拦截页。
//
// 只在一个 article 都没解析出来时调用；标记用具体反爬文案，避免把
// 正常「无结果」页（可能包含查询词 robot/verify）误判成拦截。
func looksBlockedHTML(data []byte) bool {
	lower := strings.ToLower(string(data))
	for _, marker := range []string{
		"captcha",
		"anubis",
		"verifyngo",
		"making sure you're not a bot",
		"verifying your browser",
		"security check",
		"challenge-platform",
		"cf-chl",
		"人机验证",
		"自动化程序",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// dedupeResults 按最终 URL 去重；没有 URL 的结果按标题+摘要去重。
func dedupeResults(results []searchResult) []searchResult {
	seen := make(map[string]bool, len(results))
	out := make([]searchResult, 0, len(results))
	for _, result := range results {
		key := strings.TrimSpace(result.URL)
		if key != "" {
			if u, err := url.Parse(key); err == nil {
				u.Fragment = ""
				key = u.String()
			}
		} else {
			key = "title:" + strings.TrimSpace(result.Title) + "\x1f" + strings.TrimSpace(result.Snippet)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, result)
	}
	return out
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
		return searchStatusNoResult
	}
	return clipRunes(searchStatusDone+"\n"+b.String(), maxResultRunes)
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
