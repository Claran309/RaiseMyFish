package web

// 本文件是面板端到端冒烟（httptest）：所有页面可打开、关键控件与空态/分组文案存在，
// 并串起注入、日报、记忆、异常清理、发送记录的读写主路径。
//
// 不依赖真实 PG：仓储用包内 fake；真实 SQL 语义由 database 包集成测试覆盖。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

const e2eToken = "e2e-panel-token"

func e2eTime(day int) time.Time {
	return time.Date(2026, 11, day, 12, 0, 0, 0, time.UTC)
}

func e2ePtrTime(at time.Time) *time.Time { return &at }

// newE2EServer 组装一个所有可选依赖都就绪的面板。
func newE2EServer(t *testing.T) (*Server, *fakeRepo, *fakeInjectionRepo, *fakeDiaryRepo, *fakeDiaryGenerator) {
	t.Helper()
	now := e2eTime(11)
	repo := &fakeRepo{
		status: store.WebStatus{
			EventsTotal: 2, EventsPending: 1, MemoryTotal: 1,
			MemoryByKind: map[string]int64{"fact": 1},
			SendTotal:    2, SendFailed: 1, GeneratedAt: now,
		},
		memory: []store.WebMemory{{
			ID: "m1", Kind: "fact", Title: "喜欢猫", Summary: "主人喜欢猫", Importance: 0.9,
			ValidFrom: now, CreatedAt: now,
		}},
		events: []store.WebEvent{
			{ID: "e1", Channel: "live", Dir: "in", Session: "room:1", FromName: "观众", Text: "晚上好", At: now, Status: "done"},
			{ID: "e2", Channel: "live", Dir: "out", Session: "room:1", FromName: "大肥鱼", Text: "晚上好呀", At: now, Status: "retry"},
		},
		errors: []store.WebEvent{
			{ID: "e3", Channel: "live", Dir: "out", FromName: "大肥鱼", Text: "发送失败", At: now, Status: "dead"},
			{ID: "e4", Channel: "dm", Dir: "out", FromName: "大肥鱼", Text: "重试中", At: now, Status: "retry"},
		},
		details: map[string]store.WebEventDetail{
			"e1": {
				WebEvent: store.WebEvent{
					ID: "e1", Channel: "live", Dir: "in", Session: "room:1",
					FromName: "观众", Text: "晚上好", At: now, Status: "done",
				},
				FromID: "1001", CreatedAt: now,
			},
		},
		sends: []store.WebSend{
			{EventID: "e2", Channel: "live", OK: true, Verified: false, MsgID: "m-1", Error: "unconfirmed", At: now},
			{EventID: "e1", Channel: "live", OK: false, Error: "boom", At: now},
		},
	}
	injections := &fakeInjectionRepo{rows: []store.WebInjection{
		{ID: 1, Content: "永久设定", CreatedAt: now, UpdatedAt: now},
		{ID: 2, Content: "临时设定", CreatedAt: now, UpdatedAt: now, ExpiresAt: e2ePtrTime(now.Add(time.Hour))},
	}}
	diaryRow := store.DailySummary{
		Day: "2026-11-10", Content: "## 昨天的总结", Model: "m", EventCount: 8,
		CreatedAt: now, UpdatedAt: now,
	}
	diaryRepo := &fakeDiaryRepo{
		rows: []store.DailySummary{
			diaryRow,
			{Day: "2026-10-31", Content: "## 上月总结", Model: "m", EventCount: 3, CreatedAt: now, UpdatedAt: now},
		},
		byDay: map[string]store.DailySummary{"2026-11-10": diaryRow},
	}
	generator := &fakeDiaryGenerator{result: store.DailySummary{
		Day: "2026-11-11", Content: "## 新日报", Model: "m", EventCount: 2,
		CreatedAt: now, UpdatedAt: now,
	}}
	accounts := &fakeAccounts{rows: []account.Account{{
		ID: "a1", MID: "1001", Name: "主号", Status: account.StatusActive, IsActive: true, LastCheck: now,
	}}}
	srv, err := New(Options{
		Addr: "127.0.0.1:8090", Repo: repo, Log: zap.NewNop(), Token: e2eToken,
		Now:            func() time.Time { return now },
		Accounts:       accounts,
		Injections:     injections,
		Diary:          diaryRepo,
		DiaryGenerator: generator,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, repo, injections, diaryRepo, generator
}

// TestE2EPagesKeyPaths 十个页面都能打开，且关键控件、空态/分组文案在位。
func TestE2EPagesKeyPaths(t *testing.T) {
	srv, _, _, _, _ := newE2EServer(t)
	cases := []struct {
		path  string
		wants []string
	}{
		{"/", []string{"操作人注入", `id="inj-list"`, `id="errors"`,
			"永久设定", "临时设定", "需人工处理（dead）", "自动重试中（retry）",
			"没有异常，稳", "暂无事件"}},
		{"/memory", []string{`id="sort"`, `id="group"`, "分组只影响排版", "没有匹配的记忆"}},
		{"/knowledge", []string{`id="cards"`, `id="rows"`}},
		{"/activity", []string{`id="rows"`, `id="sends"`, "已发送（未确认）", "暂无事件", "最近发送"}},
		{"/accounts", []string{`id="cookie"`, `id="note"`, `id="rows"`}},
		{"/live", []string{`id="live-btn"`, `id="watch-start"`}},
		{"/message", []string{`id="comment-btn"`, `id="dm-btn"`, `id="comment-rows"`}},
		{"/config", []string{`id="proactive-save"`, `id="wd-rows"`}},
		{"/logs", []string{`id="logbox"`, `id="level"`}},
		{"/diary", []string{`id="diary-list"`, "生成日报", "还没有日报", "byMonth"}},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, readReq(tc.path))
		if rec.Code != http.StatusOK {
			t.Errorf("%s 状态码 = %d", tc.path, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, want := range []string{"<style>", "大肥鱼面板", `name="panel-token"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s 缺少基础骨架 %q", tc.path, want)
			}
		}
		if !strings.Contains(body, `content="`+e2eToken+`"`) {
			t.Errorf("%s 未注入面板令牌", tc.path)
		}
		if strings.Contains(body, pageTokenPlaceholder) {
			t.Errorf("%s 仍残留令牌占位符", tc.path)
		}
		for _, want := range tc.wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s 缺少 %q", tc.path, want)
			}
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s 缺少 nosniff：%q", tc.path, got)
		}
	}
}

// TestE2EInjectionRoundTrip 注入列表 → 新建 → 编辑 → 删除；跨站写被拒。
func TestE2EInjectionRoundTrip(t *testing.T) {
	srv, _, injections, _, _ := newE2EServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/inject"))
	var list struct {
		Count int              `json:"count"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Count != 2 || len(list.Items) != 2 {
		t.Fatalf("注入列表不符：%v / %+v", err, list)
	}

	// 跨站 Origin 即使带 confirm 也必须被拒，且不触达仓储。
	evil := writeReq(http.MethodPost, "/api/inject?confirm=yes", `{"content":"坏设定"}`)
	evil.Header.Set("Origin", "http://evil.example")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, evil)
	if rec.Code != http.StatusForbidden || injections.createContent != "" {
		t.Fatalf("跨站写应 403 且不落库：%d content=%q", rec.Code, injections.createContent)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/inject?confirm=yes", `{"content":"加个设定","ttl_seconds":0}`))
	if rec.Code != http.StatusCreated || injections.createContent != "加个设定" || injections.createExpires != nil {
		t.Fatalf("新建注入失败：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPut, "/api/inject/3?confirm=yes", `{"content":"改设定","ttl_seconds":3600}`))
	if rec.Code != http.StatusOK || injections.updateID != 3 || injections.updateContent != "改设定" {
		t.Fatalf("编辑注入失败：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/inject/3?confirm=yes", ""))
	if rec.Code != http.StatusOK || injections.deleteID != 3 {
		t.Fatalf("删除注入失败：%d %s", rec.Code, rec.Body.String())
	}
}

// TestE2EDiaryRoundTrip 日报列表 → 详情 → 导出 → 手动生成；缺 confirm 被拒。
func TestE2EDiaryRoundTrip(t *testing.T) {
	srv, _, _, _, generator := newE2EServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/diary?limit=60"))
	var list struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Count != 2 {
		t.Fatalf("日报列表不符：%v / %s", err, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/diary/2026-11-10"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "昨天的总结") {
		t.Fatalf("日报详情不符：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/diary/2026-11-10/export"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "# 日报 2026-11-10") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "diary-2026-11-10.md") {
		t.Fatalf("日报导出不符：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate?day=2026-11-10", ""))
	if rec.Code != http.StatusBadRequest || generator.calls != 0 {
		t.Fatalf("缺 confirm 不应生成：%d calls=%d", rec.Code, generator.calls)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate?confirm=yes&day=2026-11-10", ""))
	if rec.Code != http.StatusOK || generator.calls != 1 || generator.day != "2026-11-10" {
		t.Fatalf("手动生成失败：%d calls=%d day=%q %s", rec.Code, generator.calls, generator.day, rec.Body.String())
	}
}

// TestE2EMemoryErrorsRoundTrip 记忆排序/删除与异常单条/批量清理。
func TestE2EMemoryErrorsRoundTrip(t *testing.T) {
	srv, repo, _, _, _ := newE2EServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/memory?sort=importance&kind=fact&q=猫"))
	var mem struct {
		Count int    `json:"count"`
		Sort  string `json:"sort"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &mem); err != nil || mem.Count != 1 || mem.Sort != "importance" {
		t.Fatalf("记忆列表不符：%v / %s", err, rec.Body.String())
	}
	if repo.memoryOrder != "importance" {
		t.Fatalf("排序未透传：%q", repo.memoryOrder)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/memory/m1?confirm=yes", ""))
	if rec.Code != http.StatusOK || len(repo.archived) != 1 || repo.archived[0] != "m1" {
		t.Fatalf("记忆删除失败：%d archived=%v", rec.Code, repo.archived)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/errors?limit=5"))
	var errs struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errs); err != nil || errs.Count != 2 {
		t.Fatalf("异常列表不符：%v / %s", err, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/errors/e3?confirm=yes", ""))
	if rec.Code != http.StatusOK || len(repo.cleared) != 1 || repo.cleared[0] != "e3" {
		t.Fatalf("单条清理失败：%d cleared=%v", rec.Code, repo.cleared)
	}

	repo.clearCount = 3
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/errors/clear?confirm=yes", `{"days":7}`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cleared":3`) {
		t.Fatalf("批量清理失败：%d %s", rec.Code, rec.Body.String())
	}
}

// TestE2ESendLogAndEventDetail 发送记录「已发送（未确认）」可见，事件详情可点开。
func TestE2ESendLogAndEventDetail(t *testing.T) {
	srv, _, _, _, _ := newE2EServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/send_log?limit=20"))
	var sends struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sends); err != nil || len(sends.Items) != 2 {
		t.Fatalf("发送记录不符：%v / %s", err, rec.Body.String())
	}
	if sends.Items[0]["verified"] != false || sends.Items[0]["error"] != "unconfirmed" {
		t.Fatalf("未确认记录字段不符：%+v", sends.Items[0])
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/events/e1"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"from_id":"1001"`) {
		t.Fatalf("事件详情不符：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/events/missing"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的事件应 404，实际 %d", rec.Code)
	}
}
