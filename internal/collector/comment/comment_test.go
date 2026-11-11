package comment

// 本文件用 httptest 模拟评论区：断言查询参数、Cookie、解析字段、rpid 去重与自评论过滤。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const testCookie = "SESSDATA=fixture; bili_jct=csrf"

type replyJSON map[string]any

// commentServer 是可动态追加评论的假平台，记录每次请求的查询参数与 Cookie。
// 顶层评论按时间倒序、楼中楼按时间正序分页；fail 里的一次性状态码用于模拟翻页失败。
type commentServer struct {
	mu         sync.Mutex
	replies    map[string][]replyJSON
	subReplies map[string][]replyJSON
	code       int
	fail       map[string]int
	after      func(url.Values)
	queries    []url.Values
	cookies    []string
}

func newCommentServer(t *testing.T) (*commentServer, *httptest.Server) {
	t.Helper()
	fixture := &commentServer{
		replies:    map[string][]replyJSON{},
		subReplies: map[string][]replyJSON{},
		fail:       map[string]int{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub := r.URL.Path == "/x/v2/reply/reply"
		if !sub && r.URL.Path != "/x/v2/reply" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		pn := queryInt(q.Get("pn"), 1)
		ps := queryInt(q.Get("ps"), 20)
		fixture.mu.Lock()
		fixture.queries = append(fixture.queries, q)
		fixture.cookies = append(fixture.cookies, r.Header.Get("Cookie"))
		key := pageKey(r.URL.Path, q)
		status := fixture.fail[key]
		if status != 0 {
			delete(fixture.fail, key)
		}
		var all []replyJSON
		if sub {
			all = append([]replyJSON(nil), fixture.subReplies[q.Get("root")]...)
			sort.SliceStable(all, func(i, j int) bool {
				left, right := replyInt(all[i]["ctime"]), replyInt(all[j]["ctime"])
				if left != right {
					return left < right
				}
				return replyText(all[i]["rpid"]) < replyText(all[j]["rpid"])
			})
		} else {
			all = append([]replyJSON(nil), fixture.replies[q.Get("oid")]...)
			sort.SliceStable(all, func(i, j int) bool {
				left, right := replyInt(all[i]["ctime"]), replyInt(all[j]["ctime"])
				if left != right {
					return left > right
				}
				return replyText(all[i]["rpid"]) > replyText(all[j]["rpid"])
			})
		}
		total := len(all)
		start := (pn - 1) * ps
		if start > total {
			start = total
		}
		end := start + ps
		if end > total {
			end = total
		}
		page := append([]replyJSON(nil), all[start:end]...)
		code := fixture.code
		hook := fixture.after
		fixture.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if hook != nil {
			hook(q)
		}
		payload := map[string]any{"code": code, "message": "0", "data": map[string]any{
			"replies": page,
			"page":    map[string]any{"count": total, "num": pn, "size": ps},
		}}
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

func (s *commentServer) addSub(root string, replies ...replyJSON) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subReplies[root] = append(s.subReplies[root], replies...)
}

func (s *commentServer) snapshot() ([]url.Values, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.queries...), append([]string(nil), s.cookies...)
}

func (s *commentServer) queriesSince(index int) []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index > len(s.queries) {
		index = len(s.queries)
	}
	return append([]url.Values(nil), s.queries[index:]...)
}

func pageKey(path string, q url.Values) string {
	if path == "/x/v2/reply/reply" {
		return "sub:" + q.Get("root") + ":" + q.Get("pn")
	}
	return "top:" + q.Get("oid") + ":" + q.Get("pn")
}

func queryInt(value string, fallback int) int {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func replyInt(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		n, _ := typed.Int64()
		return n
	default:
		return 0
	}
}

