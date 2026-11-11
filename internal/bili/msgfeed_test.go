package bili

// 本文件用 httptest 模拟账号消息接口：断言路径/游标/凭据/Referer 与解析。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMsgfeedRepliesParsesItemsAndCursor(t *testing.T) {
	var gotPath, gotQuery, gotCookie, gotReferer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotCookie = r.Header.Get("Cookie")
		gotReferer = r.Header.Get("Referer")
		_, _ = w.Write([]byte(`{
			"code":0,
			"data":{
				"items":[
					{"id":1001,"user":{"mid":42,"nickname":"小明"},"item":{"type":"reply","business_id":1,"subject_id":"12345","source_id":"777","root_id":"666","source_content":"回复你"},"reply_time":1700000000},
					{"id":"1002","user":{"mid":"43","nickname":"小红"},"item":{"type":"reply","business_id":"17","subject_id":888,"source_id":778,"root_id":0,"source_content":"动态里回你"},"reply_time":1700000001}
				],
				"cursor":{"is_end":false,"id":1002,"time":1700000001},
				"last_view_at":1699999999
			}
		}`))
	}))
	defer server.Close()

	client, err := New(Options{APIRoot: server.URL, Cookie: testCookie, SelfMID: "42"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.MsgfeedReplies(context.Background(), "999", 1700000000)
	if err != nil {
		t.Fatalf("拉取失败：%v", err)
	}
	if gotPath != msgfeedReplyPath {
		t.Errorf("路径 = %q", gotPath)
	}
	for _, want := range []string{"id=999", "reply_time=1700000000", "mobi_app=web"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query 缺少 %q：%s", want, gotQuery)
		}
	}
	if gotCookie != testCookie || !strings.HasPrefix(gotReferer, "https://message.bilibili.com") {
		t.Errorf("凭据/Referer 不符：%q %q", gotCookie, gotReferer)
	}
	if len(page.Items) != 2 {
		t.Fatalf("消息数量 = %d", len(page.Items))
	}
	first, second := page.Items[0], page.Items[1]
	if first.ID != "1001" || first.BusinessID != 1 || first.SubjectID != "12345" || first.SourceID != "777" || first.RootID != "666" ||
		first.UserMID != "42" || first.UserName != "小明" || first.Text != "回复你" || first.Time != 1700000000 {
		t.Errorf("第一条解析不符：%+v", first)
	}
	if second.ID != "1002" || second.BusinessID != 17 || second.SubjectID != "888" || second.SourceID != "778" || second.RootID != "" || second.Time != 1700000001 {
		t.Errorf("第二条解析不符：%+v", second)
	}
	if page.CursorID != "1002" || page.CursorTime != 1700000001 || page.IsEnd || page.LastViewAt != 1699999999 {
		t.Errorf("游标解析不符：%+v", page)
	}
}

func TestMsgfeedAtsParsesAtTime(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"user":{"mid":9,"nickname":"阿蓝"},"item":{"type":"reply","business_id":1,"subject_id":2,"source_id":3,"root_id":0,"source_content":"@鱼 看看"},"at_time":1700000123}],"cursor":{"is_end":true,"id":1,"time":1700000123},"last_view_at":0}}`))
	}))
	defer server.Close()

	client, err := New(Options{APIRoot: server.URL, Cookie: testCookie})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.MsgfeedAts(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != msgfeedAtPath {
		t.Errorf("路径 = %q", gotPath)
	}
	if strings.Contains(gotQuery, "id=") || strings.Contains(gotQuery, "at_time=") {
		t.Errorf("无游标时不应带翻页参数：%s", gotQuery)
	}
	if len(page.Items) != 1 || page.Items[0].Time != 1700000123 || !page.IsEnd {
		t.Fatalf("解析不符：%+v", page)
	}
}

func TestMsgfeedAuthErrorIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":-101,"message":"账号未登录"}`))
	}))
	defer server.Close()
	client, err := New(Options{APIRoot: server.URL, Cookie: testCookie})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.MsgfeedReplies(context.Background(), "", 0); !IsAuthError(err) {
		t.Fatalf("未识别鉴权错误：%v", err)
	}
}

func TestSelfMID(t *testing.T) {
	client, err := New(Options{APIRoot: "http://127.0.0.1:1", Cookie: testCookie, SelfMID: "42"})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := client.SelfMID(context.Background())
	if err != nil || mid != "42" {
		t.Fatalf("SelfMID = %q, %v", mid, err)
	}
	empty, err := New(Options{APIRoot: "http://127.0.0.1:1", Cookie: "SESSDATA=x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.SelfMID(context.Background()); err == nil {
		t.Fatal("缺少 UID 应报错")
	}
}
