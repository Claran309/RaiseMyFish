package bili

// 本文件用 httptest 模拟视频/动态列表：断言 WBI 签名、解析与密钥缓存。

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	testWbiImg = "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png"
	testWbiSub = "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"
	testMixin  = "ea1db124af3c7062474693fa704f4ff8"
)

func TestWbiMixinKeyKnownVector(t *testing.T) {
	if got := wbiMixinKey(testWbiImg, testWbiSub); got != testMixin {
		t.Fatalf("mixin key = %q，期望 %q", got, testMixin)
	}
	if got := wbiMixinKey("", ""); got != "" {
		t.Fatalf("空输入应返回空，实际 %q", got)
	}
}

// verifyWBI 按同一规则重算 w_rid，确认 query 确实被签名。
func verifyWBI(t *testing.T, query url.Values) {
	t.Helper()
	rid := query.Get("w_rid")
	if len(rid) != 32 {
		t.Fatalf("w_rid 长度不符：%q", rid)
	}
	if _, err := time.ParseDuration(query.Get("wts") + "s"); err != nil {
		t.Fatalf("wts 不是秒级时间戳：%q", query.Get("wts"))
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		if key != "w_rid" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var raw strings.Builder
	for i, key := range keys {
		if i > 0 {
			raw.WriteByte('&')
		}
		raw.WriteString(url.QueryEscape(key))
		raw.WriteByte('=')
		raw.WriteString(url.QueryEscape(filterWBIValue(query.Get(key))))
	}
	sum := md5.Sum([]byte(raw.String() + testMixin))
	if want := hex.EncodeToString(sum[:]); rid != want {
		t.Fatalf("w_rid = %s，期望 %s（query=%s）", rid, want, raw.String())
	}
}

func spaceClient(t *testing.T, h http.HandlerFunc) (*Client, *int) {
	t.Helper()
	navCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			navCalls++
			_, _ = w.Write([]byte(`{"code":0,"data":{"wbi_img":{"img_url":"` + testWbiImg + `","sub_url":"` + testWbiSub + `"}}}`))
		default:
			h(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{
		APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL,
		Cookie:  "SESSDATA=s",
		SelfMID: "42",
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, &navCalls
}

func TestSpaceVideosSignedAndParsed(t *testing.T) {
	var gotQuery url.Values
	c, navCalls := spaceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x/space/wbi/arc/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"code":0,"data":{"list":{"vlist":[
			{"aid":1001,"bvid":"BV1xx","title":"第一集","created":1700000000},
			{"aid":"1002","bvid":"BV1yy","title":" 第二集 ","created":1700000100},
			{"aid":0,"bvid":"BVbad"},
			{"aid":"abc","bvid":"BVbad2"}
		]}}}`))
	})
	got, err := c.SpaceVideos(context.Background(), "", 2)
	if err != nil {
		t.Fatalf("拉视频失败：%v", err)
	}
	if len(got) != 2 || got[0].AID != "1001" || got[1].AID != "1002" {
		t.Fatalf("视频解析不符：%+v", got)
	}
	if got[1].Title != "第二集" {
		t.Errorf("标题未去空格：%q", got[1].Title)
	}
	if gotQuery.Get("mid") != "42" || gotQuery.Get("ps") != "2" || gotQuery.Get("order") != "pubdate" {
		t.Errorf("查询参数不符：%v", gotQuery)
	}
	verifyWBI(t, gotQuery)

	// 第二次使用缓存，不再请求 nav。
	if _, err := c.SpaceVideos(context.Background(), "42", 1); err != nil {
		t.Fatal(err)
	}
	if *navCalls != 1 {
		t.Errorf("nav 应只请求一次（缓存），实际 %d", *navCalls)
	}
}

func TestSpaceDynamicsParsed(t *testing.T) {
	var gotQuery url.Values
	c, _ := spaceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x/polymer/web-dynamic/v1/feed/space" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[
			{"id_str":"9001","type":"DYNAMIC_TYPE_DRAW","basic":{"comment_id_str":"415114992","comment_type":11}},
			{"id_str":9002,"type":"DYNAMIC_TYPE_FORWARD","basic":{"comment_id_str":9002,"comment_type":17}},
			{"id_str":"bad"}
		]}}`))
	})
	got, err := c.SpaceDynamics(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("拉动态失败：%v", err)
	}
	if len(got) != 2 || got[0].ID != "9001" || got[1].ID != "9002" {
		t.Fatalf("动态解析不符：%+v", got)
	}
	if got[0].CommentID != "415114992" || got[0].CommentType != 11 {
		t.Errorf("图文动态评论区未解析：%+v", got[0])
	}
	if got[1].CommentType != 17 {
		t.Errorf("普通动态评论类型未解析：%+v", got[1])
	}
	if gotQuery.Get("host_mid") != "42" {
		t.Errorf("host_mid 不符：%v", gotQuery)
	}
}

func TestSpaceRequiresCredential(t *testing.T) {
	var nilClient *Client
	if _, err := nilClient.SpaceVideos(context.Background(), "42", 1); err == nil {
		t.Fatal("nil 客户端应报未配置凭据")
	}
	c, err := New(Options{APIRoot: "http://127.0.0.1:1"})
	if err == nil {
		t.Fatal("无凭据不应构造出客户端")
	}
	_ = c
}
