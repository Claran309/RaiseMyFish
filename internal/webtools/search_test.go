package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSearchPostsQueryAndBearer 断言通用协议：POST JSON + 可选 Bearer，结果按行拼接。
func TestSearchPostsQueryAndBearer(t *testing.T) {
	var gotMethod, gotContentType, gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		var req searchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("请求体不是约定JSON：%v", err)
		}
		gotQuery = req.Query
		io.WriteString(w, `{"results":[
			{"title":"标题A","snippet":"摘要A","url":"https://example.com/a"},
			{"title":"标题B","snippet":"摘要B","url":"https://example.com/b"}
		]}`)
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL, Key: "search-secret"})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := searcher.Search(context.Background(), "  大肥鱼  ")
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q，期望 POST", gotMethod)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotAuth != "Bearer search-secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotQuery != "大肥鱼" {
		t.Errorf("query = %q，期望已去除首尾空白", gotQuery)
	}
	for _, want := range []string{"1. 标题A：摘要A（https://example.com/a）", "2. 标题B：摘要B（https://example.com/b）"} {
		if !strings.Contains(got, want) {
			t.Errorf("结果缺少 %q：%q", want, got)
		}
	}
}

// TestSearchWithoutKeyOmitsAuthorization 未配置 key 时不带 Authorization 头。
func TestSearchWithoutKeyOmitsAuthorization(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"results":[{"title":"t","snippet":"s"}]}`)
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := searcher.Search(context.Background(), "查询"); err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if gotAuth != "" {
		t.Errorf("未配置 key 却带了 Authorization：%q", gotAuth)
	}
}

// TestSearchUnconfiguredReturnsNotice 空 URL 不报错，返回「未配置」说明。
func TestSearchUnconfiguredReturnsNotice(t *testing.T) {
	searcher, err := NewHTTP(Options{})
	if err != nil {
		t.Fatalf("空 URL 不应装配失败：%v", err)
	}
	got, err := searcher.Search(context.Background(), "今天几号")
	if err != nil {
		t.Fatalf("未配置不应报错：%v", err)
	}
	if !strings.Contains(got, "未配置") {
		t.Errorf("应说明未配置：%q", got)
	}

	var nilSearcher *HTTP
	got, err = nilSearcher.Search(context.Background(), "今天几号")
	if err != nil || !strings.Contains(got, "未配置") {
		t.Errorf("nil 接收者应返回未配置说明：got=%q err=%v", got, err)
	}
}

// TestSearchTruncatesTo500Runes 超长结果按 rune 截断到 500。
func TestSearchTruncatesTo500Runes(t *testing.T) {
	long := strings.Repeat("鱼", 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(searchResponse{Results: []searchResult{{Title: "标题", Snippet: long, URL: "https://example.com"}}})
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := searcher.Search(context.Background(), "查询")
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if n := utf8.RuneCountInString(got); n != maxResultRunes {
		t.Errorf("截断后长度 = %d，期望 %d", n, maxResultRunes)
	}
}

// TestSearchRejectsOversizeResponse 响应超过 1MiB 直接报错，不继续解析。
func TestSearchRejectsOversizeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	_, err = searcher.Search(context.Background(), "查询")
	if err == nil || !strings.Contains(err.Error(), "1MiB") {
		t.Fatalf("超限响应应报错，实际：%v", err)
	}
}

// TestSearchStatusErrorDoesNotLeakBody 非 2xx 只报状态码，不回显响应正文。
func TestSearchStatusErrorDoesNotLeakBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "private-search-body")
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	_, err = searcher.Search(context.Background(), "查询")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("应报状态码错误，实际：%v", err)
	}
	if strings.Contains(err.Error(), "private-search-body") {
		t.Fatalf("错误泄漏响应正文：%v", err)
	}
}

// TestSearchEmptyAndInvalidResponse 空结果与非法 JSON 都有明确出口。
func TestSearchEmptyAndInvalidResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"空结果", `{"results":[]}`, "没有搜到相关结果"},
		{"字段全空", `{"results":[{"title":"","snippet":"","url":""}]}`, "没有搜到相关结果"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			searcher, err := NewHTTP(Options{URL: srv.URL})
			if err != nil {
				t.Fatalf("装配失败：%v", err)
			}
			got, err := searcher.Search(context.Background(), "查询")
			if err != nil || !strings.Contains(got, tc.want) {
				t.Fatalf("got=%q err=%v，期望包含 %q", got, err, tc.want)
			}
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not-json")
	}))
	defer srv.Close()
	searcher, _ := NewHTTP(Options{URL: srv.URL})
	if _, err := searcher.Search(context.Background(), "查询"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

// TestSearchRejectsEmptyQuery 空查询词在客户端层也被拒绝。
func TestSearchRejectsEmptyQuery(t *testing.T) {
	searcher, err := NewHTTP(Options{URL: "https://search.example.com/api"})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := searcher.Search(context.Background(), "   "); err == nil {
		t.Fatal("空查询应报错")
	}
}

// TestSearchHonorsCanceledContext 上下文取消立即失败，不发请求。
func TestSearchHonorsCanceledContext(t *testing.T) {
	searcher, err := NewHTTP(Options{URL: "https://search.example.com/api"})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := searcher.Search(ctx, "查询"); err == nil {
		t.Fatal("已取消的上下文应报错")
	}
}

// TestSearchRetriesTransientFailureOnce 5xx 失败重试 1 次，第二次成功即返回。
func TestSearchRetriesTransientFailureOnce(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		io.WriteString(w, `{"results":[{"title":"恢复","snippet":"第二次成功","url":"https://example.com"}]}`)
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := searcher.Search(context.Background(), "查询")
	if err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if calls != 2 {
		t.Errorf("请求次数 = %d，期望 2", calls)
	}
	if !strings.Contains(got, "第二次成功") {
		t.Errorf("结果不符：%q", got)
	}
}

// TestSearchDoesNotRetryClientError 4xx 不重试，避免放大风控。
func TestSearchDoesNotRetryClientError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	searcher, _ := NewHTTP(Options{URL: srv.URL})
	if _, err := searcher.Search(context.Background(), "查询"); err == nil {
		t.Fatal("4xx 应报错")
	}
	if calls != 1 {
		t.Errorf("4xx 不应重试，请求次数 = %d", calls)
	}
}

type failOnceTransport struct {
	calls int
	base  http.RoundTripper
}

func (f *failOnceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	if f.calls == 1 {
		return nil, errors.New("connection reset")
	}
	return f.base.RoundTrip(r)
}

// TestSearchRetriesNetworkError 连接级失败也重试 1 次。
func TestSearchRetriesNetworkError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"results":[{"title":"网络恢复","snippet":"ok","url":"https://example.com"}]}`)
	}))
	defer srv.Close()
	transport := &failOnceTransport{base: http.DefaultTransport}
	searcher, err := NewHTTP(Options{URL: srv.URL, Client: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := searcher.Search(context.Background(), "查询")
	if err != nil {
		t.Fatalf("网络失败重试后应成功：%v", err)
	}
	if transport.calls != 2 || calls != 1 {
		t.Errorf("transport=%d server=%d，期望各 2/1", transport.calls, calls)
	}
	if !strings.Contains(got, "网络恢复") {
		t.Errorf("结果不符：%q", got)
	}
}

// TestSearchFiltersByAllowedDomains 白名单只保留精确域名与子域。
func TestSearchFiltersByAllowedDomains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"results":[
			{"title":"主域","snippet":"a","url":"https://example.com/a"},
			{"title":"子域","snippet":"b","url":"https://www.example.com/b"},
			{"title":"伪后缀","snippet":"c","url":"https://evil-example.com/c"},
			{"title":"其它","snippet":"d","url":"https://other.org/d"},
			{"title":"无链接","snippet":"e","url":""}
		]}`)
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL, AllowedDomains: []string{"example.com"}})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := searcher.Search(context.Background(), "查询")
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	for _, want := range []string{"主域", "子域"} {
		if !strings.Contains(got, want) {
			t.Errorf("白名单内结果缺失 %q：%q", want, got)
		}
	}
	for _, banned := range []string{"伪后缀", "其它", "无链接"} {
		if strings.Contains(got, banned) {
			t.Errorf("白名单外结果未过滤 %q：%q", banned, got)
		}
	}
}

// TestSearchSerperProtocol Serper：POST {"q"} + X-API-KEY，解析 organic。
func TestSearchSerperProtocol(t *testing.T) {
	var method, apiKey, auth, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		apiKey = r.Header.Get("X-API-KEY")
		auth = r.Header.Get("Authorization")
		var req serperRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		query = req.Q
		io.WriteString(w, `{"organic":[{"title":"Serper标题","snippet":"摘要","link":"https://example.com/s"}]}`)
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL, Protocol: "serper", Key: "serper-secret"})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := searcher.Search(context.Background(), "大肥鱼")
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if method != http.MethodPost || apiKey != "serper-secret" || auth != "" {
		t.Errorf("Serper 请求不符：method=%s key=%q auth=%q", method, apiKey, auth)
	}
	if query != "大肥鱼" {
		t.Errorf("Serper q = %q", query)
	}
	if !strings.Contains(got, "Serper标题") || !strings.Contains(got, "https://example.com/s") {
		t.Errorf("Serper 结果未解析：%q", got)
	}
}

// TestSearchBingProtocol Bing：GET ?q + 订阅密钥头，解析 webPages.value。
func TestSearchBingProtocol(t *testing.T) {
	var method, key, query, count, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		key = r.Header.Get("Ocp-Apim-Subscription-Key")
		auth = r.Header.Get("Authorization")
		query = r.URL.Query().Get("q")
		count = r.URL.Query().Get("count")
		io.WriteString(w, `{"webPages":{"value":[{"name":"Bing标题","snippet":"摘要","url":"https://example.com/b"}]}}`)
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL, Protocol: "bing", Key: "bing-secret"})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	got, err := searcher.Search(context.Background(), "养鱼")
	if err != nil {
		t.Fatalf("搜索失败：%v", err)
	}
	if method != http.MethodGet || key != "bing-secret" || auth != "" {
		t.Errorf("Bing 请求不符：method=%s key=%q auth=%q", method, key, auth)
	}
	if query != "养鱼" || count != "10" {
		t.Errorf("Bing 参数不符：q=%q count=%q", query, count)
	}
	if !strings.Contains(got, "Bing标题") || !strings.Contains(got, "https://example.com/b") {
		t.Errorf("Bing 结果未解析：%q", got)
	}
}

// TestNewHTTPValidatesProtocolAndDomains 装配期拒绝未知协议与非法白名单。
func TestNewHTTPValidatesProtocolAndDomains(t *testing.T) {
	if _, err := NewHTTP(Options{Protocol: "google"}); err == nil {
		t.Error("未知协议应被拒绝")
	}
	for _, domain := range []string{"", "https://example.com", "*.example.com", "example.com/path", "exa mple.com"} {
		if _, err := NewHTTP(Options{AllowedDomains: []string{domain}}); err == nil {
			t.Errorf("白名单 %q 应被拒绝", domain)
		}
	}
	if _, err := NewHTTP(Options{Protocol: "SERPER", AllowedDomains: []string{"Example.COM"}}); err != nil {
		t.Errorf("大小写应被接受：%v", err)
	}
}

// TestNewHTTPRejectsBadURL 端点必须是纯 HTTP(S) 地址，不带用户信息/查询/片段。
func TestNewHTTPRejectsBadURL(t *testing.T) {
	bad := []string{
		"ftp://search.example.com/api",
		"https://user:pass@search.example.com/api",
		"https://search.example.com/api?token=secret",
		"https://search.example.com/api#frag",
		"not a url",
		"//search.example.com/api",
	}
	for _, raw := range bad {
		if _, err := NewHTTP(Options{URL: raw}); err == nil {
			t.Errorf("URL %q 应被拒绝", raw)
		}
	}
	good := []string{"", "https://search.example.com/api", "http://127.0.0.1:8080/search"}
	for _, raw := range good {
		if _, err := NewHTTP(Options{URL: raw}); err != nil {
			t.Errorf("URL %q 应被接受：%v", raw, err)
		}
	}
}

// TestSearchRejectsCrossHostRedirect O12 核心：跨 host 重定向必须被拒，密钥不能跟着走。
//
// Go 默认只在跨域时剥离 Authorization/Cookie，自定义头（X-API-KEY 等）会原样保留；
// 所以搜索服务返回 302 到别的域名时，第二端原本能收到完整密钥。
func TestSearchRejectsCrossHostRedirect(t *testing.T) {
	const key = "secret-serper-key"

	// 第二端：只记录「有没有被访问、有没有收到 key」。
	var hits int
	var leakedKey string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		leakedKey = r.Header.Get("X-API-KEY")
		_, _ = io.WriteString(w, `{"organic":[]}`)
	}))
	defer target.Close()

	// 第一端：收到带 key 的请求后，试图把客户端重定向到第二端。
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer source.Close()

	searcher, err := NewHTTP(Options{
		URL: source.URL, Key: key, Protocol: "serper", Client: source.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := searcher.Search(context.Background(), "测试查询")
	if err == nil {
		t.Fatalf("跨域重定向应失败，实际返回：%q", out)
	}
	if !strings.Contains(err.Error(), "不安全重定向") {
		t.Fatalf("错误文案应说明跨域重定向：%v", err)
	}
	// 错误里绝不能回显密钥。
	if strings.Contains(err.Error(), key) {
		t.Fatalf("错误泄露密钥：%v", err)
	}
	if hits != 0 {
		t.Fatalf("第二端不应收到请求，实际 %d 次", hits)
	}
	if leakedKey != "" {
		t.Fatalf("密钥被带到别的 host：%q", leakedKey)
	}
}

// TestSearchAllowsSameHostRedirect 同 host 的重定向照常跟随（服务端常见 301/302）。
func TestSearchAllowsSameHostRedirect(t *testing.T) {
	var srv *httptest.Server
	var redirected int
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			redirected++
			http.Redirect(w, r, srv.URL+"/search", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"标题","snippet":"摘要","url":"https://example.com/a"}]}`)
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL + "/redirect", Key: "k", Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	out, err := searcher.Search(context.Background(), "查询")
	if err != nil {
		t.Fatalf("同 host 重定向应成功：%v", err)
	}
	if redirected != 1 || !strings.Contains(out, "标题") {
		t.Fatalf("同 host 重定向未被跟随：redirected=%d out=%q", redirected, out)
	}
}

// TestSearchRejectsRedirectLoop 同 host 也不能无限跳。
func TestSearchRejectsRedirectLoop(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
	}))
	defer srv.Close()

	searcher, err := NewHTTP(Options{URL: srv.URL + "/loop", Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := searcher.Search(context.Background(), "查询"); err == nil {
		t.Fatal("重定向环应报错")
	}
}

// TestSameHostRedirectPolicy 单元验证策略本身：host 含端口才算同源。
func TestSameHostRedirectPolicy(t *testing.T) {
	base := func(rawURL string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	origin := base("https://api.example.com/v1/search")
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{"同 host 同端口", "https://api.example.com/next", false},
		{"不同 host", "https://evil.example.net/next", true},
		{"不同端口", "https://api.example.com:8443/next", true},
		{"降级到 http 同 host", "http://api.example.com/next", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sameHostRedirect(base(tc.target), []*http.Request{origin})
			if tc.wantErr && err == nil {
				t.Fatalf("应拒绝：%s", tc.target)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("应允许：%s（%v）", tc.target, err)
			}
		})
	}
}
