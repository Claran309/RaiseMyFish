package comment

// 本文件用 httptest 模拟评论区：断言查询参数、Cookie、解析字段、rpid 去重与自评论过滤。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

const testCookie = "SESSDATA=fixture; bili_jct=csrf"

type replyJSON map[string]any

// commentServer 是可动态追加评论的假平台，记录每次请求的查询参数与 Cookie。
type commentServer struct {
	mu      sync.Mutex
	replies map[string][]replyJSON
	code    int
	queries []url.Values
	cookies []string
}

func newCommentServer(t *testing.T) (*commentServer, *httptest.Server) {
	t.Helper()
	fixture := &commentServer{replies: map[string][]replyJSON{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x/v2/reply" {
			http.NotFound(w, r)
			return
		}
		fixture.mu.Lock()
		fixture.queries = append(fixture.queries, r.URL.Query())
		fixture.cookies = append(fixture.cookies, r.Header.Get("Cookie"))
		code := fixture.code
		replies := append([]replyJSON(nil), fixture.replies[r.URL.Query().Get("oid")]...)
		fixture.mu.Unlock()
		payload := map[string]any{"code": code, "message": "0", "data": map[string]any{"replies": replies}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	return fixture, srv
}

func (s *commentServer) add(oid string, replies ...replyJSON) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[oid] = append(s.replies[oid], replies...)
}

func (s *commentServer) snapshot() ([]url.Values, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.queries...), append([]string(nil), s.cookies...)
}

func reply(rpid, mid int, uname, text string) replyJSON {
	return replyJSON{
		"rpid":    rpid,
		"mid":     mid,
		"ctime":   1700000000,
		"root":    1000,
		"parent":  rpid,
		"member":  map[string]any{"mid": mid, "uname": uname},
		"content": map[string]any{"message": text},
	}
}

func newCollector(t *testing.T, root string, videos []string, interval time.Duration) *Collector {
	t.Helper()
	o := DefaultOptions()
	o.Videos = videos
	o.Interval = interval
	o.SelfID = "999"
	o.Cookie = testCookie
	o.APIRoot = root
	o.HTTPTimeout = time.Second
	c, err := New(o, zap.NewNop())
	if err != nil {
		t.Fatalf("构造采集器失败：%v", err)
	}
	return c
}

type fakeDiscoverer struct {
	targets []Target
	err     error
	calls   int
}

func (f *fakeDiscoverer) Discover(context.Context) ([]Target, error) {
	f.calls++
	return f.targets, f.err
}

func startCollector(t *testing.T, c *Collector) (context.CancelFunc, chan event.Event, chan error, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan event.Event, 32)
	errc := make(chan error, 16)
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, out, errc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("采集器未在超时内退出")
		}
	})
	return cancel, out, errc, done
}

func awaitEvent(t *testing.T, out <-chan event.Event) event.Event {
	t.Helper()
	select {
	case ev := <-out:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("等待评论事件超时")
		return event.Event{}
	}
}

