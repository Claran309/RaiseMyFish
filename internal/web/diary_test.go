package web

// 本文件测日报接口：列表/详情/导出、手动生成与页面入口。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/diary"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

type fakeDiaryRepo struct {
	rows      []store.DailySummary
	listErr   error
	byDay     map[string]store.DailySummary
	detailErr error
	gotLimit  int
}

func (f *fakeDiaryRepo) DayEvents(context.Context, time.Time, time.Time, int) ([]store.DiaryEvent, error) {
	return nil, nil
}

func (f *fakeDiaryRepo) SaveDailySummary(context.Context, store.DailySummary) error { return nil }

func (f *fakeDiaryRepo) DailySummary(_ context.Context, day string) (store.DailySummary, error) {
	if f.detailErr != nil {
		return store.DailySummary{}, f.detailErr
	}
	row, ok := f.byDay[day]
	if !ok {
		return store.DailySummary{}, store.ErrDiaryNotFound
	}
	return row, nil
}

func (f *fakeDiaryRepo) DailySummaries(_ context.Context, limit int) ([]store.DailySummary, error) {
	f.gotLimit = limit
	return f.rows, f.listErr
}

type fakeDiaryGenerator struct {
	day    string
	calls  int
	result store.DailySummary
	err    error
}

func (f *fakeDiaryGenerator) Generate(_ context.Context, day string) (store.DailySummary, error) {
	f.calls++
	f.day = day
	return f.result, f.err
}

