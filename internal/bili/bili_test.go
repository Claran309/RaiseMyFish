package bili

// 本文件用 httptest 模拟平台：断言 URL/方法/参数/csrf，不触碰真实网络。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testCookie = "SESSDATA=secret-sess; bili_jct=csrf-token-123; DedeUserID=42"

// newTestClient 起一个模拟平台并把三个根地址都指向它。
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		APIRoot:  srv.URL,
		LiveRoot: srv.URL,
		VCRoot:   srv.URL,
		Cookie:   testCookie,
		SelfMID:  "3747574135655903",
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	return c
}

func writeEnvelope(w http.ResponseWriter, code int, data any) {
	payload := map[string]any{"code": code, "message": "0", "data": data}
	_ = json.NewEncoder(w).Encode(payload)
}

// TestNewRequiresCookie 无 cookie 返回 ErrNoCredential（调用方按未配置处理，不报错）。
func TestNewRequiresCookie(t *testing.T) {
	if _, err := New(Options{Cookie: "  "}); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("空 cookie 应返回 ErrNoCredential，实际 %v", err)
	}
	if _, err := New(Options{Cookie: "SESSDATA=x\r\ninjected: 1"}); err == nil {
		t.Fatal("换行注入应被拒绝")
	}
}

// TestParseCSRF 从 bili_jct 取 csrf。
func TestParseCSRF(t *testing.T) {
	cases := map[string]string{
		testCookie:                  "csrf-token-123",
		"SESSDATA=only":             "",
		"SESSDATA=bb; bili_jct=aa;": "aa",
		"bili_jct=":                 "",
	}
	for cookie, want := range cases {
		if got := parseCSRF(cookie); got != want {
			t.Errorf("parseCSRF(%q) = %q，期望 %q", cookie, got, want)
		}
	}
}

// TestLiveStatus League 直播间状态：断言 URL 与 room_id，解析状态/标题/人气。
func TestLiveStatus(t *testing.T) {
	var gotPath, gotRoom, gotCookie string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRoom = r.URL.Query().Get("room_id")
		gotCookie = r.Header.Get("Cookie")
		writeEnvelope(w, 0, map[string]any{"live_status": 1, "title": "今晚钓大鱼", "online": 2333})
	})

	got, err := c.LiveStatus(context.Background(), "30308220")
	if err != nil {
		t.Fatalf("LiveStatus 失败：%v", err)
	}
	if gotPath != "/room/v1/Room/get_info" || gotRoom != "30308220" {
		t.Errorf("请求不符：path=%s room=%s", gotPath, gotRoom)
	}
	if !strings.Contains(gotCookie, "SESSDATA=secret-sess") {
		t.Errorf("未带 cookie：%q", gotCookie)
	}
	if !strings.Contains(got, "直播中") || !strings.Contains(got, "今晚钓大鱼") || !strings.Contains(got, "2333") {
		t.Errorf("摘要不符：%q", got)
	}

	// 房间号非法直接拒绝，不发请求
	if _, err := c.LiveStatus(context.Background(), "abc"); err == nil {
		t.Error("非法房间号应拒绝")
	}
}

// TestRelationFollow 关注：POST + fid/act/re_src/csrf 齐全。
func TestRelationFollow(t *testing.T) {
	var method, path, fid, act, reSrc, csrf string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		method, path = r.Method, r.URL.Path
		fid, act, reSrc, csrf = r.PostForm.Get("fid"), r.PostForm.Get("act"), r.PostForm.Get("re_src"), r.PostForm.Get("csrf")
		writeEnvelope(w, 0, nil)
	})

	if err := c.Follow(context.Background(), "313591854"); err != nil {
		t.Fatalf("Follow 失败：%v", err)
	}
	if method != http.MethodPost || path != "/x/relation/modify" {
		t.Errorf("请求不符：%s %s", method, path)
	}
	if fid != "313591854" || act != "1" || reSrc != relationReSrc || csrf != "csrf-token-123" {
		t.Errorf("表单不符：fid=%s act=%s re_src=%s csrf=%s", fid, act, reSrc, csrf)
	}
}

// TestUnfollowUsesAct2 取关 act=2。
func TestUnfollowUsesAct2(t *testing.T) {
	var act string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		act = r.PostForm.Get("act")
		writeEnvelope(w, 0, nil)
	})
	if err := c.Unfollow(context.Background(), "123"); err != nil {
		t.Fatalf("Unfollow 失败：%v", err)
	}
	if act != "2" {
		t.Errorf("act = %q，期望 2", act)
	}
}

// TestLikeRequest 点赞：路径 + room_id/count/csrf。
func TestLikeRequest(t *testing.T) {
	var path, room, count, csrf string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		path = r.URL.Path
		room, count, csrf = r.PostForm.Get("room_id"), r.PostForm.Get("count"), r.PostForm.Get("csrf")
		writeEnvelope(w, 0, nil)
	})

	if err := c.Like(context.Background(), "30308220", 2); err != nil {
		t.Fatalf("Like 失败：%v", err)
	}
	if path != "/room/v1/Room/like" || room != "30308220" || count != "2" || csrf != "csrf-token-123" {
		t.Errorf("请求不符：path=%s room=%s count=%s csrf=%s", path, room, count, csrf)
	}
	if err := c.Like(context.Background(), "30308220", 9); err == nil {
		t.Error("count 越界应拒绝")
	}
}