func TestCollectorParsesFiltersAndDeduplicates(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.add("123",
		reply(1001, 42, "路人甲", "第一条评论"),
		reply(1002, 999, "本鱼自己", "自己的评论"),
		reply(1005, 43, "路人丙", "   "),
		reply(1003, 44, "路人乙", "第二条评论"),
	)
	c := newCollector(t, srv.URL, []string{"123"}, 20*time.Millisecond)
	cancel, out, errc, _ := startCollector(t, c)

	first := awaitEvent(t, out)
	second := awaitEvent(t, out)
	got := map[string]event.Event{first.ID: first, second.ID: second}
	ev, ok := got["comment:123:1001"]
	if !ok {
		t.Fatalf("缺少 1001 事件：%+v", got)
	}
	if ev.Channel != event.ChComment || ev.Session != "123" || ev.FromID != "42" || ev.FromName != "路人甲" || ev.Text != "第一条评论" || ev.ReplyTo != "1001" || ev.Private || ev.Status != event.StatusPending || ev.Dir != event.DirIn {
		t.Errorf("事件字段不符：%+v", ev)
	}
	if want := time.Unix(1700000000, 0).UTC(); !ev.At.Equal(want) {
		t.Errorf("At = %v，期望 %v", ev.At, want)
	}
	var meta map[string]any
	if err := json.Unmarshal(ev.Meta, &meta); err != nil || meta["source"] != "bili-comment" || meta["root"] != "1000" || meta["parent"] != "1001" {
		t.Errorf("Meta 不符：%s (%v)", ev.Meta, err)
	}
	if _, ok := got["comment:123:1003"]; !ok {
		t.Errorf("缺少 1003 事件：%+v", got)
	}
	if _, ok := got["comment:123:1002"]; ok {
		t.Error("不应采集自己的评论")
	}
	if _, ok := got["comment:123:1005"]; ok {
		t.Error("空白评论不应产生事件")
	}

	// 多轮轮询后仍不应收到重复 rpid。
	select {
	case ev := <-out:
		t.Fatalf("重复轮询产生了重复事件：%+v", ev)
	case <-time.After(120 * time.Millisecond):
	}
	select {
	case err := <-errc:
		if err == nil {
			t.Error("空白评论应报告协议错误")
		}
	default:
	}

	// 新出现的 rpid 仍应被采集。
	fixture.add("123", reply(1004, 45, "路人丙", "新评论"))
	if ev := awaitEvent(t, out); ev.ID != "comment:123:1004" {
		t.Fatalf("新评论事件 ID = %s", ev.ID)
	}

	queries, cookies := fixture.snapshot()
	if len(queries) == 0 {
		t.Fatal("未记录到请求")
	}
	q := queries[0]
	if q.Get("type") != "1" || q.Get("oid") != "123" || q.Get("pn") != "1" || q.Get("ps") != "20" || q.Get("sort") != "2" {
		t.Errorf("查询参数不符：%v", q)
	}
	if len(cookies) == 0 || cookies[0] != testCookie {
		t.Errorf("Cookie 头不符：%v", cookies)
	}

	cancel()
}

// TestCollectorTopLevelZeroRoot 顶层评论 root/parent=0：不能把整批解析打挂，Meta 省略空字段。
func TestCollectorTopLevelZeroRoot(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.add("123", replyJSON{
		"rpid":    2001,
		"mid":     42,
		"ctime":   1700000000,
		"root":    0,
		"parent":  0,
		"member":  map[string]any{"mid": 42, "uname": "路人"},
		"content": map[string]any{"message": "顶层评论"},
	})
	c := newCollector(t, srv.URL, []string{"123"}, 20*time.Millisecond)
	cancel, out, _, _ := startCollector(t, c)
	defer cancel()

	ev := awaitEvent(t, out)
	if ev.ID != "comment:123:2001" || ev.ReplyTo != "2001" {
		t.Fatalf("顶层评论事件不符：%+v", ev)
	}
	var meta map[string]any
	if err := json.Unmarshal(ev.Meta, &meta); err != nil {
		t.Fatalf("Meta 不是合法 JSON：%v", err)
	}
	if _, ok := meta["root"]; ok {
		t.Errorf("顶层评论不应带 root：%s", ev.Meta)
	}
	if _, ok := meta["parent"]; ok {
		t.Errorf("顶层评论不应带 parent：%s", ev.Meta)
	}
}

