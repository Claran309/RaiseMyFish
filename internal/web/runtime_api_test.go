package web

// 本文件测运行时管理 API、日志接口与新增页面。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/logview"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/watchdog"
	"go.uber.org/zap"
)

// fakeControl 记录面板下发的运行时开关调用。
type fakeControl struct {
	status   RuntimeStatus
	watchdog WatchdogStatus
	err      error

	liveCalls          []bool
	watchCalls         []bool
	watchRooms         []string
	commentCalls       []bool
	commentVideos      [][]string
	dmCalls            []bool
	dmAllowed          [][]string
	proactiveCalls     []bool
	proactiveIntervals []int
	giftCalls          []bool
	frozenCalls        []bool
	hotReloadCalls     []bool
	memeLearnCalls     []bool
	watchdogCalls      int
}

func (f *fakeControl) Status() RuntimeStatus { return f.status }

func (f *fakeControl) SetLiveCollect(enabled bool) error {
	f.liveCalls = append(f.liveCalls, enabled)
	return f.err
}

func (f *fakeControl) SetWatch(enabled bool, roomID string) error {
	f.watchCalls = append(f.watchCalls, enabled)
	f.watchRooms = append(f.watchRooms, roomID)
	return f.err
}

func (f *fakeControl) SetComment(enabled bool, videos []string) error {
	f.commentCalls = append(f.commentCalls, enabled)
	f.commentVideos = append(f.commentVideos, videos)
	return f.err
}

func (f *fakeControl) SetDM(enabled bool, allowed []string) error {
	f.dmCalls = append(f.dmCalls, enabled)
	f.dmAllowed = append(f.dmAllowed, allowed)
	return f.err
}

func (f *fakeControl) SetProactive(enabled bool, intervalSeconds int) error {
	f.proactiveCalls = append(f.proactiveCalls, enabled)
	f.proactiveIntervals = append(f.proactiveIntervals, intervalSeconds)
	return f.err
}

func (f *fakeControl) SetGift(enabled bool) error {
	f.giftCalls = append(f.giftCalls, enabled)
	return f.err
}

func (f *fakeControl) SetMemoryFrozen(frozen bool) error {
	f.frozenCalls = append(f.frozenCalls, frozen)
	return f.err
}

func (f *fakeControl) SetHotReload(enabled bool) error {
	f.hotReloadCalls = append(f.hotReloadCalls, enabled)
	return f.err
}

func (f *fakeControl) SetMemeLearn(enabled bool) error {
	f.memeLearnCalls = append(f.memeLearnCalls, enabled)
	return f.err
}

func (f *fakeControl) Watchdog() WatchdogStatus {
	f.watchdogCalls++
	return f.watchdog
}

// fakeLogs 记录查询条件，可推送一条假日志。
type fakeLogs struct {
	entries   []logview.Entry
	queries   []logview.Query
	tail      *logview.Entry
	recentErr error
}

func (f *fakeLogs) Recent(q logview.Query) ([]logview.Entry, error) {
	f.queries = append(f.queries, q)
	return f.entries, f.recentErr
}

func (f *fakeLogs) Tail(_ context.Context, fn func(logview.Entry) error) error {
	if f.tail != nil {
		if err := fn(*f.tail); err != nil {
			return err
		}
	}
	return nil
}

func newTestServerWithRuntime(t *testing.T, repo store.WebRepo, control RuntimeController, logs LogSource) *Server {
	t.Helper()
	s, err := New(Options{Addr: "127.0.0.1:8090", Repo: repo, Log: zap.NewNop(), Control: control, Logs: logs})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func postJSON(t *testing.T, srv *Server, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	// 走同源写请求 helper：O04 之后必须带 Origin + Host + JSON Content-Type。
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, target, body))
	return rec
}

