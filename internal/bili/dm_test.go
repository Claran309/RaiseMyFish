package bili

// 本文件用 httptest 模拟私信接口：断言 URL/参数/csrf/错误码，不触真实平台。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestDMSessionsUnwrapsContent 会话 last_msg 与详情走同一套正文解包，降级时正文一致。
func TestDMSessionsUnwrapsContent(t *testing.T) {
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session_svr/v1/session_svr/get_sessions":
			_, _ = w.Write([]byte(`{"code":0,"data":{"session_list":[
				{"talker_id":"2002","last_msg":{"msg_key":"9001","sender_uid":"2002","content":"{\"content\":\"在吗\"}","timestamp":1700000000}}
			]}}`))
		case "/session_svr/v1/session_svr/get_session_msgs":
			_, _ = w.Write([]byte(`{"code":0,"data":{"messages":[
				{"msg_key":"9001","sender_uid":"2002","content":"{\"content\":\"在吗\"}","timestamp":1700000000}
			]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	sessions, err := c.DMSessions(context.Background(), 10)
	if err != nil {
		t.Fatalf("拉会话失败：%v", err)
	}
	if len(sessions) != 1 || sessions[0].Content != "在吗" {
		t.Fatalf("会话正文未解包：%+v", sessions)
	}
	messages, err := c.DMSessionMessages(context.Background(), "2002", 50)
	if err != nil {
		t.Fatalf("拉详情失败：%v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("详情解析不符：%+v", messages)
	}
	if messages[0].Content != sessions[0].Content {
		t.Fatalf("正常/降级正文不一致：detail=%q session=%q", messages[0].Content, sessions[0].Content)
	}
}

// TestDMSessionMessagesParses 断言会话详情路径/参数与内层 JSON 正文解析。
func TestDMSessionMessagesParses(t *testing.T) {
	var path, talker, sessionType, size string
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		talker = r.URL.Query().Get("talker_id")
		sessionType = r.URL.Query().Get("session_type")
		size = r.URL.Query().Get("size")
		_, _ = w.Write([]byte(`{"code":0,"data":{"messages":[
			{"msg_key":"9001","sender_uid":"2002","content":"{\"content\":\"第一条\"}","timestamp":1700000000},
			{"msg_key":9002,"sender_uid":2002,"content":"{\"content\":\"第二条\"}","timestamp":1700000001},
			{"msg_key":"9003","sender_uid":"2002","content":"{\"type\":1}","timestamp":1700000002}
		]}}`))
	})

	got, err := c.DMSessionMessages(context.Background(), "2002", 50)
	if err != nil {
		t.Fatalf("拉详情失败：%v", err)
	}
	if path != "/session_svr/v1/session_svr/get_session_msgs" {
		t.Errorf("路径不符：%s", path)
	}
	if talker != "2002" || sessionType != "1" || size != "50" {
		t.Errorf("参数不符：talker=%s type=%s size=%s", talker, sessionType, size)
	}
	if len(got) != 2 {
		t.Fatalf("非文本消息应跳过，实际 %d：%+v", len(got), got)
	}
	if got[0].MsgKey != "9001" || got[0].SenderUID != "2002" || got[0].Content != "第一条" {
		t.Errorf("第 1 条不符：%+v", got[0])
	}
	if got[1].MsgKey != "9002" || got[1].Content != "第二条" {
		t.Errorf("数字 msg_key 未解析：%+v", got[1])
	}
}

// TestDMSessionMessagesRejectsBadTalker 非数字 talker 本地拒绝，不发请求。
func TestDMSessionMessagesRejectsBadTalker(t *testing.T) {
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("非法 talker 不应发请求")
	})
	if _, err := c.DMSessionMessages(context.Background(), "abc", 50); err == nil {
		t.Fatal("非数字 talker 应报错")
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

// TestNewReadsCookieFile Cookie 为空时从 _FILE 读。
func TestNewReadsCookieFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookie.txt")
	if err := os.WriteFile(path, []byte("SESSDATA=from-file; bili_jct=csrf-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{CookieFile: path})
	if err != nil {
		t.Fatalf("从文件读凭据失败：%v", err)
	}
	if !c.HasCSRF() {
		t.Error("文件里的 bili_jct 应被识别")
	}
	// Cookie 优先于文件
	c2, err := New(Options{Cookie: "SESSDATA=direct", CookieFile: path})
	if err != nil || c2.HasCSRF() {
		t.Errorf("Cookie 非空时应忽略文件：%v / csrf=%v", err, c2.HasCSRF())
	}
	// 文件不存在 → 未配置
	if _, err := New(Options{CookieFile: filepath.Join(t.TempDir(), "nope.txt")}); !errors.Is(err, ErrNoCredential) {
		t.Errorf("缺失文件应返回 ErrNoCredential：%v", err)
	}
}

// TestDMSessionsPageCursorAndSeqno 断言会话分页参数与 seqno 解析。
func TestDMSessionsPageCursorAndSeqno(t *testing.T) {
	var size, endTS string
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		size = r.URL.Query().Get("size")
		endTS = r.URL.Query().Get("end_ts")
		_, _ = w.Write([]byte(`{"code":0,"data":{"session_list":[
			{"talker_id":"2002","max_seqno":12,"last_msg":{"msg_key":"9001","msg_seqno":12,"sender_uid":"2002","content":"在吗","timestamp":1700000000}}
		],"has_more":1}}`))
	})
	page, err := c.DMSessionsPage(context.Background(), 25, 1700000000123456)
	if err != nil {
		t.Fatalf("拉会话失败：%v", err)
	}
	if size != "25" || endTS != "1700000000123456" {
		t.Errorf("分页参数不符：size=%s end_ts=%s", size, endTS)
	}
	if !page.HasMore || len(page.Sessions) != 1 {
		t.Fatalf("分页解析不符：%+v", page)
	}
	if got := page.Sessions[0]; got.MsgSeqno != "12" || got.MaxSeqno != "12" {
		t.Errorf("seqno 未解析：%+v", got)
	}
}

// TestDMSessionMessagesPageCursor 断言 end_seqno 参数、原始条数与 min/max seqno。
func TestDMSessionMessagesPageCursor(t *testing.T) {
	var talker, size, endSeqno string
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		talker = r.URL.Query().Get("talker_id")
		size = r.URL.Query().Get("size")
		endSeqno = r.URL.Query().Get("end_seqno")
		_, _ = w.Write([]byte(`{"code":0,"data":{"messages":[
			{"msg_key":"11","msg_seqno":11,"sender_uid":"2002","content":"{\"content\":\"最新\"}","timestamp":1700000011},
			{"msg_key":"10","msg_seqno":10,"sender_uid":"2002","content":"{\"type\":1}","timestamp":1700000010},
			{"msg_key":"9","msg_seqno":9,"sender_uid":"2002","content":"{\"content\":\"更早\"}","timestamp":1700000009}
		],"has_more":1,"min_seqno":9,"max_seqno":11}}`))
	})
	page, err := c.DMSessionMessagesPage(context.Background(), "2002", 2, "12")
	if err != nil {
		t.Fatalf("拉详情失败：%v", err)
	}
	if talker != "2002" || size != "2" || endSeqno != "12" {
		t.Errorf("分页参数不符：talker=%s size=%s end_seqno=%s", talker, size, endSeqno)
	}
	if page.Count != 3 {
		t.Errorf("原始条数 = %d，期望 3", page.Count)
	}
	if len(page.Messages) != 2 {
		t.Fatalf("非文本应跳过：%+v", page.Messages)
	}
	if page.MinSeqno != "9" || page.MaxSeqno != "11" {
		t.Errorf("min/max 应覆盖非文本消息：%s/%s", page.MinSeqno, page.MaxSeqno)
	}
	if !page.HasMore {
		t.Error("has_more=1 应解析为 true")
	}
	if page.Messages[0].Seqno != "11" || page.Messages[1].Seqno != "9" {
		t.Errorf("消息 seqno 未解析：%+v", page.Messages)
	}
}

// TestDMSessionMessagesPageLegacyHasMore 旧响应无 has_more 时按「取满一页」保守判断。
func TestDMSessionMessagesPageLegacyHasMore(t *testing.T) {
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"messages":[
			{"msg_key":"1","msg_seqno":1,"sender_uid":"2002","content":"{\"content\":\"hi\"}","timestamp":1700000001}
		]}}`))
	})
	full, err := c.DMSessionMessagesPage(context.Background(), "2002", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if !full.HasMore {
		t.Error("取满一页且无 has_more 应保守认为还有更早消息")
	}
	part, err := c.DMSessionMessagesPage(context.Background(), "2002", 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if part.HasMore {
		t.Error("未取满一页应认为没有更早消息")
	}
}

// TestDMSessionMessagesPageRejectsCursor 非法 end_seqno 本地拒绝，不发请求。
func TestDMSessionMessagesPageRejectsCursor(t *testing.T) {
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("非法游标不应发请求")
	})
	if _, err := c.DMSessionMessagesPage(context.Background(), "2002", 50, "abc"); err == nil {
		t.Fatal("非数字游标应报错")
	}
}