// TestCollectorAutoDiscoverMergesTargets 覆盖：手动+视频+动态合并、动态 type=17 与 comment_type。
func TestCollectorAutoDiscoverMergesTargets(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.add("100", reply(101, 41, "甲", "手动视频"))
	fixture.add("200", reply(201, 42, "乙", "自动视频"))
	fixture.add("300", reply(301, 43, "丙", "动态评论"))
	discoverer := &fakeDiscoverer{targets: []Target{{OID: "200", Type: TypeVideo}, {OID: "300", Type: TypeDynamic}, {OID: "200", Type: TypeVideo}}}

	o := DefaultOptions()
	o.Videos = []string{"100"}
	o.Interval = 20 * time.Millisecond
	o.AutoDiscover = true
	o.DiscoverInterval = time.Hour
	o.Discoverer = discoverer
	o.SelfID = "999"
	o.Cookie = testCookie
	o.APIRoot = srv.URL
	o.HTTPTimeout = time.Second
	c, err := New(o, zap.NewNop())
	if err != nil {
		t.Fatalf("构造采集器失败：%v", err)
	}
	_, out, _, _ := startCollector(t, c)

	got := map[string]event.Event{}
	for i := 0; i < 3; i++ {
		ev := awaitEvent(t, out)
		got[ev.ID] = ev
	}
	if len(got) != 3 || got["comment:100:101"].Session != "100" || got["comment:200:201"].Session != "200" || got["comment:300:301"].Session != "300" {
		t.Fatalf("自动发现事件不符：%+v", got)
	}
	var meta map[string]any
	if err := json.Unmarshal(got["comment:300:301"].Meta, &meta); err != nil || meta["comment_type"] != float64(TypeDynamic) {
		t.Errorf("动态评论 Meta 不符：%s (%v)", got["comment:300:301"].Meta, err)
	}
	if discoverer.calls == 0 {
		t.Error("自动发现未被调用")
	}
	queries, _ := fixture.snapshot()
	types := map[string]string{}
	for _, q := range queries {
		types[q.Get("oid")] = q.Get("type")
	}
	if types["300"] != "17" || types["200"] != "1" || types["100"] != "1" {
		t.Errorf("评论类型查询不符：%v", types)
	}
}

