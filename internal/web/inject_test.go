package web

// 本文件测看板注入接口：列表、新建（默认/永久/非法有效期）、编辑、删除与页面入口。

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

type fakeInjectionRepo struct {
	rows    []store.WebInjection
	listErr error

	createContent string
	createExpires *time.Time
	createErr     error

	updateID      int64
	updateContent string
	updateExpires *time.Time
	updateErr     error

	deleteID  int64
	deleteErr error

	nextID int64
}

func (f *fakeInjectionRepo) ActiveInjections(context.Context, time.Time) ([]store.WebInjection, error) {
	return f.rows, f.listErr
}

func (f *fakeInjectionRepo) CreateInjection(_ context.Context, content string, expiresAt *time.Time) (store.WebInjection, error) {
	f.createContent, f.createExpires = content, expiresAt
	if f.createErr != nil {
		return store.WebInjection{}, f.createErr
	}
	f.nextID++
	return store.WebInjection{ID: f.nextID, Content: content, CreatedAt: time.Now(), UpdatedAt: time.Now(), ExpiresAt: expiresAt}, nil
}

func (f *fakeInjectionRepo) UpdateInjection(_ context.Context, id int64, content string, expiresAt *time.Time) (store.WebInjection, error) {
	f.updateID, f.updateContent, f.updateExpires = id, content, expiresAt
	if f.updateErr != nil {
		return store.WebInjection{}, f.updateErr
	}
	return store.WebInjection{ID: id, Content: content, CreatedAt: time.Now(), UpdatedAt: time.Now(), ExpiresAt: expiresAt}, nil
}

func (f *fakeInjectionRepo) DeleteInjection(_ context.Context, id int64) error {
	f.deleteID = id
	return f.deleteErr
}

func newTestServerWithInjections(t *testing.T, repo store.WebRepo, injections store.InjectionRepo, now time.Time) *Server {
	t.Helper()
	s, err := New(Options{
		Addr: "127.0.0.1:8090", Repo: repo, Log: zap.NewNop(),
		Injections: injections, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
	}
	return body
}

// TestInjectList 列表返回有效注入与剩余秒数；永久条目 permanent=true。
func TestInjectList(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	expires := now.Add(30 * time.Minute)
	repo := &fakeInjectionRepo{rows: []store.WebInjection{
		{ID: 2, Content: "临时设定", CreatedAt: now, UpdatedAt: now, ExpiresAt: &expires},
		{ID: 1, Content: "永久设定", CreatedAt: now, UpdatedAt: now},
	}}
	srv := newTestServerWithInjections(t, &fakeRepo{}, repo, now)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/api/inject"))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	items, _ := body["items"].([]any)
	if body["count"] != float64(2) || len(items) != 2 {
		t.Fatalf("列表条数错误：%v", body)
	}
	first, _ := items[0].(map[string]any)
	if first["content"] != "临时设定" || first["permanent"] != false || first["remaining_seconds"] != float64(1800) {
		t.Errorf("临时注入字段错误：%v", first)
	}
	second, _ := items[1].(map[string]any)
	if second["permanent"] != true || second["expires_at"] != "" || second["remaining_seconds"] != float64(0) {
		t.Errorf("永久注入字段错误：%v", second)
	}
}

// TestInjectDisabled 未装配注入仓储时读写都返回 503。
func TestInjectDisabled(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	for _, req := range []*http.Request{readReq("/api/inject"), writeReq(http.MethodPost, "/api/inject?confirm=yes", `{"content":"x"}`)} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s 状态码 %d", req.Method, req.URL.Path, rec.Code)
		}
	}
}

// TestInjectCreate TTL 缺省 1 小时、0 永久、越界拒绝；写操作必须 confirm。
func TestInjectCreate(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	repo := &fakeInjectionRepo{}
	srv := newTestServerWithInjections(t, &fakeRepo{}, repo, now)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/inject", `{"content":"加个设定"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 confirm 应拒绝：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/inject?confirm=yes", `{"content":"加个设定"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("新建失败：%d %s", rec.Code, rec.Body.String())
	}
	if repo.createContent != "加个设定" || repo.createExpires == nil || !repo.createExpires.Equal(now.Add(time.Hour)) {
		t.Errorf("默认有效期应为 1 小时：%q %v", repo.createContent, repo.createExpires)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/inject?confirm=yes", `{"content":"永久","ttl_seconds":0}`))
	if rec.Code != http.StatusCreated || repo.createExpires != nil {
		t.Errorf("ttl=0 应永久：%d %v", rec.Code, repo.createExpires)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/inject?confirm=yes", `{"content":"太短","ttl_seconds":30}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("有效期过短应拒绝：%d %s", rec.Code, rec.Body.String())
	}

	repo.createErr = store.ErrInjectionInvalid
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPost, "/api/inject?confirm=yes", `{"content":"坏内容"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法内容应映射 400：%d %s", rec.Code, rec.Body.String())
	}
}

// TestInjectUpdateAndDelete 编辑与删除都要求 confirm；不存在映射 404，坏 id 映射 400。
func TestInjectUpdateAndDelete(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	repo := &fakeInjectionRepo{}
	srv := newTestServerWithInjections(t, &fakeRepo{}, repo, now)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPut, "/api/inject/7?confirm=yes", `{"content":"改设定","ttl_seconds":3600}`))
	if rec.Code != http.StatusOK || repo.updateID != 7 || repo.updateContent != "改设定" {
		t.Fatalf("编辑失败：%d %s（id=%d content=%q）", rec.Code, rec.Body.String(), repo.updateID, repo.updateContent)
	}
	if repo.updateExpires == nil || !repo.updateExpires.Equal(now.Add(time.Hour)) {
		t.Errorf("编辑有效期错误：%v", repo.updateExpires)
	}

	repo.updateErr = store.ErrWebNotFound
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodPut, "/api/inject/8?confirm=yes", `{"content":"改设定"}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在应映射 404：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/inject/7?confirm=yes", ""))
	if rec.Code != http.StatusOK || repo.deleteID != 7 {
		t.Fatalf("删除失败：%d %s（id=%d）", rec.Code, rec.Body.String(), repo.deleteID)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/inject/abc?confirm=yes", ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("坏 id 应映射 400：%d %s", rec.Code, rec.Body.String())
	}

	repo.deleteErr = errors.New("db down")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, writeReq(http.MethodDelete, "/api/inject/9?confirm=yes", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("存储故障应映射 500：%d %s", rec.Code, rec.Body.String())
	}
}

// TestIndexHasInjectionPanel 首页必须包含注入输入框、有效期与列表入口（纯前端无编译期保护）。
func TestIndexHasInjectionPanel(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, readReq("/"))
	if rec.Code != http.StatusOK {
		t.Fatalf("首页状态码 %d", rec.Code)
	}
	page := rec.Body.String()
	for _, want := range []string{"操作人注入", `id="inj-content"`, `id="inj-ttl"`, `id="inj-list"`, "/api/inject"} {
		if !strings.Contains(page, want) {
			t.Errorf("首页缺少 %q", want)
		}
	}
}
