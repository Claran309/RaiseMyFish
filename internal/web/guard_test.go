package web

// 本文件测 O04：面板写接口的跨站防护（T21）。
//
// 三条验收：
//   - 跨站 + text/plain 被拒；
//   - 合法同源页面可正常操作；
//   - 没有 Origin 的本地 CLI 出示令牌后通过。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// guardReq 构造一个「没有 Origin」的裸请求，由各用例自行补头。
func guardReq(method, target, body string) *http.Request {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, target, reader)
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Host = "127.0.0.1:8090"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

const guardPayload = `{"cookie":"SESSDATA=abc","note":"测试号"}`

// TestWriteGuardRejectsCrossSiteOrigin 跨站 Origin 一律 403，且不落库。
func TestWriteGuardRejectsCrossSiteOrigin(t *testing.T) {
	accounts := &fakeAccounts{}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, accounts)

	cases := []struct {
		name   string
		origin string
		ctype  string
	}{
		{"跨站+JSON", "http://evil.example.com", "application/json"},
		{"跨站+text/plain", "http://evil.example.com", "text/plain"},
		{"同主机不同端口", "http://127.0.0.1:9999", "application/json"},
		{"同主机不同协议主机名", "http://localhost.localdomain", "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Content-Type", tc.ctype)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("跨站写应 403，实际 %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	if accounts.addCalls != 0 {
		t.Fatalf("跨站请求不应落库：%d", accounts.addCalls)
	}
}

// TestWriteGuardRejectsPlainTextBody 同源但 text/plain 也要拒（浏览器简单请求的入口）。
func TestWriteGuardRejectsPlainTextBody(t *testing.T) {
	accounts := &fakeAccounts{}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, accounts)

	req := guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
	req.Header.Set("Origin", "http://127.0.0.1:8090")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain 应 415，实际 %d", rec.Code)
	}
	if accounts.addCalls != 0 {
		t.Fatalf("text/plain 不应落库：%d", accounts.addCalls)
	}
}

// TestWriteGuardAllowsSameOriginPage 同源页面照常操作。
func TestWriteGuardAllowsSameOriginPage(t *testing.T) {
	accounts := &fakeAccounts{}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, accounts)

	req := guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
	req.Header.Set("Origin", "http://127.0.0.1:8090")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("同源页面应可写（201），实际 %d %s", rec.Code, rec.Body.String())
	}
	if accounts.addCalls != 1 {
		t.Fatalf("应落库 1 次，实际 %d", accounts.addCalls)
	}
}

// TestWriteGuardAllowsTokenWithoutOrigin 本地 CLI：没有 Origin，用令牌通过。
func TestWriteGuardAllowsTokenWithoutOrigin(t *testing.T) {
	accounts := &fakeAccounts{}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, accounts)
	if srv.Token() == "" {
		t.Fatal("面板应自动生成令牌")
	}

	req := guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
	req.Header.Set(panelTokenHeader, srv.Token())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("带令牌的 CLI 请求应通过（201），实际 %d %s", rec.Code, rec.Body.String())
	}

	// Bearer 形式同样接受。
	req = guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
	req.Header.Set("Authorization", "Bearer "+srv.Token())
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("Bearer 令牌应通过（201），实际 %d", rec.Code)
	}
}

// TestWriteGuardRequiresTokenForBareCLI 缺 Origin 又缺令牌：401（不是 403，缺头不等于攻击）。
func TestWriteGuardRequiresTokenForBareCLI(t *testing.T) {
	accounts := &fakeAccounts{}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, accounts)

	req := guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺令牌应 401，实际 %d", rec.Code)
	}

	// 错令牌同样拒绝。
	req = guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
	req.Header.Set(panelTokenHeader, "wrong-token")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错令牌应 401，实际 %d", rec.Code)
	}
}

// TestWriteGuardHostMustMatchPanel Host 不是面板自身时拒（DNS rebinding）。
func TestWriteGuardHostMustMatchPanel(t *testing.T) {
	accounts := &fakeAccounts{}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, accounts)

	req := guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
	req.Host = "evil.example.com:8090"
	req.Header.Set("Origin", "http://evil.example.com:8090")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("异常 Host 应 403，实际 %d", rec.Code)
	}

	// 端口不符也要拒。
	req = guardReq(http.MethodPost, "/api/accounts?confirm=yes", guardPayload)
	req.Host = "127.0.0.1:9999"
	req.Header.Set(panelTokenHeader, srv.Token())
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("端口不符应 403，实际 %d", rec.Code)
	}
}

// TestWriteGuardLeavesReadsAlone 读接口不要求 Origin/令牌（跨站读不到响应体）。
func TestWriteGuardLeavesReadsAlone(t *testing.T) {
	srv := newTestServerWithAccounts(t, &fakeRepo{memory: nil}, &fakeAccounts{})

	req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
	req.Host = "example.com" // 极端 Host 也不拦读
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("读接口不应被拦，实际 %d", rec.Code)
	}
}

// TestPageInjectsToken 页面里要带令牌，页面 JS 才能做写操作。
func TestPageInjectsToken(t *testing.T) {
	srv := newTestServerWithAccounts(t, &fakeRepo{}, &fakeAccounts{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("首页应可访问，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, pageTokenPlaceholder) {
		t.Fatal("页面仍残留令牌占位符")
	}
	if !strings.Contains(body, srv.Token()) {
		t.Fatal("页面未注入真实令牌")
	}

	// 页面里的写请求必须真的带上令牌头，否则同源页面也会被 401。
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts", nil))
	if !strings.Contains(rec.Body.String(), "X-Panel-Token") {
		t.Fatal("账号页的写请求未携带令牌头")
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/memory", nil))
	if !strings.Contains(rec.Body.String(), "X-Panel-Token") {
		t.Fatal("记忆页的删除请求未携带令牌头")
	}
}

// TestTokenEndpoint /api/token 供 CLI 取令牌，且不返回 CORS 头。
func TestTokenEndpoint(t *testing.T) {
	srv := newTestServerWithAccounts(t, &fakeRepo{}, &fakeAccounts{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/token", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), srv.Token()) {
		t.Fatalf("令牌端点不符：%d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("不应返回 CORS 头（否则跨站能读到令牌）：%q", got)
	}
}

// TestNewGeneratesDistinctTokens 每次启动的令牌不同，避免固定值被复用。
func TestNewGeneratesDistinctTokens(t *testing.T) {
	first, err := New(Options{Addr: "127.0.0.1:8090", Repo: &fakeRepo{}, Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Options{Addr: "127.0.0.1:8090", Repo: &fakeRepo{}, Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	if first.Token() == "" || first.Token() == second.Token() {
		t.Fatalf("令牌应为随机且互不相同：%q / %q", first.Token(), second.Token())
	}
}
