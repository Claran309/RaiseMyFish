package bili

// 本文件用 httptest 模拟私信接口：断言 URL/参数/csrf/错误码，不触真实平台。

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

func dmClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL,
		Cookie:  "SESSDATA=s; bili_jct=csrf-1",
		SelfMID: "3747574135655903",
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestDMSessionsParses 断言路径与解析（字符串/数字 uid 都认）。
func TestDMSessionsParses(t *testing.T) {
	var gotPath, gotCookie string
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{"code":0,"data":{"session_list":[
			{"talker_id":"2002","last_msg":{"msg_key":"9001","sender_uid":"2002","content":"在吗","timestamp":1700000000}},
			{"talker_id":3003,"last_msg":{"msg_key":9002,"sender_uid":3003,"content":"看看这个","timestamp":1700000100}}
		]}}`))
	})

	got, err := c.DMSessions(context.Background(), 10)
	if err != nil {
		t.Fatalf("拉会话失败：%v", err)
	}
	if gotPath != "/session_svr/v1/session_svr/get_sessions" {
		t.Errorf("路径不符：%s", gotPath)
	}
	if gotCookie == "" {
		t.Error("未带 cookie")
	}
	if len(got) != 2 || got[0].TalkerID != "2002" || got[0].MsgKey != "9001" {
		t.Fatalf("解析不符：%+v", got)
	}
	if got[1].TalkerID != "3003" {
		t.Errorf("数字 uid 未解析：%+v", got[1])
	}
}

// TestSendMessageParams 断言 POST、msg[...] 参数与 csrf。
func TestSendMessageParams(t *testing.T) {
	var method, path, sender, receiver, content, csrf string
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/web_im/v1/web_im/send_msg" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = r.ParseForm()
		method, path = r.Method, r.URL.Path
		sender = r.PostForm.Get("msg[sender_uid]")
		receiver = r.PostForm.Get("msg[receiver_id]")
		content = r.PostForm.Get("msg[content]")
		csrf = r.PostForm.Get("csrf")
		_, _ = w.Write([]byte(`{"code":0,"data":{"msg_key":"777888"}}`))
	})

	msgID, err := c.SendMessage(context.Background(), "313591854", "主人好")
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if method != http.MethodPost || path != "/web_im/v1/web_im/send_msg" {
		t.Errorf("请求不符：%s %s", method, path)
	}
	if sender != "3747574135655903" || receiver != "313591854" {
		t.Errorf("收发件人不符：%s -> %s", sender, receiver)
	}
	if csrf != "csrf-1" {
		t.Errorf("csrf 不符：%q", csrf)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(content), &parsed); err != nil || parsed["content"] != "主人好" {
		t.Errorf("content 结构不符：%s", content)
	}
	if msgID != "777888" {
		t.Errorf("msg_key 未返回：%q", msgID)
	}
}

// TestSendMessagePlatformCode 平台 code≠0 → 错误，且不含凭据。
func TestSendMessagePlatformCode(t *testing.T) {
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":21019,"message":"对方不是你的粉丝"}`))
	})
	_, err := c.SendMessage(context.Background(), "313591854", "你好")
	if err == nil {
		t.Fatal("非 0 code 应报错")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != 21019 {
		t.Errorf("错误码未透传：%v", err)
	}
	if strings.Contains(err.Error(), "csrf-1") || strings.Contains(err.Error(), "SESSDATA") {
		t.Errorf("错误泄漏凭据：%v", err)
	}
}

// TestSendMessageNeedsCSRF 缺 bili_jct 时本地拒绝、不发请求。
func TestSendMessageNeedsCSRF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("缺 csrf 不应发请求")
	}))
	defer srv.Close()
	c, _ := New(Options{APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL, Cookie: "SESSDATA=only", SelfMID: "1"})
	if _, err := c.SendMessage(context.Background(), "2", "hi"); err == nil {
		t.Error("缺 csrf 应拒绝")
	}
}

// TestRelationAttribute 断言关系查询路径与解析（6=互关）。
func TestRelationAttribute(t *testing.T) {
	var path, fid string
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fid = r.URL.Query().Get("fid")
		_, _ = w.Write([]byte(`{"code":0,"data":{"attribute":6}}`))
	})
	attr, err := c.RelationAttribute(context.Background(), "313591854")
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if path != "/x/relation" || fid != "313591854" {
		t.Errorf("请求不符：%s fid=%s", path, fid)
	}
	if attr != 6 {
		t.Errorf("attribute = %d，期望 6", attr)
	}
}