func TestLiveAPI(t *testing.T) {
	control := &fakeControl{status: RuntimeStatus{
		Live:        CollectorState{Available: true, Running: true},
		Watch:       WatchState{Available: true, RoomID: "30308220"},
		GeneratedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	}}
	srv := newTestServerWithRuntime(t, &fakeRepo{}, control, nil)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/live", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"room_id":"30308220"`) {
		t.Fatalf("直播状态不符：%d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, srv, "/api/live", `{"action":"collect","enabled":true}`)
	if rec.Code != http.StatusBadRequest || len(control.liveCalls) != 0 {
		t.Fatalf("缺 confirm 应拒绝：%d %v", rec.Code, control.liveCalls)
	}

	rec = postJSON(t, srv, "/api/live?confirm=yes", `{"action":"watch","enabled":true,"room_id":"https://live.bilibili.com/123"}`)
	if rec.Code != http.StatusOK || len(control.watchCalls) != 1 || control.watchRooms[0] != "https://live.bilibili.com/123" {
		t.Fatalf("开看直播失败：%d %+v", rec.Code, control)
	}

	rec = postJSON(t, srv, "/api/live?confirm=yes", `{"action":"bogus","enabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知 action 应 400，实际 %d", rec.Code)
	}

	control.err = errors.New("room 无效")
	rec = postJSON(t, srv, "/api/live?confirm=yes", `{"action":"collect","enabled":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "room 无效") {
		t.Fatalf("控制面错误应回 400：%d %s", rec.Code, rec.Body.String())
	}
}

func TestMessageAPI(t *testing.T) {
	control := &fakeControl{status: RuntimeStatus{
		Comment: CommentState{CollectorState: CollectorState{Available: true}, Videos: []string{"12"}, AutoDiscover: true},
		DM:      DMState{AllowedMIDs: []string{"42"}, AllFollowers: true, WhitelistAvailable: true},
	}}
	srv := newTestServerWithRuntime(t, &fakeRepo{}, control, nil)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/message", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"videos":["12"]`) || !strings.Contains(body, `"allowed_mids":["42"]`) ||
		!strings.Contains(body, `"auto_discover":true`) || !strings.Contains(body, `"all_followers":true`) {
		t.Fatalf("消息状态不符：%d %s", rec.Code, body)
	}

	rec = postJSON(t, srv, "/api/message", `{"action":"comment","enabled":true,"videos":["1"]}`)
	if rec.Code != http.StatusBadRequest || len(control.commentCalls) != 0 {
		t.Fatalf("缺 confirm 应拒绝：%d %v", rec.Code, control.commentCalls)
	}

	rec = postJSON(t, srv, "/api/message?confirm=yes", `{"action":"comment","enabled":true,"videos":["1","2"]}`)
	if rec.Code != http.StatusOK || len(control.commentVideos) != 1 || len(control.commentVideos[0]) != 2 {
		t.Fatalf("评论监听更新失败：%d %+v", rec.Code, control.commentVideos)
	}

	rec = postJSON(t, srv, "/api/message?confirm=yes", `{"action":"dm","enabled":true,"allowed_mids":["42","77"]}`)
	if rec.Code != http.StatusOK || len(control.dmAllowed) != 1 || len(control.dmAllowed[0]) != 2 {
		t.Fatalf("私信白名单更新失败：%d %+v", rec.Code, control.dmAllowed)
	}

	rec = postJSON(t, srv, "/api/message?confirm=yes", `{"action":"nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知 action 应 400，实际 %d", rec.Code)
	}
}

func TestConfigAPI(t *testing.T) {
	control := &fakeControl{status: RuntimeStatus{
		Proactive: ProactiveState{FeatureState: FeatureState{Enabled: true}, IntervalSeconds: 20},
		Gift:      GiftState{Enabled: true},
	}}
	srv := newTestServerWithRuntime(t, &fakeRepo{}, control, nil)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"interval_seconds":20`) {
		t.Fatalf("配置快照不符：%d %s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, srv, "/api/config", `{"field":"gift_enabled","value":false}`)
	if rec.Code != http.StatusBadRequest || len(control.giftCalls) != 0 {
		t.Fatalf("缺 confirm 应拒绝：%d %v", rec.Code, control.giftCalls)
	}

	cases := []struct {
		body string
		want int
	}{
		{`{"field":"gift_enabled","value":false}`, http.StatusOK},
		{`{"field":"proactive_interval_seconds","value":30}`, http.StatusOK},
		{`{"field":"memory_frozen","value":true}`, http.StatusOK},
		{`{"field":"hot_reload_enabled","value":true}`, http.StatusOK},
		{`{"field":"meme_learn_enabled","value":false}`, http.StatusOK},
		{`{"field":"proactive_enabled","value":true}`, http.StatusOK},
		{`{"field":"unknown","value":1}`, http.StatusBadRequest},
		{`{"field":"gift_enabled","value":"yes"}`, http.StatusBadRequest},
		{`{"field":"proactive_interval_seconds","value":2}`, http.StatusBadRequest},
		{`{"field":"memory_frozen","value":true,"extra":1}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := postJSON(t, srv, "/api/config?confirm=yes", tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s = %d，期望 %d（%s）", tc.body, rec.Code, tc.want, rec.Body.String())
		}
	}
	if len(control.giftCalls) != 1 || control.giftCalls[0] {
		t.Errorf("礼物开关调用不符：%v", control.giftCalls)
	}
	if len(control.proactiveIntervals) != 2 || control.proactiveIntervals[0] != 30 || control.proactiveCalls[0] != true {
		t.Errorf("主动弹幕间隔调用不符：%v %v", control.proactiveCalls, control.proactiveIntervals)
	}
	if len(control.frozenCalls) != 1 || !control.frozenCalls[0] {
		t.Errorf("记忆冻结调用不符：%v", control.frozenCalls)
	}
	if len(control.hotReloadCalls) != 1 || len(control.memeLearnCalls) != 1 {
		t.Errorf("热重载/梗白调用不符：%v %v", control.hotReloadCalls, control.memeLearnCalls)
	}
}

func TestWatchdogAPI(t *testing.T) {
	control := &fakeControl{watchdog: WatchdogStatus{
		Enabled: true, StaleSeconds: 120, RestartCommandConfigured: true,
		Restarts: []watchdog.RestartRecord{{At: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC), Reason: "stale", PID: 42}},
	}}
	srv := newTestServerWithRuntime(t, &fakeRepo{}, control, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/watchdog", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"reason":"stale"`) || !strings.Contains(body, `"restart_command_configured":true`) {
		t.Fatalf("看门狗状态不符：%d %s", rec.Code, body)
	}
	if control.watchdogCalls != 1 {
		t.Errorf("应调用一次 Watchdog：%d", control.watchdogCalls)
	}
}

func TestChannelRecentAPI(t *testing.T) {
	repo := &fakeRepo{channelEvents: map[string][]store.WebEvent{
		"comment": {{ID: "comment:1", Channel: "comment", Dir: "in", Text: "评论", Status: "done"}},
		"dm":      {{ID: "dm:1", Channel: "dm", Dir: "in", Text: "私信", Status: "done"}},
	}}
	srv := newTestServer(t, repo)
	for path, want := range map[string]string{
		"/api/comment/recent": `"channel":"comment"`,
		"/api/dm/recent":      `"channel":"dm"`,
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s 响应不符：%d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/comment/recent?dir=side", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 dir 应 400，实际 %d", rec.Code)
	}
}

func TestEventDetailAPI(t *testing.T) {
	ok := true
	repo := &fakeRepo{details: map[string]store.WebEventDetail{
		"live:1:2": {
			WebEvent: store.WebEvent{ID: "live:1:2", Channel: "live", Dir: "in", Text: "在吗", Status: "done"},
			FromID:   "778", ReplyTo: "@鱼", Meta: `{"tool":"gift"}`,
			CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
			Reply: &store.WebReply{
				ActorID: "778", Text: "在的", State: "sent", AttemptCount: 1,
				CreatedAt: time.Date(2026, 11, 5, 12, 0, 1, 0, time.UTC),
				Attempts: []store.WebAttempt{{
					Number: 1, State: "sent", OK: &ok, Verified: true, MsgID: "m1",
				}},
			},
		},
	}}
	srv := newTestServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events/live:1:2", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	for _, want := range []string{`"from_id":"778"`, `"reply_to":"@鱼"`, `"meta"`, `"reply"`, `"attempts"`, `"msg_id":"m1"`, `"verified":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("详情缺少 %q：%s", want, body)
		}
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在应 404，实际 %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("缺 id 应 400，实际 %d", rec.Code)
	}
}

func TestGiftQuotaAPI(t *testing.T) {
	control := &fakeControl{status: RuntimeStatus{Gift: GiftState{
		Enabled: true, Real: true, DailyLimit: 5, MaxNum: 2, Used: 3, Day: "2026-11-05",
	}}}
	srv := newTestServerWithRuntime(t, &fakeRepo{}, control, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/gift/quota", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"used":3`) || !strings.Contains(body, `"daily_limit":5`) {
		t.Fatalf("额度响应不符：%d %s", rec.Code, body)
	}
}

func TestLogsAPI(t *testing.T) {
	logs := &fakeLogs{
		entries: []logview.Entry{{Time: "2026-11-05T12:00:00Z", Level: "info", Msg: "ok", Action: "proactive"}},
		tail:    &logview.Entry{Time: "2026-11-05T12:00:01Z", Level: "warn", Msg: "tail"},
	}
	srv := newTestServerWithRuntime(t, &fakeRepo{}, nil, logs)

	for path, wantAction := range map[string]string{
		"/api/gift/log":      "tool_side_effect",
		"/api/proactive/log": "proactive",
		"/api/like/log":      "heartbeat_like",
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"msg":"ok"`) {
			t.Errorf("%s 响应不符：%d %s", path, rec.Code, rec.Body.String())
		}
		found := false
		for _, q := range logs.queries {
			for _, action := range q.Actions {
				if action == wantAction {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s 未按 %s 过滤", path, wantAction)
		}
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "event: ready") || !strings.Contains(body, `"msg":"tail"`) {
		t.Fatalf("SSE 输出不符：%d %s", rec.Code, body)
	}
}

func TestRuntimeUnavailable(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	for _, path := range []string{"/api/live", "/api/message", "/api/config", "/api/watchdog", "/api/gift/quota"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s 应 503，实际 %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/api/logs/stream", "/api/gift/log", "/api/proactive/log", "/api/like/log"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s 应 503，实际 %d", path, rec.Code)
		}
	}
}

// TestNewPagesRender 新增页面都可访问，且都带全局导航。
func TestNewPagesRender(t *testing.T) {
	srv := newTestServerWithRuntime(t, &fakeRepo{}, &fakeControl{}, &fakeLogs{})
	want := map[string][]string{
		"/live":    {"直播管理", "/api/live", "room_id", "开始看直播", "直播采集", "未装配"},
		"/message": {"消息管理", "/api/comment/recent", "/api/dm/recent", "白名单", "视频 oid", "自动发现", "所有发信人", "未装配"},
		"/config":  {"配置编辑", "/api/config", "/api/watchdog", "记忆冻结", "梗白学习", "未装配"},
		"/logs":    {"实时日志", "/api/logs/stream", "EventSource", "暂停"},
	}
	for path, keys := range want {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s 状态码 = %d", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, key := range keys {
			if !strings.Contains(body, key) {
				t.Errorf("%s 缺少 %q", path, key)
			}
		}
		for _, nav := range []string{`href="/"`, `href="/live"`, `href="/message"`, `href="/config"`, `href="/logs"`, `href="/activity"`} {
			if !strings.Contains(body, nav) {
				t.Errorf("%s 缺少导航 %s", path, nav)
			}
		}
	}
}

// TestActivityPageBlocks 活动页补齐送礼/主动/点赞块与事件详情入口。
func TestActivityPageBlocks(t *testing.T) {
	srv := newTestServerWithRuntime(t, &fakeRepo{}, &fakeControl{}, &fakeLogs{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity", nil))
	body := rec.Body.String()
	for _, want := range []string{"/api/gift/log", "/api/gift/quota", "/api/proactive/log", "/api/like/log", "/api/events/"} {
		if !strings.Contains(body, want) {
			t.Errorf("活动页缺少 %q", want)
		}
	}
}