// TestSendDMRequest 私信：sender/receiver/content 结构正确。
func TestSendDMRequest(t *testing.T) {
	var path, sender, receiver, msgType, content, csrf string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		path = r.URL.Path
		sender = r.PostForm.Get("msg[sender_uid]")
		receiver = r.PostForm.Get("msg[receiver_id]")
		msgType = r.PostForm.Get("msg[msg_type]")
		content = r.PostForm.Get("msg[content]")
		csrf = r.PostForm.Get("csrf")
		writeEnvelope(w, 0, nil)
	})

	if err := c.SendDM(context.Background(), "313591854", "在干嘛"); err != nil {
		t.Fatalf("SendDM 失败：%v", err)
	}
	if path != "/web_im/v1/web_im/send_msg" {
		t.Errorf("path = %s", path)
	}
	if sender != "3747574135655903" || receiver != "313591854" || msgType != "1" {
		t.Errorf("字段不符：sender=%s receiver=%s type=%s", sender, receiver, msgType)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(content), &parsed); err != nil || parsed["content"] != "在干嘛" {
		t.Errorf("content 结构不符：%s", content)
	}
	if csrf == "" {
		t.Error("缺少 csrf")
	}
}

// TestPostDynamicRequest 动态：content + csrf。
func TestPostDynamicRequest(t *testing.T) {
	var path, content, csrf string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		path = r.URL.Path
		content, csrf = r.PostForm.Get("content"), r.PostForm.Get("csrf")
		writeEnvelope(w, 0, nil)
	})

	if err := c.PostDynamic(context.Background(), "今天也在钓鱼"); err != nil {
		t.Fatalf("PostDynamic 失败：%v", err)
	}
	if path != "/dynamic_svr/v1/dynamic_svr/create" {
		t.Errorf("path = %s", path)
	}
	if content != "今天也在钓鱼" || csrf == "" {
		t.Errorf("表单不符：content=%q csrf=%q", content, csrf)
	}
}

// TestPlatformCodeBecomesError 平台返回非 0 code → 安全错误（不含 cookie）。
func TestPlatformCodeBecomesError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, 10031, nil)
	})

	err := c.Follow(context.Background(), "123")
	if err == nil {
		t.Fatal("非 0 code 应报错")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != 10031 {
		t.Errorf("错误类型/码不符：%v", err)
	}
	if strings.Contains(err.Error(), "secret-sess") || strings.Contains(err.Error(), "csrf-token-123") {
		t.Errorf("错误信息泄漏凭据：%v", err)
	}
}

// TestWriteRequiresCSRF 只有 SESSDATA 没有 bili_jct 时写操作直接拒绝。
func TestWriteRequiresCSRF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("缺 csrf 时不应发出请求")
	}))
	defer srv.Close()
	c, err := New(Options{APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL, Cookie: "SESSDATA=only"})
	if err != nil {
		t.Fatal(err)
	}
	if c.HasCSRF() {
		t.Error("HasCSRF 应为 false")
	}
	if err := c.Follow(context.Background(), "123"); err == nil || !strings.Contains(err.Error(), "bili_jct") {
		t.Errorf("应以缺少 bili_jct 拒绝：%v", err)
	}
	if err := c.Like(context.Background(), "1", 1); err == nil {
		t.Error("点赞也应拒绝")
	}
	if err := c.SendDM(context.Background(), "1", "hi"); err == nil {
		t.Error("私信也应拒绝")
	}
	if err := c.PostDynamic(context.Background(), "hi"); err == nil {
		t.Error("动态也应拒绝")
	}
}

// TestHTTPStatusAndNetworkErrors HTTP 非 200 / 网络错误都转成安全错误。
func TestHTTPStatusAndNetworkErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	var typed *Error
	if err := c.Follow(context.Background(), "123"); !errors.As(err, &typed) || typed.Status != http.StatusTooManyRequests {
		t.Errorf("应返回 429 错误：%v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // 关掉制造网络错误
	c2, err := New(Options{APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL, Cookie: testCookie, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.LiveStatus(context.Background(), "1"); err == nil {
		t.Error("网络错误应报错")
	}
}

// TestParseEnvelopeCode 只认 JSON 整数：缺失/null/字符串/浮点都不能当 code=0。
func TestParseEnvelopeCode(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		code int
		ok   bool
	}{
		{"零", `0`, 0, true},
		{"负数", `-352`, -352, true},
		{"缺失", ``, 0, false},
		{"null", `null`, 0, false},
		{"字符串", `"0"`, 0, false},
		{"浮点", `0.0`, 0, false},
		{"布尔", `false`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := ParseEnvelopeCode(json.RawMessage(tc.raw))
			if code != tc.code || ok != tc.ok {
				t.Fatalf("code=%d ok=%v want %d/%v", code, ok, tc.code, tc.ok)
			}
		})
	}
}
