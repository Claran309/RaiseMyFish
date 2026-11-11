package web

// 观众侧写面板接口与页面入口的测试（R08）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

type fakeViewerSummary struct {
	state       ViewerSummaryState
	pauseCalls  []bool
	backfills   []int
	rebuilds    []string
	revokes     []string
	pauseErr    error
	backfillErr error
	rebuildErr  error
	revokeErr   error
}

func (f *fakeViewerSummary) ViewerSummaryStatus(context.Context) ViewerSummaryState {
	return f.state
}

func (f *fakeViewerSummary) SetViewerSummaryPaused(paused bool) error {
	f.pauseCalls = append(f.pauseCalls, paused)
	f.state.Paused = paused
	return f.pauseErr
}

func (f *fakeViewerSummary) TriggerViewerBackfill(_ context.Context, limit int) error {
	f.backfills = append(f.backfills, limit)
	return f.backfillErr
}

func (f *fakeViewerSummary) RebuildViewerSummary(_ context.Context, uid string) error {
	f.rebuilds = append(f.rebuilds, uid)
	return f.rebuildErr
}

func (f *fakeViewerSummary) RevokeViewerSummary(_ context.Context, uid string) error {
	f.revokes = append(f.revokes, uid)
	return f.revokeErr
}

func newViewerSummaryServer(t *testing.T, control ViewerSummaryControl) *Server {
	t.Helper()
	srv, err := New(Options{Addr: "127.0.0.1:8090", Repo: &fakeRepo{}, Log: zap.NewNop(), ViewerSummary: control})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestViewerSummaryAPIStatus(t *testing.T) {
	control := &fakeViewerSummary{state: ViewerSummaryState{
		Available: true, Enabled: true, Running: true, Paused: false,
		BackfillPending: 20, BackfillRunning: true,
		IntervalSeconds: 3600, LastSuccessAt: "2026-11-10T12:00:00Z",
		Profiles:       ViewerSummaryProfiles{Summarized: 7, Failed: 1, Fallback: 2, Sources: 42, LastSummaryAt: "2026-11-10T11:00:00Z"},
		PendingPublish: 2, PendingPurge: 1,
		LastRun: ViewerSummaryRun{Candidates: 3, Summarized: 2, Fallback: 1, Expired: 4, BusySkipped: 1},
	}}
	srv := newViewerSummaryServer(t, control)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/viewer/summary"))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态查询失败：%d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		`"running":true`, `"summarized":7`, `"fallback":2`, `"sources":42`,
		`"pending_publish":2`, `"pending_purge":1`, `"busy_skipped":1`,
		`"backfill_pending":20`, `"backfill_running":true`,
		`"last_summary_at":"2026-11-10T11:00:00Z"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("状态响应缺少 %s：%s", want, rec.Body.String())
		}
	}
}

func TestViewerSummaryAPIActions(t *testing.T) {
	control := &fakeViewerSummary{}
	srv := newViewerSummaryServer(t, control)

	// 写操作必须带 confirm=yes。
	rec := postJSON(t, srv, "/api/viewer/summary", `{"action":"pause"}`)
	if rec.Code != http.StatusBadRequest || len(control.pauseCalls) != 0 {
		t.Fatalf("缺 confirm 应拒绝：%d %v", rec.Code, control.pauseCalls)
	}

	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"pause"}`)
	if rec.Code != http.StatusOK || len(control.pauseCalls) != 1 || !control.pauseCalls[0] {
		t.Fatalf("暂停失败：%d %v %s", rec.Code, control.pauseCalls, rec.Body.String())
	}
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"resume"}`)
	if rec.Code != http.StatusOK || len(control.pauseCalls) != 2 || control.pauseCalls[1] {
		t.Fatalf("恢复失败：%d %v", rec.Code, control.pauseCalls)
	}

	// 重建/撤回必须指定 uid。
	for _, action := range []string{"rebuild", "revoke"} {
		rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"`+action+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 缺 uid 应 400：%d %s", action, rec.Code, rec.Body.String())
		}
	}
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"rebuild","uid":" 42 "}`)
	if rec.Code != http.StatusOK || len(control.rebuilds) != 1 || control.rebuilds[0] != "42" {
		t.Fatalf("重建失败：%d %v %s", rec.Code, control.rebuilds, rec.Body.String())
	}
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"revoke","uid":"42"}`)
	if rec.Code != http.StatusOK || len(control.revokes) != 1 || control.revokes[0] != "42" {
		t.Fatalf("撤回失败：%d %v %s", rec.Code, control.revokes, rec.Body.String())
	}

	// 未知观众 → 404；未知动作 → 400；服务错误 → 400。
	control.revokeErr = store.ErrViewerNotFound
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"revoke","uid":"404"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知观众应 404：%d %s", rec.Code, rec.Body.String())
	}
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"explode"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知动作应 400：%d %s", rec.Code, rec.Body.String())
	}
	control.rebuildErr = context.DeadlineExceeded
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"rebuild","uid":"42"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("服务错误应 400：%d %s", rec.Code, rec.Body.String())
	}
}

