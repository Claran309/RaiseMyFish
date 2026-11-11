package bili

// 本文件测动态凭据解析（account.Resolver）、Nav 身份确认与失效判定。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewAllowsResolverWithoutStaticCookie 有 Resolver 时无需静态 cookie 即可构造。
func TestNewAllowsResolverWithoutStaticCookie(t *testing.T) {
	c, err := New(Options{Resolver: func(context.Context) (string, string, error) {
		return testCookie, "42", nil
	}})
	if err != nil {
		t.Fatalf("动态解析应允许无静态 cookie：%v", err)
	}
	if !c.HasResolver() {
		t.Error("HasResolver 应为 true")
	}
	if !c.HasCSRF() {
		t.Error("动态解析时 HasCSRF 应为 true（请求期再检查）")
	}
	// Resolver 模式忽略静态 cookie，换行也由解析结果校验
	if _, err := New(Options{Cookie: "SESSDATA=only\r\nx: y", Resolver: func(context.Context) (string, string, error) {
		return testCookie, "42", nil
	}}); err != nil {
		t.Errorf("Resolver 模式不应校验静态 cookie：%v", err)
	}
}

// TestResolverCredentialPerRequest 每次请求都取最新凭据，账号切换即时生效。
func TestResolverCredentialPerRequest(t *testing.T) {
	var mu sync.Mutex
	var cookies []string
	var senders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookies = append(cookies, r.Header.Get("Cookie"))
		mu.Unlock()
		if r.URL.Path == "/x/web-interface/nav" {
			// 私信发送需要 WBI 签名，nav 用当前凭据应答。
			_, _ = w.Write([]byte(`{"code":0,"data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`))
			return
		}
		_ = r.ParseForm()
		if r.URL.Path == "/web_im/v1/web_im/send_msg" {
			mu.Lock()
			senders = append(senders, r.PostForm.Get("msg[sender_uid]"))
			mu.Unlock()
		}
		writeEnvelope(w, 0, map[string]any{"live_status": 0, "title": "t"})
	}))
	t.Cleanup(srv.Close)

	var mu2 sync.Mutex
	cookie, mid := "SESSDATA=first; bili_jct=csrf-1", "1001"
	c, err := New(Options{
		APIRoot:  srv.URL,
		LiveRoot: srv.URL,
		VCRoot:   srv.URL,
		Resolver: func(context.Context) (string, string, error) {
			mu2.Lock()
			defer mu2.Unlock()
			return cookie, mid, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.LiveStatus(context.Background(), "1"); err != nil {
		t.Fatalf("首次请求失败：%v", err)
	}
	mu2.Lock()
	cookie, mid = "SESSDATA=second; bili_jct=csrf-2", "2002"
	mu2.Unlock()
	if err := c.SendDM(context.Background(), "9", "hi"); err != nil {
		t.Fatalf("切换后请求失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(cookies) < 2 || !strings.Contains(cookies[0], "first") {
		t.Fatalf("首次请求未用旧凭据：%v", cookies)
	}
	// 切号后 SendDM 先取 WBI 密钥再发送，两步都必须用新凭据。
	for _, cookie := range cookies[1:] {
		if !strings.Contains(cookie, "second") {
			t.Fatalf("切号后仍用旧凭据：%v", cookies)
		}
	}
	if len(senders) != 1 || senders[0] != "2002" {
		t.Fatalf("sender_uid 未用最新 UID：%v", senders)
	}
}

// TestResolverNoCredentialAndBadFormat 无凭据/格式非法时本地拒绝，不发请求。
func TestResolverNoCredentialAndBadFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("凭据不可用时不应发请求")
	}))
	t.Cleanup(srv.Close)

	base := Options{APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL}
	noCred := base
	noCred.Resolver = func(context.Context) (string, string, error) { return "", "", ErrNoCredential }
	c, err := New(noCred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.LiveStatus(context.Background(), "1"); !errors.Is(err, ErrNoCredential) {
		t.Errorf("应返回 ErrNoCredential，实际 %v", err)
	}

	bad := base
	bad.Resolver = func(context.Context) (string, string, error) {
		return "SESSDATA=x\r\ninjected: 1", "", nil
	}
	c2, err := New(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.LiveStatus(context.Background(), "1"); err == nil || !strings.Contains(err.Error(), "格式非法") {
		t.Errorf("换行注入应被拒绝：%v", err)
	}
}

// TestResolverErrorIsSafe Resolver 失败时不发请求，错误不携带静态凭据。
func TestResolverErrorIsSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("解析失败时不应发请求")
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{
		APIRoot:  srv.URL,
		LiveRoot: srv.URL,
		VCRoot:   srv.URL,
		Resolver: func(context.Context) (string, string, error) { return "", "", errors.New("数据库暂不可用") },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Nav(context.Background())
	if err == nil || !strings.Contains(err.Error(), "数据库暂不可用") {
		t.Fatalf("应透出安全的解析失败原因：%v", err)
	}
	if strings.Contains(err.Error(), "SESSDATA") || strings.Contains(err.Error(), "bili_jct") {
		t.Errorf("错误泄漏凭据：%v", err)
	}
}

// TestNavIdentity get_info 成功时解析 mid（数字/字符串）与昵称。
func TestNavIdentity(t *testing.T) {
	for _, mid := range []string{`42`, `"42"`} {
		var gotPath, gotCookie string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotCookie = r.URL.Path, r.Header.Get("Cookie")
			_, _ = w.Write([]byte(`{"code":0,"data":{"isLogin":true,"mid":` + mid + `,"uname":"肥鱼"}}`))
		}))
		c, err := New(Options{APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL, Cookie: testCookie, Timeout: time.Second})
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		info, err := c.Nav(context.Background())
		srv.Close()
		if err != nil {
			t.Fatalf("Nav 失败（mid=%s）：%v", mid, err)
		}
		if gotPath != "/x/web-interface/nav" {
			t.Errorf("路径不符：%s", gotPath)
		}
		if gotCookie != testCookie {
			t.Errorf("cookie 不符：%q", gotCookie)
		}
		if info.MID != "42" || info.Name != "肥鱼" {
			t.Errorf("身份不符：%+v", info)
		}
	}
}

// TestNavInvalidCredential 平台未登录/HTTP 401 都归为 ErrNotLoggedIn。
func TestNavInvalidCredential(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"code-101", http.StatusOK, `{"code":-101,"message":"未登录"}`},
		{"not-login", http.StatusOK, `{"code":0,"data":{"isLogin":false,"mid":0,"uname":""}}`},
		{"http401", http.StatusUnauthorized, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != http.StatusOK {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, err := New(Options{APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL, Cookie: testCookie})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Nav(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
				t.Fatalf("应返回 ErrNotLoggedIn，实际 %v", err)
			}
		})
	}
}

// TestIsAuthError 只把未登录/无权限类错误判为凭据失效。
func TestIsAuthError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{ErrNotLoggedIn, true},
		{&Error{Op: "x", Status: http.StatusUnauthorized}, true},
		{&Error{Op: "x", Status: http.StatusForbidden}, true},
		{&Error{Op: "x", Code: -101}, true},
		{&Error{Op: "x", Code: -111}, true},
		{&Error{Op: "x", Code: 10031}, false},
		{&Error{Op: "x", Status: http.StatusTooManyRequests}, false},
		{errors.New("网络请求失败"), false},
	}
	for _, tc := range cases {
		if got := IsAuthError(tc.err); got != tc.want {
			t.Errorf("IsAuthError(%v) = %v，期望 %v", tc.err, got, tc.want)
		}
	}
}
