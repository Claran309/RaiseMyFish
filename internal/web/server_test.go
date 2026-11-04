package web

// 本文件用 httptest 测面板：JSON 结构、删除需 confirm、非回环拒绝、页面可访问。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

type fakeRepo struct {
	status     store.WebStatus
	memory     []store.WebMemory
	events     []store.WebEvent
	sends      []store.WebSend
	archived   []string
	archiveErr error
}

func (f *fakeRepo) Status(context.Context) (store.WebStatus, error) { return f.status, nil }

func (f *fakeRepo) Memory(context.Context, string, string, int) ([]store.WebMemory, error) {
	return f.memory, nil
}

func (f *fakeRepo) ArchiveMemory(_ context.Context, id string) error {
	if f.archiveErr != nil {
		return f.archiveErr
	}
	f.archived = append(f.archived, id)
	return nil
}

func (f *fakeRepo) Events(_ context.Context, dir string, _ int) ([]store.WebEvent, error) {
	if dir != "" && dir != "in" && dir != "out" {
		return nil, store.ErrInvalid
	}
	return f.events, nil
}

func (f *fakeRepo) SendLogs(context.Context, int) ([]store.WebSend, error) { return f.sends, nil }

func newTestServer(t *testing.T, repo store.WebRepo) *Server {
	t.Helper()
	s, err := New(Options{Addr: "127.0.0.1:8090", Repo: repo, Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestStatusJSON 断言概览字段齐全。
func TestStatusJSON(t *testing.T) {
	repo := &fakeRepo{status: store.WebStatus{
		EventsTotal: 10, EventsPending: 3, MemoryTotal: 5,
		MemoryByKind: map[string]int64{"fact": 4, "preference": 1},
		SendTotal:    8, SendFailed: 1, CostUSD: 0.42,
		GeneratedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	}}
	srv := newTestServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	for _, k := range []string{"events_total", "events_pending", "memory_total", "memory_by_kind", "send_total", "send_failed", "cost_usd", "generated_at"} {
		if _, ok := body[k]; !ok {
			t.Errorf("缺少字段 %s", k)
		}
	}
	if body["events_total"].(float64) != 10 || body["cost_usd"].(float64) != 0.42 {
		t.Errorf("数值不符：%+v", body)
	}
}

// TestMemoryListTruncates 列表截断长文本。
func TestMemoryListTruncates(t *testing.T) {
	long := strings.Repeat("鱼", 500)
	repo := &fakeRepo{memory: []store.WebMemory{{
		ID: "m1", Kind: "fact", Title: "标题", Summary: long, Importance: 0.9,
		ValidFrom: time.Now(), CreatedAt: time.Now(),
	}}}
	srv := newTestServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/memory?limit=10", nil))

	var body struct {
		Items []struct {
			ID      string `json:"id"`
			Summary string `json:"summary"`
		} `json:"items"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if body.Count != 1 || body.Items[0].ID != "m1" {
		t.Fatalf("列表不符：%+v", body)
	}
	if n := len([]rune(body.Items[0].Summary)); n > maxTextRunes+1 {
		t.Errorf("摘要未截断：%d rune", n)
	}
}

// TestMemoryDeleteRequiresConfirm 无 confirm 拒绝，带 confirm 才归档。
func TestMemoryDeleteRequiresConfirm(t *testing.T) {
	repo := &fakeRepo{}
	srv := newTestServer(t, repo)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/memory/m1", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 confirm 应 400，实际 %d", rec.Code)
	}
	if len(repo.archived) != 0 {
		t.Error("缺 confirm 不应删除")
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/memory/m1?confirm=yes", nil))
	if rec.Code != http.StatusOK || len(repo.archived) != 1 || repo.archived[0] != "m1" {
		t.Fatalf("带 confirm 应删除：code=%d archived=%v", rec.Code, repo.archived)
	}
}

// TestMemoryDeleteNotFound 不存在返回 404。
func TestMemoryDeleteNotFound(t *testing.T) {
	repo := &fakeRepo{archiveErr: store.ErrWebNotFound}
	srv := newTestServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/memory/nope?confirm=yes", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("应 404，实际 %d", rec.Code)
	}
}

// TestEventsJSON 活动流水结构 + dir 校验。
func TestEventsJSON(t *testing.T) {
	repo := &fakeRepo{events: []store.WebEvent{{
		ID: "live:1:2", Channel: "live", Dir: "in", Session: "1",
		FromName: "乙", Text: "在吗", At: time.Now(), Status: "done",
	}}}
	srv := newTestServer(t, repo)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events?dir=in&limit=20", nil))
	var body struct {
		Items []map[string]any `json:"items"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Count != 1 {
		t.Fatalf("解析失败：%v / %+v", err, body)
	}
	for _, k := range []string{"id", "channel", "dir", "session", "from_name", "text", "at", "status"} {
		if _, ok := body.Items[0][k]; !ok {
			t.Errorf("缺少字段 %s", k)
		}
	}

	// 非法 dir → 400
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events?dir=sideways", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 dir 应 400，实际 %d", rec.Code)
	}
}

// TestSendLogJSON 发送记录结构。
func TestSendLogJSON(t *testing.T) {
	repo := &fakeRepo{sends: []store.WebSend{{EventID: "e1", Channel: "live", OK: true, MsgID: "m", At: time.Now()}}}
	srv := newTestServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/send_log?limit=5", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "event_id") {
		t.Fatalf("响应不符：%d %s", rec.Code, rec.Body.String())
	}
}

// TestPagesRender 三个页面都能 200 且含内联样式。
func TestPagesRender(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	for _, path := range []string{"/", "/memory", "/activity"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s 状态码 = %d", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<style>") || !strings.Contains(body, "大肥鱼面板") {
			t.Errorf("%s 页面内容不完整", path)
		}
	}
	// 未知路径 404
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("未知路径应 404，实际 %d", rec.Code)
	}
}

// TestAddrMustBeLoopback 非回环地址在构造期被拒绝。
func TestAddrMustBeLoopback(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:8090", true},
		{"localhost:8090", true},
		{"[::1]:8090", true},
		{"0.0.0.0:8090", false},
		{"192.168.1.10:8090", false},
		{":8090", false},
		{"127.0.0.1", false},
		{"127.0.0.1:abc", false},
	}
	for _, tc := range cases {
		err := ValidateAddr(tc.addr)
		if tc.ok && err != nil {
			t.Errorf("%s 应允许：%v", tc.addr, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s 应拒绝", tc.addr)
		}
	}
	// 构造期同样拦截
	if _, err := New(Options{Addr: "0.0.0.0:8090", Repo: &fakeRepo{}, Log: zap.NewNop()}); err == nil {
		t.Error("0.0.0.0 应在 New 阶段被拒绝")
	}
}

// TestMethodNotAllowed 非 GET 的读接口返回 405。
func TestMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("应 405，实际 %d", rec.Code)
	}
}