// TestViewerSummaryAPIBackfill 手动回填：默认 50、上限 500、排队失败给 400。
func TestViewerSummaryAPIBackfill(t *testing.T) {
	control := &fakeViewerSummary{}
	srv := newViewerSummaryServer(t, control)

	rec := postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"backfill"}`)
	if rec.Code != http.StatusOK || len(control.backfills) != 1 || control.backfills[0] != 50 {
		t.Fatalf("默认回填不符：%d %v %s", rec.Code, control.backfills, rec.Body.String())
	}
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"backfill","limit":120}`)
	if rec.Code != http.StatusOK || len(control.backfills) != 2 || control.backfills[1] != 120 {
		t.Fatalf("指定回填不符：%d %v", rec.Code, control.backfills)
	}
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"backfill","limit":501}`)
	if rec.Code != http.StatusBadRequest || len(control.backfills) != 2 {
		t.Fatalf("超上限应拒绝：%d %v", rec.Code, control.backfills)
	}
	control.backfillErr = errors.New("已有观众侧写回填任务在执行")
	rec = postJSON(t, srv, "/api/viewer/summary?confirm=yes", `{"action":"backfill","limit":10}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "回填") {
		t.Fatalf("排队失败应 400：%d %s", rec.Code, rec.Body.String())
	}
}

func TestViewerSummaryAPIDisabled(t *testing.T) {
	srv := newViewerSummaryServer(t, nil)
	for _, target := range []string{"/api/viewer/summary"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, readReq(target))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s 未启用应 503：%d", target, rec.Code)
		}
	}
}

// TestMemoryPageViewerSummarySection 页面必须提供侧写状态与三个操作入口。
func TestMemoryPageViewerSummarySection(t *testing.T) {
	srv := newViewerSummaryServer(t, &fakeViewerSummary{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/memory"))
	if rec.Code != http.StatusOK {
		t.Fatalf("记忆页状态码 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"观众侧写", "id=\"vs-status\"", "id=\"vs-uid\"", "id=\"vs-limit\"",
		"暂停", "恢复", "回填", "重建", "撤回",
		"/api/viewer/summary",
		"最后成功", "失败", "fallback", "来源", "待发布知识",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("记忆页缺少侧写控制入口：%q", want)
		}
	}
}

// TestViewerSummaryStateJSONShape 时间字段为空时省略，生成时间始终存在。
func TestViewerSummaryStateJSONShape(t *testing.T) {
	state := ViewerSummaryState{GeneratedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, "last_summary_at") || strings.Contains(body, "last_error") {
		t.Fatalf("空时间/错误不应输出：%s", body)
	}
	if !strings.Contains(body, `"generated_at":"2026-11-10T12:00:00Z"`) {
		t.Fatalf("生成时间缺失：%s", body)
	}
}