func replyText(value any) string {
	return fmt.Sprint(value)
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

func timedReply(rpid, mid int, uname, text string, ctime int64, rcount int) replyJSON {
	r := reply(rpid, mid, uname, text)
	r["ctime"] = ctime
	r["rcount"] = rcount
	return r
}

// replyWithPics 给评论附上图片地址；正文可为空，模拟纯图片评论。
func replyWithPics(rpid, mid int, uname, text string, pics ...string) replyJSON {
	r := reply(rpid, mid, uname, text)
	list := make([]map[string]string, 0, len(pics))
	for _, pic := range pics {
		list = append(list, map[string]string{"img_src": pic})
	}
	r["content"].(map[string]any)["pics"] = list
	return r
}

func subReply(rpid, mid int, uname, text string, ctime int64, root int) replyJSON {
	r := reply(rpid, mid, uname, text)
	r["ctime"] = ctime
	r["root"] = root
	r["parent"] = root
	return r
}

func newCollector(t *testing.T, root string, videos []string, interval time.Duration) *Collector {
	t.Helper()
	return newCollectorWith(t, root, videos, interval, zap.NewNop(), nil)
}

func newCollectorWith(t *testing.T, root string, videos []string, interval time.Duration, log *zap.Logger, edit func(*Options)) *Collector {
	t.Helper()
	o := DefaultOptions()
	o.Videos = videos
	o.Interval = interval
	o.SelfID = "999"
	o.Cookie = testCookie
	o.APIRoot = root
	o.HTTPTimeout = time.Second
	if edit != nil {
		edit(&o)
	}
	c, err := New(o, log)
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
	return startCollectorWithBuffer(t, c, 32)
}

func startCollectorWithBuffer(t *testing.T, c *Collector, buffer int) (context.CancelFunc, chan event.Event, chan error, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan event.Event, buffer)
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

func collectEvents(t *testing.T, out <-chan event.Event, n int) []event.Event {
	t.Helper()
	events := make([]event.Event, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, awaitEvent(t, out))
	}
	return events
}

func assertUniqueEvents(t *testing.T, events []event.Event, want ...string) {
	t.Helper()
	counts := make(map[string]int, len(events))
	for _, ev := range events {
		counts[ev.ID]++
	}
	for id, count := range counts {
		if count != 1 {
			t.Errorf("事件 %s 出现 %d 次，期望恰好一次", id, count)
		}
	}
	for _, id := range want {
		if counts[id] != 1 {
			t.Errorf("事件 %s 入库次数 = %d，期望 1", id, counts[id])
		}
	}
}

func assertNoEvents(t *testing.T, out <-chan event.Event, wait time.Duration) {
	t.Helper()
	select {
	case ev := <-out:
		t.Fatalf("不应再产生事件：%+v", ev)
	case <-time.After(wait):
	}
}