// TestDiscoverFailureFallsBackToManual 覆盖：发现失败只轮询手动列表且不向 err 通道报错。
func TestDiscoverFailureFallsBackToManual(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.add("100", reply(101, 41, "甲", "手动评论"))
	o := DefaultOptions()
	o.Videos = []string{"100"}
	o.Interval = 20 * time.Millisecond
	o.AutoDiscover = true
	o.DiscoverInterval = time.Hour
	o.Discoverer = &fakeDiscoverer{err: errors.New("平台超时")}
	o.SelfID = "999"
	o.Cookie = testCookie
	o.APIRoot = srv.URL
	o.HTTPTimeout = time.Second
	c, err := New(o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	_, out, errc, _ := startCollector(t, c)
	if ev := awaitEvent(t, out); ev.ID != "comment:100:101" {
		t.Fatalf("手动列表未继续轮询：%+v", ev)
	}
	select {
	case err := <-errc:
		t.Fatalf("发现失败不应上报错误：%v", err)
	default:
	}
}

func TestBiliDiscovererPartial(t *testing.T) {
	source := &fakeSpaceSource{
		videos:   []bili.SpaceVideo{{AID: "11"}},
		dynamics: []bili.SpaceDynamic{{ID: "22"}},
	}
	got, err := BiliDiscoverer{Client: source, MID: "42", VideoLimit: 5, DynamicLimit: 5}.Discover(context.Background())
	if err != nil {
		t.Fatalf("发现失败：%v", err)
	}
	if len(got) != 2 || got[0] != (Target{OID: "11", Type: TypeVideo}) || got[1] != (Target{OID: "22", Type: TypeDynamic}) {
		t.Fatalf("target 映射不符：%+v", got)
	}
	source.videoErr = errors.New("视频失败")
	got, err = BiliDiscoverer{Client: source, MID: "42"}.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Type != TypeDynamic {
		t.Fatalf("部分失败应保留动态：%+v (%v)", got, err)
	}
	source.dynamicErr = errors.New("动态失败")
	if _, err := (BiliDiscoverer{Client: source, MID: "42"}).Discover(context.Background()); err == nil {
		t.Fatal("两类都失败应返回错误")
	}
}

type fakeSpaceSource struct {
	videos     []bili.SpaceVideo
	dynamics   []bili.SpaceDynamic
	videoErr   error
	dynamicErr error
}

func (f *fakeSpaceSource) SpaceVideos(context.Context, string, int) ([]bili.SpaceVideo, error) {
	return f.videos, f.videoErr
}

func (f *fakeSpaceSource) SpaceDynamics(context.Context, string, int) ([]bili.SpaceDynamic, error) {
	return f.dynamics, f.dynamicErr
}

func TestCollectorMultipleVideos(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.add("1", reply(11, 41, "甲", "视频一"))
	fixture.add("2", reply(22, 42, "乙", "视频二"))
	c := newCollector(t, srv.URL, []string{"1", "2"}, 50*time.Millisecond)
	_, out, _, _ := startCollector(t, c)

	got := map[string]string{}
	for i := 0; i < 2; i++ {
		ev := awaitEvent(t, out)
		got[ev.ID] = ev.Session
	}
	if got["comment:1:11"] != "1" || got["comment:2:22"] != "2" {
		t.Errorf("多视频事件不符：%+v", got)
	}
}

func TestCollectorAuthStops(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.code = -101
	c := newCollector(t, srv.URL, []string{"123"}, time.Minute)
	out := make(chan event.Event, 1)
	errc := make(chan error, 4)
	err := c.Start(context.Background(), out, errc)
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("凭据失效应返回 auth 错误，实际 %v", err)
	}
	select {
	case reported := <-errc:
		if reported == nil {
			t.Error("err channel 收到 nil")
		}
	default:
		t.Error("凭据失效未上报 err channel")
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	valid := func() Options {
		o := DefaultOptions()
		o.Videos = []string{"123"}
		o.Cookie = testCookie
		return o
	}
	cases := []struct {
		name string
		edit func(*Options)
	}{
		{"无视频且不自动发现", func(o *Options) { o.Videos = nil }},
		{"自动发现缺Disoverer", func(o *Options) { o.Videos = nil; o.AutoDiscover = true }},
		{"发现间隔过小", func(o *Options) {
			o.AutoDiscover = true
			o.Discoverer = &fakeDiscoverer{}
			o.DiscoverInterval = time.Second
		}},
		{"发现间隔过大", func(o *Options) {
			o.AutoDiscover = true
			o.Discoverer = &fakeDiscoverer{}
			o.DiscoverInterval = 25 * time.Hour
		}},
		{"非法oid", func(o *Options) { o.Videos = []string{"abc"} }},
		{"零oid", func(o *Options) { o.Videos = []string{"000"} }},
		{"零间隔", func(o *Options) { o.Interval = 0 }},
		{"无超时", func(o *Options) { o.HTTPTimeout = 0 }},
		{"无凭据", func(o *Options) { o.Cookie = "" }},
		{"凭据换行", func(o *Options) { o.Cookie = "SESSDATA=x\r\nX: 1" }},
		{"非法self", func(o *Options) { o.SelfID = "self" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := valid()
			tc.edit(&o)
			if _, err := New(o, zap.NewNop()); err == nil {
				t.Fatal("非法配置未被拒绝")
			}
		})
	}
	if _, err := New(valid(), nil); err == nil {
		t.Fatal("nil 日志器未被拒绝")
	}
}

func TestDecimalAcceptsNumberAndString(t *testing.T) {
	var got struct {
		A decimal `json:"a"`
		B decimal `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":123,"b":"456"}`), &got); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got.A != "123" || got.B != "456" {
		t.Errorf("decimal = %q/%q", got.A, got.B)
	}
	for _, body := range []string{`{"a":0}`, `{"a":-1}`, `{"a":1.5}`, `{"a":"x"}`} {
		var v struct {
			A decimal `json:"a"`
		}
		if err := json.Unmarshal([]byte(body), &v); err == nil {
			t.Errorf("非法 ID 未被拒绝：%s", strings.TrimSpace(body))
		}
	}
}