func newTestServerWithDiary(t *testing.T, repo store.DiaryRepo, generator DiaryGenerator, now time.Time) *Server {
	t.Helper()
	s, err := New(Options{
		Addr: "127.0.0.1:8090", Repo: &fakeRepo{}, Log: zap.NewNop(),
		Diary: repo, DiaryGenerator: generator, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestDiaryListAndDetail 列表给预览，详情给全文；坏日期 400、缺失 404。
func TestDiaryListAndDetail(t *testing.T) {
	updated := time.Date(2026, 11, 11, 0, 12, 0, 0, time.UTC)
	long := strings.Repeat("热", 260)
	repo := &fakeDiaryRepo{
		rows: []store.DailySummary{{Day: "2026-11-10", Content: long, Model: "model-a", EventCount: 42, UpdatedAt: updated}},
		byDay: map[string]store.DailySummary{
			"2026-11-10": {Day: "2026-11-10", Content: "## 概览\n今天很热闹。", Model: "model-a", EventCount: 42, UpdatedAt: updated},
		},
	}
	srv := newTestServerWithDiary(t, repo, nil, updated)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/diary?limit=7"))
	if rec.Code != http.StatusOK {
		t.Fatalf("列表状态码 %d：%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["count"] != float64(1) || repo.gotLimit != 7 {
		t.Fatalf("列表条数/limit 错误：%v limit=%d", body, repo.gotLimit)
	}
	item, _ := body["items"].([]any)[0].(map[string]any)
	if item["day"] != "2026-11-10" || item["event_count"] != float64(42) || item["model"] != "model-a" {
		t.Errorf("列表字段错误：%v", item)
	}
	preview, _ := item["preview"].(string)
	if !strings.HasSuffix(preview, "…") || len([]rune(preview)) != maxTextRunes+1 {
		t.Errorf("列表预览未截断：%q", preview)
	}
	if _, ok := item["content"]; ok {
		t.Error("列表不应返回全文")
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/diary/2026-11-10"))
	if rec.Code != http.StatusOK {
		t.Fatalf("详情状态码 %d：%s", rec.Code, rec.Body.String())
	}
	detail := decodeBody(t, rec)
	if detail["content"] != "## 概览\n今天很热闹。" || detail["event_count"] != float64(42) {
		t.Errorf("详情字段错误：%v", detail)
	}

	for target, want := range map[string]int{
		"/api/diary/2026-1-1":   http.StatusBadRequest,
		"/api/diary/2026-11-09": http.StatusNotFound,
	} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, readReq(target))
		if rec.Code != want {
			t.Errorf("%s 状态码 %d，期望 %d", target, rec.Code, want)
		}
	}
}

// TestDiaryExport 导出 Markdown 附件，文件名与正文使用同一日期。
func TestDiaryExport(t *testing.T) {
	repo := &fakeDiaryRepo{byDay: map[string]store.DailySummary{
		"2026-11-10": {Day: "2026-11-10", Content: "## 概览\n今天很热闹。"},
	}}
	srv := newTestServerWithDiary(t, repo, nil, time.Now())

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/diary/2026-11-10/export"))
	if rec.Code != http.StatusOK {
		t.Fatalf("导出状态码 %d：%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("导出类型错误：%s", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="diary-2026-11-10.md"` {
		t.Errorf("导出文件名错误：%s", cd)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "# 日报 2026-11-10\n\n") || !strings.Contains(body, "今天很热闹") {
		t.Errorf("导出正文错误：%s", body)
	}
}

// TestDiaryGenerate 手动生成要求 POST + confirm；缺省日期为东八区昨天。
func TestDiaryGenerate(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 30, 0, 0, time.UTC) // 上海 08:30
	gen := &fakeDiaryGenerator{result: store.DailySummary{Day: "2026-11-10", Content: "## 概览\nok", EventCount: 5}}
	repo := &fakeDiaryRepo{}
	srv := newTestServerWithDiary(t, repo, gen, now)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate", ""))
	if rec.Code != http.StatusBadRequest || gen.calls != 0 {
		t.Fatalf("缺 confirm 应拒绝：%d calls=%d", rec.Code, gen.calls)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate?confirm=yes", ""))
	if rec.Code != http.StatusOK || gen.day != "2026-11-10" {
		t.Fatalf("默认日期错误：%d day=%q %s", rec.Code, gen.day, rec.Body.String())
	}
	if decodeBody(t, rec)["ok"] != true {
		t.Error("生成响应缺少 ok")
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate?confirm=yes&day=2026-11-01", ""))
	if rec.Code != http.StatusOK || gen.day != "2026-11-01" {
		t.Fatalf("指定日期错误：%d day=%q", rec.Code, gen.day)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate?confirm=yes&day=2026-11-1", ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("坏日期应拒绝：%d %s", rec.Code, rec.Body.String())
	}

	gen.err = diary.ErrNoEvents
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate?confirm=yes&day=2026-11-02", ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("无事件应映射 400：%d %s", rec.Code, rec.Body.String())
	}

	gen.err = errors.New("llm down")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate?confirm=yes&day=2026-11-03", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("模型故障应映射 500：%d %s", rec.Code, rec.Body.String())
	}
}

// TestDiaryDisabled 未装配日报仓储或生成器时明确返回 503。
func TestDiaryDisabled(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	for _, req := range []*http.Request{
		readReq("/api/diary"),
		readReq("/api/diary/2026-11-10"),
		writeReq(http.MethodPost, "/api/diary/generate?confirm=yes", ""),
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s 状态码 %d，期望 503", req.Method, req.URL.Path, rec.Code)
		}
	}

	// 有日报仓储但无生成器：查看可用，生成 503。
	srv = newTestServerWithDiary(t, &fakeDiaryRepo{}, nil, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/diary/generate?confirm=yes", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("无生成器应 503：%d %s", rec.Code, rec.Body.String())
	}
}

// TestDiaryPage 日报页必须包含列表、生成与导出入口；首页导航可达。
func TestDiaryPage(t *testing.T) {
	srv := newTestServerWithDiary(t, &fakeDiaryRepo{}, nil, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/diary"))
	if rec.Code != http.StatusOK {
		t.Fatalf("日报页状态码 %d", rec.Code)
	}
	page := rec.Body.String()
	for _, want := range []string{`id="diary-list"`, `id="diary-content"`, "/api/diary/generate", "/export", "生成日报"} {
		if !strings.Contains(page, want) {
			t.Errorf("日报页缺少 %q", want)
		}
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/"))
	if !strings.Contains(rec.Body.String(), `href="/diary"`) {
		t.Error("首页导航缺少日报入口")
	}
}