func waitForLog(t *testing.T, logs *observer.ObservedLogs, message string) observer.LoggedEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entries := logs.FilterMessage(message).All(); len(entries) > 0 {
			return entries[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("未等到日志 %q", message)
	return observer.LoggedEntry{}
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
	if q.Get("type") != "1" || q.Get("oid") != "123" || q.Get("pn") != "1" || q.Get("ps") != "20" || q.Get("sort") != "0" {
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

// TestCollectorCapturesCommentPictures 带图评论把图片地址写进 Meta；纯图片评论用占位正文。
func TestCollectorCapturesCommentPictures(t *testing.T) {
	const (
		img1 = "https://i0.hdslb.com/bfs/new_dyn/a.jpg"
		img2 = "https://i1.hdslb.com/bfs/new_dyn/b.webp"
	)
	fixture, srv := newCommentServer(t)
	fixture.add("123",
		replyWithPics(3001, 42, "带图甲", "看这个", img1, img2),
		replyWithPics(3002, 43, "纯图乙", "   ", img1),
	)
	c := newCollector(t, srv.URL, []string{"123"}, 20*time.Millisecond)
	cancel, out, _, _ := startCollector(t, c)
	defer cancel()

	got := map[string]event.Event{}
	for i := 0; i < 2; i++ {
		ev := awaitEvent(t, out)
		got[ev.ID] = ev
	}
	withText, ok := got["comment:123:3001"]
	if !ok {
		t.Fatalf("缺少带图评论事件：%+v", got)
	}
	if withText.Text != "看这个" {
		t.Errorf("正文 = %q，期望保留原文", withText.Text)
	}
	if pics := Pics(withText.Meta); len(pics) != 2 || pics[0] != img1 || pics[1] != img2 {
		t.Errorf("Meta 图片 = %v，期望 [%s %s]", pics, img1, img2)
	}
	pure, ok := got["comment:123:3002"]
	if !ok {
		t.Fatalf("缺少纯图片评论事件：%+v", got)
	}
	if pure.Text != pictureOnlyText {
		t.Errorf("纯图片评论正文 = %q，期望 %q", pure.Text, pictureOnlyText)
	}
	if pics := Pics(pure.Meta); len(pics) != 1 || pics[0] != img1 {
		t.Errorf("纯图片评论 Meta 图片 = %v", pics)
	}
}

// TestPictureURLsFiltersAndCaps 只收 https、无凭据、长度受限的地址，去重并封顶 4 张。
func TestPictureURLsFiltersAndCaps(t *testing.T) {
	valid := func(n string) apiPicture { return apiPicture{URL: "https://i0.hdslb.com/" + n} }
	long := apiPicture{URL: "https://i0.hdslb.com/" + strings.Repeat("x", maxPictureURL)}
	pics := []apiPicture{
		{URL: " https://i0.hdslb.com/a.jpg "},
		{URL: "http://i0.hdslb.com/b.jpg"},
		{URL: "https://user:pass@i0.hdslb.com/c.jpg"},
		{URL: ""},
		{URL: "https://i0.hdslb.com/d.\njpg"},
		long,
		{URL: "https://i0.hdslb.com/a.jpg"},
		valid("e1.jpg"), valid("e2.jpg"), valid("e3.jpg"), valid("e4.jpg"), valid("e5.jpg"),
	}
	got := pictureURLs(pics)
	want := []string{
		"https://i0.hdslb.com/a.jpg",
		"https://i0.hdslb.com/e1.jpg",
		"https://i0.hdslb.com/e2.jpg",
		"https://i0.hdslb.com/e3.jpg",
	}
	if len(got) != len(want) {
		t.Fatalf("图片数 = %d，期望 %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 张 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

// TestPicsReadsMeta Meta 缺失、坏 JSON、空串与超量都要安全降级。
func TestPicsReadsMeta(t *testing.T) {
	if got := Pics(nil); got != nil {
		t.Errorf("空 Meta 应返回 nil：%v", got)
	}
	if got := Pics(json.RawMessage(`not-json`)); got != nil {
		t.Errorf("坏 JSON 应返回 nil：%v", got)
	}
	meta := json.RawMessage(`{"pics":[" https://i0.hdslb.com/a.jpg ","","https://i0.hdslb.com/b.jpg","https://i0.hdslb.com/c.jpg","https://i0.hdslb.com/d.jpg","https://i0.hdslb.com/e.jpg"]}`)
	got := Pics(meta)
	if len(got) != 4 || got[0] != "https://i0.hdslb.com/a.jpg" || got[1] != "https://i0.hdslb.com/b.jpg" {
		t.Errorf("Meta 图片读取不符：%v", got)
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
	videos       []bili.SpaceVideo
	dynamics     []bili.SpaceDynamic
	videoErr     error
	dynamicErr   error
	videoCalls   int
	dynamicCalls int
}

func (f *fakeSpaceSource) SpaceVideos(context.Context, string, int) ([]bili.SpaceVideo, error) {
	f.videoCalls++
	return f.videos, f.videoErr
}

func (f *fakeSpaceSource) SpaceDynamics(context.Context, string, int) ([]bili.SpaceDynamic, error) {
	f.dynamicCalls++
	return f.dynamics, f.dynamicErr
}

// TestDynamicDiscovererOnlyDynamics 只拉动态列表，按平台评论区类型/OID 映射。
func TestDynamicDiscovererOnlyDynamics(t *testing.T) {
	source := &fakeSpaceSource{
		videos: []bili.SpaceVideo{{AID: "11"}},
		dynamics: []bili.SpaceDynamic{
			{ID: "22", CommentID: "415114992", CommentType: 11},
			{ID: "022", CommentID: "415114992", CommentType: 11},
			{ID: "abc"},
			{ID: "33"},
			{ID: "44", CommentType: 1},
		},
	}
	got, err := DynamicDiscoverer{Client: source, MID: "42", Limit: 5}.Discover(context.Background())
	if err != nil {
		t.Fatalf("动态发现失败：%v", err)
	}
	if source.videoCalls != 0 {
		t.Errorf("动态发现不应调用视频接口：%d 次", source.videoCalls)
	}
	want := []Target{{OID: "415114992", Type: TypeDraw}, {OID: "33", Type: TypeDynamic}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("动态 target 映射不符：%+v", got)
	}
	if _, err := (DynamicDiscoverer{}).Discover(context.Background()); err == nil {
		t.Fatal("缺少列表来源应失败")
	}
}

// TestMergeTargetsKeepsDraw 图文动态（type=11）不能被 merged 去重逻辑丢掉。
func TestMergeTargetsKeepsDraw(t *testing.T) {
	got := mergeTargets(nil, []Target{{OID: "415114992", Type: TypeDraw}})
	if len(got) != 1 || got[0].Type != TypeDraw || got[0].OID != "415114992" {
		t.Fatalf("图文动态 target 被丢弃：%+v", got)
	}
}

// TestCollectorDynamicPollingType17 动态只轮询 type=17 并带动态评论类型。
func TestCollectorDynamicPollingType17(t *testing.T) {
	fixture, srv := newCommentServer(t)
	top := timedReply(601, 77, "观众", "评论动态", 1700000000, 0)
	top["root"], top["parent"] = 0, 0
	fixture.add("520", top)

	c := newCollectorWith(t, srv.URL, nil, 20*time.Millisecond, zap.NewNop(), func(o *Options) {
		o.AutoDiscover = true
		o.DiscoverInterval = time.Hour
		o.Discoverer = DynamicDiscoverer{
			Client: &fakeSpaceSource{dynamics: []bili.SpaceDynamic{{ID: "520"}}},
			MID:    "8",
			Limit:  5,
		}
	})
	_, out, _, _ := startCollector(t, c)
	ev := awaitEvent(t, out)
	if ev.ID != "comment:520:601" || ev.Session != "520" || ev.FromID != "77" {
		t.Fatalf("动态评论事件不符：%+v", ev)
	}
	var meta map[string]any
	if err := json.Unmarshal(ev.Meta, &meta); err != nil || meta["comment_type"] != float64(TypeDynamic) {
		t.Errorf("动态评论 Meta 不符：%s (%v)", ev.Meta, err)
	}
	queries, _ := fixture.snapshot()
	if len(queries) == 0 || queries[0].Get("type") != "17" || queries[0].Get("oid") != "520" {
		t.Errorf("动态评论查询参数不符：%v", queries)
	}
}

// TestCollectorDynamicPollingType11 图文动态（draw）必须按平台评论类型 11 拉取。
func TestCollectorDynamicPollingType11(t *testing.T) {
	fixture, srv := newCommentServer(t)
	top := timedReply(601, 77, "观众", "评论图文动态", 1700000000, 0)
	top["root"], top["parent"] = 0, 0
	fixture.add("415114992", top)

	c := newCollectorWith(t, srv.URL, nil, 20*time.Millisecond, zap.NewNop(), func(o *Options) {
		o.AutoDiscover = true
		o.DiscoverInterval = time.Hour
		o.Discoverer = DynamicDiscoverer{
			Client: &fakeSpaceSource{dynamics: []bili.SpaceDynamic{{ID: "415114992", CommentID: "415114992", CommentType: TypeDraw}}},
			MID:    "8",
			Limit:  5,
		}
	})
	_, out, _, _ := startCollector(t, c)
	ev := awaitEvent(t, out)
	if ev.ID != "comment:415114992:601" || ev.Session != "415114992" {
		t.Fatalf("图文动态评论事件不符：%+v", ev)
	}
	var meta map[string]any
	if err := json.Unmarshal(ev.Meta, &meta); err != nil || meta["comment_type"] != float64(TypeDraw) {
		t.Errorf("图文动态 Meta 不符：%s (%v)", ev.Meta, err)
	}
	queries, _ := fixture.snapshot()
	if len(queries) == 0 || queries[0].Get("type") != "11" || queries[0].Get("oid") != "415114992" {
		t.Errorf("图文动态评论查询参数不符：%v", queries)
	}
}

// TestCollectorRecentSinceSkipsOldComments 首轮补采窗口只放行近期评论。
func TestCollectorRecentSinceSkipsOldComments(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.add("700",
		timedReply(701, 41, "旧评论", "两天前", 1700000000, 0),
		timedReply(702, 42, "新评论", "刚刚", 1700003600, 0),
	)
	c := newCollectorWith(t, srv.URL, []string{"700"}, 20*time.Millisecond, zap.NewNop(), func(o *Options) {
		o.RecentSince = time.Unix(1700001800, 0)
	})
	_, out, _, _ := startCollector(t, c)
	if ev := awaitEvent(t, out); ev.ID != "comment:700:702" {
		t.Fatalf("窗口内评论未产出或旧评论混入：%+v", ev)
	}
	assertNoEvents(t, out, 80*time.Millisecond)
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

// TestT20PaginationWatermarkInsertionExactlyOnce 覆盖 45 条跨页评论、翻页时插入、
// 跨页重复与水位早停：稳定 rpid 最终恰好一次入库。
func TestT20PaginationWatermarkInsertionExactlyOnce(t *testing.T) {
	fixture, srv := newCommentServer(t)
	for i := 1; i <= 45; i++ {
		fixture.add("123", timedReply(i, 40+i%5, fmt.Sprintf("观众%d", i), fmt.Sprintf("评论%d", i), int64(1700000000+i), 0))
	}
	var inserted sync.Once
	fixture.after = func(q url.Values) {
		if q.Get("root") != "" || q.Get("pn") != "1" {
			return
		}
		inserted.Do(func() {
			fixture.add("123", timedReply(46, 77, "插队者", "翻页时插入", 1700000046, 0))
		})
	}
	c := newCollectorWith(t, srv.URL, []string{"123"}, 20*time.Millisecond, zap.NewNop(), nil)
	_, out, _, _ := startCollectorWithBuffer(t, c, 256)

	events := collectEvents(t, out, 46)
	want := make([]string, 0, 46)
	for i := 1; i <= 46; i++ {
		want = append(want, fmt.Sprintf("comment:123:%d", i))
	}
	assertUniqueEvents(t, events, want...)
	assertNoEvents(t, out, 120*time.Millisecond)

	// 水位已追平：之后每轮只应取第一页，不再深翻。
	fixture.mu.Lock()
	before := len(fixture.queries)
	fixture.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	for _, q := range fixture.queriesSince(before) {
		if q.Get("root") != "" || q.Get("pn") != "1" {
			t.Errorf("追平后仍继续深翻：%v", q)
		}
	}
}

// TestT20SecondPageFailureRecoversExactlyOnce 第二页一次失败后，下一轮从页首
// 重新追平；已入库的 rpid 不重复，缺口只补一次。
func TestT20SecondPageFailureRecoversExactlyOnce(t *testing.T) {
	fixture, srv := newCommentServer(t)
	for i := 1; i <= 45; i++ {
		fixture.add("123", timedReply(i, 40+i%5, fmt.Sprintf("观众%d", i), fmt.Sprintf("评论%d", i), int64(1700000000+i), 0))
	}
	fixture.fail["top:123:2"] = http.StatusInternalServerError
	c := newCollectorWith(t, srv.URL, []string{"123"}, 20*time.Millisecond, zap.NewNop(), nil)
	_, out, errc, _ := startCollectorWithBuffer(t, c, 256)

	events := collectEvents(t, out, 45)
	want := make([]string, 0, 45)
	for i := 1; i <= 45; i++ {
		want = append(want, fmt.Sprintf("comment:123:%d", i))
	}
	assertUniqueEvents(t, events, want...)
	select {
	case err := <-errc:
		if err == nil {
			t.Error("第二页失败应上报非 nil 错误")
		}
	case <-time.After(2 * time.Second):
		t.Error("第二页失败未上报到错误通道")
	}
	assertNoEvents(t, out, 120*time.Millisecond)
}

// TestT20PageLimitTruncationLogged 超过页数上限时只保证前缀并记录截断。
func TestT20PageLimitTruncationLogged(t *testing.T) {
	fixture, srv := newCommentServer(t)
	for i := 1; i <= 45; i++ {
		fixture.add("123", timedReply(i, 40+i%5, fmt.Sprintf("观众%d", i), fmt.Sprintf("评论%d", i), int64(1700000000+i), 0))
	}
	core, logs := observer.New(zap.WarnLevel)
	c := newCollectorWith(t, srv.URL, []string{"123"}, 20*time.Millisecond, zap.New(core), func(o *Options) {
		o.MaxPages = 1
	})
	_, out, _, _ := startCollectorWithBuffer(t, c, 128)

	events := collectEvents(t, out, 20)
	want := make([]string, 0, 20)
	for i := 45; i >= 26; i-- {
		want = append(want, fmt.Sprintf("comment:123:%d", i))
	}
	assertUniqueEvents(t, events, want...)
	assertNoEvents(t, out, 100*time.Millisecond)

	entry := waitForLog(t, logs, "顶层评论超过单轮页数上限，已截断")
	fields := entry.ContextMap()
	if fields["action"] != "comment_page_limit" || fields["oid"] != "123" || fmt.Sprint(fields["page_count"]) != "45" {
		t.Errorf("截断日志字段不符：%v", fields)
	}
	queries, _ := fixture.snapshot()
	for _, q := range queries {
		if q.Get("pn") != "1" {
			t.Errorf("页数上限为 1 却翻到了 %s", q.Get("pn"))
		}
	}
}

// TestT20OldThreadNewSubReplyDetected 顶层评论滚出首屏后仍跟踪楼中楼，
// 老线程新增回复能被下一轮发现。
func TestT20OldThreadNewSubReplyDetected(t *testing.T) {
	fixture, srv := newCommentServer(t)
	for i := 0; i < 40; i++ {
		fixture.add("123", timedReply(1000+i, 40+i%5, fmt.Sprintf("新观众%d", i), fmt.Sprintf("新评论%d", i), int64(1700001000+i), 0))
	}
	fixture.add("123", timedReply(900, 41, "楼主", "老线程", 1700000000, 45))
	for i := 1; i <= 45; i++ {
		fixture.addSub("900", subReply(900+i, 42+i%3, "楼内观众", fmt.Sprintf("楼中楼%d", i), int64(1700000000+i), 900))
	}
	c := newCollectorWith(t, srv.URL, []string{"123"}, 20*time.Millisecond, zap.NewNop(), func(o *Options) {
		o.MaxPages = 3
	})
	_, out, _, _ := startCollectorWithBuffer(t, c, 256)

	events := collectEvents(t, out, 86)
	want := make([]string, 0, 86)
	want = append(want, "comment:123:900")
	for i := 0; i < 40; i++ {
		want = append(want, fmt.Sprintf("comment:123:%d", 1000+i))
	}
	for i := 1; i <= 45; i++ {
		want = append(want, fmt.Sprintf("comment:123:%d", 900+i))
	}
	assertUniqueEvents(t, events, want...)
	assertNoEvents(t, out, 100*time.Millisecond)

	fixture.addSub("900", subReply(946, 43, "新楼内", "老线程的新回复", 1700001000, 900))
	ev := awaitEvent(t, out)
	if ev.ID != "comment:123:946" {
		t.Fatalf("老线程新回复事件 ID = %s", ev.ID)
	}
	var meta map[string]any
	if err := json.Unmarshal(ev.Meta, &meta); err != nil || meta["root"] != "900" {
		t.Errorf("楼中楼 Meta 不符：%s (%v)", ev.Meta, err)
	}
	assertNoEvents(t, out, 120*time.Millisecond)
}

// TestT20SubReplyGrowthAcrossPageBoundary 楼中楼一次新增跨越页边界时，
// 不能只取末页：从旧总数所在页向尾部追，新增回复全部恰好一次。
func TestT20SubReplyGrowthAcrossPageBoundary(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.add("123", timedReply(900, 41, "楼主", "根评论", 1700000000, 45))
	for i := 1; i <= 45; i++ {
		fixture.addSub("900", subReply(900+i, 42, "楼内观众", fmt.Sprintf("楼中楼%d", i), int64(1700000000+i), 900))
	}
	c := newCollectorWith(t, srv.URL, []string{"123"}, 20*time.Millisecond, zap.NewNop(), nil)
	_, out, _, _ := startCollectorWithBuffer(t, c, 256)

	initial := collectEvents(t, out, 46)
	want := []string{"comment:123:900"}
	for i := 1; i <= 45; i++ {
		want = append(want, fmt.Sprintf("comment:123:%d", 900+i))
	}
	assertUniqueEvents(t, initial, want...)
	assertNoEvents(t, out, 100*time.Millisecond)

	// 45→61：旧末页是第 3 页，新增区间从第 3 页跨到第 4 页。
	for i := 46; i <= 61; i++ {
		fixture.addSub("900", subReply(900+i, 42, "楼内观众", fmt.Sprintf("楼中楼%d", i), int64(1700000000+i), 900))
	}
	grown := collectEvents(t, out, 16)
	grownWant := make([]string, 0, 16)
	for i := 46; i <= 61; i++ {
		grownWant = append(grownWant, fmt.Sprintf("comment:123:%d", 900+i))
	}
	assertUniqueEvents(t, grown, grownWant...)
	assertNoEvents(t, out, 120*time.Millisecond)
}

// TestT20SubReplyPageLimitBackfills 楼中楼超过单轮页数上限时记录落后，
// 下一轮继续回填中间页，最终全部恰好一次。
func TestT20SubReplyPageLimitBackfills(t *testing.T) {
	fixture, srv := newCommentServer(t)
	fixture.add("123", timedReply(900, 41, "楼主", "根评论", 1700000000, 45))
	for i := 1; i <= 45; i++ {
		fixture.addSub("900", subReply(900+i, 42, "楼内观众", fmt.Sprintf("楼中楼%d", i), int64(1700000000+i), 900))
	}
	core, logs := observer.New(zap.WarnLevel)
	c := newCollectorWith(t, srv.URL, []string{"123"}, 20*time.Millisecond, zap.New(core), func(o *Options) {
		o.MaxReplyPages = 2
	})
	_, out, _, _ := startCollectorWithBuffer(t, c, 256)

	events := collectEvents(t, out, 46)
	want := []string{"comment:123:900"}
	for i := 1; i <= 45; i++ {
		want = append(want, fmt.Sprintf("comment:123:%d", 900+i))
	}
	assertUniqueEvents(t, events, want...)
	entry := waitForLog(t, logs, "楼中楼尚未追平，按页数上限分批回填")
	fields := entry.ContextMap()
	if fields["action"] != "comment_reply_limit" || fields["root"] != "900" ||
		fmt.Sprint(fields["total"]) != "45" || fmt.Sprint(fields["last_page"]) != "3" {
		t.Errorf("楼中楼落后日志字段不符：%v", fields)
	}
	assertNoEvents(t, out, 120*time.Millisecond)
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
		{"顶层页数过大", func(o *Options) { o.MaxPages = maxPagesUpper + 1 }},
		{"根跟踪过大", func(o *Options) { o.MaxRoots = maxRootsUpper + 1 }},
		{"楼中楼页数不足", func(o *Options) { o.MaxReplyPages = 1 }},
		{"楼中楼页数过大", func(o *Options) { o.MaxReplyPages = maxReplyPagesUpper + 1 }},
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