// TestDMSessionsPageLegacyHasMore 旧响应无 has_more 时按「取满一页」保守判断。
func TestDMSessionsPageLegacyHasMore(t *testing.T) {
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"session_list":[
			{"talker_id":"2002","last_msg":{"msg_key":"1","sender_uid":"2002","content":"在吗","timestamp":1700000000}}
		]}}`))
	})
	full, err := c.DMSessionsPage(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !full.HasMore {
		t.Error("取满一页且无 has_more 应保守认为还有更早会话")
	}
	part, err := c.DMSessionsPage(context.Background(), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if part.HasMore {
		t.Error("未取满一页应认为没有更早会话")
	}
}

// TestDMSessionMessagesPagePrefersPageExtrema 响应 min/max 若是整会话范围，
// 以本页实际消息的极值为准，避免游标过早停住。
func TestDMSessionMessagesPagePrefersPageExtrema(t *testing.T) {
	c := dmClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"messages":[
			{"msg_key":"11","msg_seqno":11,"sender_uid":"2002","content":"{\"content\":\"新\"}","timestamp":1700000011},
			{"msg_key":"9","msg_seqno":9,"sender_uid":"2002","content":"{\"content\":\"旧\"}","timestamp":1700000009}
		],"has_more":0,"min_seqno":1,"max_seqno":999}}`))
	})
	page, err := c.DMSessionMessagesPage(context.Background(), "2002", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.MinSeqno != "9" || page.MaxSeqno != "11" {
		t.Fatalf("应以本页消息极值为准：min=%s max=%s", page.MinSeqno, page.MaxSeqno)
	}
}
