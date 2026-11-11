package web

// 本文件测记忆排序白名单与异常清理接口（#71/#72）。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

func TestMemorySortParam(t *testing.T) {
	repo := &fakeRepo{memory: []store.WebMemory{{
		ID: "m1", Kind: "fact", Title: "t", Summary: "s", Importance: 0.5,
		ValidFrom: time.Now(), CreatedAt: time.Now(),
	}}}
	srv := newTestServer(t, repo)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/memory?sort=importance"))
	if rec.Code != http.StatusOK || repo.memoryOrder != "importance" {
		t.Fatalf("重要度排序未透传：code=%d order=%q", rec.Code, repo.memoryOrder)
	}
	var body struct {
		Sort string `json:"sort"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Sort != "importance" {
		t.Fatalf("响应未回显排序：%v / %+v", err, body)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/memory"))
	if rec.Code != http.StatusOK || repo.memoryOrder != "time" {
		t.Fatalf("默认应按时间：code=%d order=%q", rec.Code, repo.memoryOrder)
	}

	repo.memoryOrder = ""
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/memory?sort=importance%20DESC"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知排序应 400，实际 %d", rec.Code)
	}
	if repo.memoryOrder != "" {
		t.Fatal("未知排序不应访问仓储")
	}
}

func TestErrorClearSingle(t *testing.T) {
	repo := &fakeRepo{}
	srv := newTestServer(t, repo)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/errors/live:1:9", ""))
	if rec.Code != http.StatusBadRequest || len(repo.cleared) != 0 {
		t.Fatalf("缺 confirm 应 400：code=%d cleared=%v", rec.Code, repo.cleared)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/errors/live:1:9?confirm=yes", ""))
	if rec.Code != http.StatusOK || len(repo.cleared) != 1 || repo.cleared[0] != "live:1:9" {
		t.Fatalf("带 confirm 应清理：code=%d cleared=%v", rec.Code, repo.cleared)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/errors/live:1:9"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("非 DELETE 应 405，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/errors/a/b?confirm=yes", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("多段 id 应 400，实际 %d", rec.Code)
	}

	repo.clearErr = store.ErrWebNotFound
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/errors/gone?confirm=yes", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404，实际 %d", rec.Code)
	}
}

func TestErrorClearOld(t *testing.T) {
	repo := &fakeRepo{clearCount: 4}
	srv := newTestServer(t, repo)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/errors/clear", `{"days":7}`))
	if rec.Code != http.StatusBadRequest || !repo.clearBefore.IsZero() {
		t.Fatalf("缺 confirm 应 400：code=%d before=%v", rec.Code, repo.clearBefore)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/errors/clear?confirm=yes", `{"days":-1}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法天数应 400，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/errors/clear?confirm=yes", `{"days":3650}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超范围天数应 400，实际 %d", rec.Code)
	}

	started := time.Now()
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/errors/clear?confirm=yes", `{"days":7}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("批量清理失败：%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Cleared int64 `json:"cleared"`
		Days    int   `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Cleared != 4 || body.Days != 7 {
		t.Fatalf("响应不符：%v / %+v", err, body)
	}
	// before 应约等于「现在 - 7 天」。
	if want := started.AddDate(0, 0, -7); repo.clearBefore.Sub(want) > time.Minute || want.Sub(repo.clearBefore) > time.Minute {
		t.Fatalf("清理时间点错误：got=%v want≈%v", repo.clearBefore, want)
	}

	// 空 body 走默认 7 天。
	repo.clearBefore = time.Time{}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/errors/clear?confirm=yes", `{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("默认天数应成功：%d %s", rec.Code, rec.Body.String())
	}
	if repo.clearBefore.IsZero() || time.Since(repo.clearBefore) < 6*24*time.Hour {
		t.Fatalf("默认天数未生效：%v", repo.clearBefore)
	}

	repo.clearBeforeErr = errors.New("db down")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/errors/clear?confirm=yes", `{"days":7}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("仓储错误应 500，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/errors/clear"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405，实际 %d", rec.Code)
	}
}

func TestMemoryAndErrorPagesExposeControls(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/memory"))
	page := rec.Body.String()
	for _, want := range []string{`id="sort"`, `value="importance"`, `id="group"`, "没有匹配的记忆", "/api/memory?q="} {
		if !strings.Contains(page, want) {
			t.Errorf("记忆页缺少 %q", want)
		}
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/"))
	overview := rec.Body.String()
	for _, want := range []string{`id="err-clear-old"`, "/api/errors/clear", "clearError(", "清理7天前"} {
		if !strings.Contains(overview, want) {
			t.Errorf("概览页缺少 %q", want)
		}
	}
}
