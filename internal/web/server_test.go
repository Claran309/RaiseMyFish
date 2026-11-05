package web

// 本文件用 httptest 测面板：JSON 结构、删除需 confirm、非回环拒绝、页面可访问。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

type fakeRepo struct {
	status        store.WebStatus
	embedding     store.WebEmbeddingStatus
	memory        []store.WebMemory
	events        []store.WebEvent
	channelEvents map[string][]store.WebEvent
	details       map[string]store.WebEventDetail
	detailErr     error
	errors        []store.WebEvent
	sends         []store.WebSend
	archived      []string
	archiveErr    error
}

func (f *fakeRepo) Status(context.Context) (store.WebStatus, error) { return f.status, nil }

func (f *fakeRepo) EmbeddingStatus(context.Context) (store.WebEmbeddingStatus, error) {
	return f.embedding, nil
}

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

func (f *fakeRepo) EventsByChannel(_ context.Context, channel, dir string, _ int) ([]store.WebEvent, error) {
	if dir != "" && dir != "in" && dir != "out" {
		return nil, store.ErrInvalid
	}
	return f.channelEvents[channel], nil
}

func (f *fakeRepo) Event(_ context.Context, id string) (store.WebEventDetail, error) {
	if f.detailErr != nil {
		return store.WebEventDetail{}, f.detailErr
	}
	detail, ok := f.details[id]
	if !ok {
		return store.WebEventDetail{}, store.ErrWebNotFound
	}
	return detail, nil
}

func (f *fakeRepo) Errors(context.Context, int) ([]store.WebEvent, error) { return f.errors, nil }

func (f *fakeRepo) SendLogs(context.Context, int) ([]store.WebSend, error) { return f.sends, nil }

type fakeAccounts struct {
	rows        []account.Account
	loadErr     error
	addErr      error
	activateErr error
	deleteErr   error
	checkErr    error
	addCalls    int
	addCookie   string
	addNote     string
	activated   []string
	deleted     []string
	checked     []string
	checkResult account.Account
}

func (f *fakeAccounts) Load(context.Context) ([]account.Account, error) {
	return f.rows, f.loadErr
}

func (f *fakeAccounts) Add(_ context.Context, cookie, note string) (account.Account, error) {
	f.addCalls++
	f.addCookie, f.addNote = cookie, note
	if f.addErr != nil {
		return account.Account{}, f.addErr
	}
	return account.Account{
		ID: "account-new", MID: "123", Name: "新号", Cookie: cookie, Status: account.StatusActive,
		LastCheck: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), IsActive: true, Note: note,
	}, nil
}

func (f *fakeAccounts) Activate(_ context.Context, id string) error {
	f.activated = append(f.activated, id)
	return f.activateErr
}

func (f *fakeAccounts) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

func (f *fakeAccounts) Check(_ context.Context, id string) (account.Account, error) {
	f.checked = append(f.checked, id)
	if f.checkErr != nil {
		return f.checkResult, f.checkErr
	}
	return f.checkResult, nil
}

func newTestServerWithAccounts(t *testing.T, repo store.WebRepo, accounts AccountService) *Server {
	t.Helper()
	s, err := New(Options{Addr: "127.0.0.1:8090", Repo: repo, Log: zap.NewNop(), Accounts: accounts})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

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

// TestEmbeddingStatusJSON 三路状态字段齐全；知识总数与 revision 由快照覆盖。
func TestEmbeddingStatusJSON(t *testing.T) {
	last := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	repo := &fakeRepo{embedding: store.WebEmbeddingStatus{
		Knowledge:   store.WebEmbeddingSection{Total: 8, Embedded: 8, LastUpdated: last},
		Memory:      store.WebEmbeddingSection{Total: 5, Embedded: 4, Pending: 1, LastUpdated: last.Add(time.Minute)},
		Viewer:      store.WebEmbeddingSection{Total: 3, Embedded: 1, Pending: 2},
		GeneratedAt: last,
	}}
	srv, err := New(Options{
		Addr: "127.0.0.1:8090", Repo: repo, Log: zap.NewNop(),
		KnowledgeInfo: func() (int, string) { return 10, strings.Repeat("a", 64) },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/embedding/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var body struct {
		Knowledge struct {
			Total       int64  `json:"total"`
			Embedded    int64  `json:"embedded"`
			Pending     int64  `json:"pending"`
			LastUpdated string `json:"last_updated"`
			Revision    string `json:"revision"`
		} `json:"knowledge"`
		Memory struct {
			Total    int64 `json:"total"`
			Embedded int64 `json:"embedded"`
			Pending  int64 `json:"pending"`
		} `json:"memory"`
		Viewer struct {
			Total    int64 `json:"total"`
			Embedded int64 `json:"embedded"`
			Pending  int64 `json:"pending"`
		} `json:"viewer"`
		GeneratedAt string `json:"generated_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if body.Knowledge.Total != 10 || body.Knowledge.Embedded != 8 || body.Knowledge.Pending != 2 {
		t.Errorf("知识状态不符：%+v", body.Knowledge)
	}
	if body.Knowledge.Revision != strings.Repeat("a", 64) || body.Knowledge.LastUpdated == "" {
		t.Errorf("知识 revision/最后同步缺失：%+v", body.Knowledge)
	}
	if body.Memory.Total != 5 || body.Memory.Embedded != 4 || body.Memory.Pending != 1 {
		t.Errorf("记忆状态不符：%+v", body.Memory)
	}
	if body.Viewer.Total != 3 || body.Viewer.Embedded != 1 || body.Viewer.Pending != 2 {
		t.Errorf("档案状态不符：%+v", body.Viewer)
	}
	if body.GeneratedAt == "" {
		t.Error("缺少 generated_at")
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/embedding/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("非 GET 应 405，实际 %d", rec.Code)
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

// TestPagesRender 四个页面都能 200 且含内联样式。
func TestPagesRender(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	for _, path := range []string{"/", "/memory", "/knowledge", "/activity", "/accounts"} {
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

// TestErrorsJSON 异常事件接口：结构 + 默认 5 条 + 截断。
func TestErrorsJSON(t *testing.T) {
	long := strings.Repeat("错", 400)
	repo := &fakeRepo{errors: []store.WebEvent{{
		ID: "live:1:9", Channel: "live", Dir: "in", Session: "1",
		FromName: "乙", Text: long, At: time.Now(), Status: "dead",
	}}}
	srv := newTestServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/errors", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var body struct {
		Items []map[string]any `json:"items"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Count != 1 {
		t.Fatalf("解析失败：%v / %+v", err, body)
	}
	for _, k := range []string{"id", "channel", "session", "from_name", "text", "at", "status"} {
		if _, ok := body.Items[0][k]; !ok {
			t.Errorf("缺少字段 %s", k)
		}
	}
	if body.Items[0]["status"] != "dead" {
		t.Errorf("status 不符：%v", body.Items[0]["status"])
	}
	if n := len([]rune(body.Items[0]["text"].(string))); n > maxTextRunes+1 {
		t.Errorf("正文未截断：%d", n)
	}
}

// TestMemoryImportanceTwoDecimals 重要度保留两位（数值 + 文本）。
func TestMemoryImportanceTwoDecimals(t *testing.T) {
	repo := &fakeRepo{memory: []store.WebMemory{{
		ID: "m1", Kind: "fact", Title: "t", Summary: "s", Importance: 0.6000000000000001,
		ValidFrom: time.Now(), CreatedAt: time.Now(),
	}}}
	srv := newTestServer(t, repo)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/memory", nil))

	var body struct {
		Items []struct {
			Importance     float64 `json:"importance"`
			ImportanceText string  `json:"importance_text"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Items[0].Importance != 0.6 {
		t.Errorf("数值未取两位：%v", body.Items[0].Importance)
	}
	if body.Items[0].ImportanceText != "0.60" {
		t.Errorf("文本格式应为 0.60，实际 %q", body.Items[0].ImportanceText)
	}
}

// TestOverviewPageBlocks 概览页包含新分区，记忆页用 toFixed(2)。
func TestOverviewPageBlocks(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, want := range []string{"运行状态", "数据统计", "向量入库", "最近动态", "最近错误", "/api/errors", "最近事件"} {
		if !strings.Contains(body, want) {
			t.Errorf("概览页缺少 %q", want)
		}
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/memory", nil))
	if !strings.Contains(rec.Body.String(), "toFixed(2)") {
		t.Error("记忆页未做两位小数格式化")
	}
}

// TestAccountsListMasksCookie 列表只返回脱敏凭据与状态字段。
func TestAccountsListMasksCookie(t *testing.T) {
	const raw = "SESSDATA=canary-sess; bili_jct=canary-csrf; buvid3=canary-buvid"
	fake := &fakeAccounts{rows: []account.Account{{
		ID: "account-1", MID: "42", Name: "小鱼", Cookie: raw, CSRF: "canary-csrf",
		Status: account.StatusInvalid, LastCheck: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
		Note: "备用", IsActive: true,
	}}}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, fake)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "canary") {
		t.Fatalf("响应泄漏凭据：%s", body)
	}
	for _, want := range []string{"cookie_masked", "SESSDATA=***", "bili_jct=***", "buvid3=***", "account-1", `"mid":"42"`, `"status":"invalid"`, `"is_active":true`, "2026-11-05T12:00:00Z"} {
		if !strings.Contains(body, want) {
			t.Errorf("响应缺少 %q：%s", want, body)
		}
	}
	var parsed struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil || parsed.Count != 1 {
		t.Errorf("列表结构不符：%v %+v", err, parsed)
	}
}

// TestAccountsAddRequiresConfirm 添加必须 confirm=yes，且响应不含 cookie 原值。
func TestAccountsAddRequiresConfirm(t *testing.T) {
	const raw = "SESSDATA=canary-sess; bili_jct=canary-csrf"
	fake := &fakeAccounts{}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, fake)
	payload := `{"cookie":"` + raw + `","note":"主号"}`

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(payload)))
	if rec.Code != http.StatusBadRequest || fake.addCalls != 0 {
		t.Fatalf("缺 confirm 应 400 且不落库：%d calls=%d", rec.Code, fake.addCalls)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts?confirm=yes", strings.NewReader("not json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实际 %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts?confirm=yes", strings.NewReader(`{"cookie":"  "}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空 cookie 应 400，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts?confirm=yes", strings.NewReader(payload)))
	if rec.Code != http.StatusCreated || fake.addCalls != 1 || fake.addCookie != raw || fake.addNote != "主号" {
		t.Fatalf("添加失败：%d calls=%d cookie=%q note=%q", rec.Code, fake.addCalls, fake.addCookie, fake.addNote)
	}
	body := rec.Body.String()
	if strings.Contains(body, "canary") || !strings.Contains(body, "SESSDATA=***") {
		t.Fatalf("添加响应凭据处理不符：%s", body)
	}
}

// TestAccountAddErrors 非法 cookie/验证失败映射为 400/502。
func TestAccountAddErrors(t *testing.T) {
	payload := `{"cookie":"SESSDATA=x; bili_jct=y"}`
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"格式非法", account.ErrInvalidCookie, http.StatusBadRequest},
		{"验证失败", account.ErrVerify, http.StatusBadGateway},
		{"其它错误", errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServerWithAccounts(t, &fakeRepo{}, &fakeAccounts{addErr: tc.err})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts?confirm=yes", strings.NewReader(payload)))
			if rec.Code != tc.want {
				t.Fatalf("状态码 = %d，期望 %d", rec.Code, tc.want)
			}
		})
	}
}

// TestAccountsActivateAndDelete 切换/删除写操作必须 confirm；活跃删除冲突 409。
func TestAccountsActivateAndDelete(t *testing.T) {
	fake := &fakeAccounts{}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, fake)
	id := "account-1"

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/accounts/"+id+"/activate", nil))
	if rec.Code != http.StatusBadRequest || len(fake.activated) != 0 {
		t.Fatalf("缺 confirm 应拒绝切换：%d %v", rec.Code, fake.activated)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/accounts/"+id+"/activate?confirm=yes", nil))
	if rec.Code != http.StatusOK || len(fake.activated) != 1 || fake.activated[0] != id {
		t.Fatalf("切换失败：%d %v", rec.Code, fake.activated)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/accounts/"+id+"/activate?confirm=yes", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("切换只允许 PUT，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/accounts/"+id, nil))
	if rec.Code != http.StatusBadRequest || len(fake.deleted) != 0 {
		t.Fatalf("缺 confirm 应拒绝删除：%d %v", rec.Code, fake.deleted)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/accounts/"+id+"?confirm=yes", nil))
	if rec.Code != http.StatusOK || len(fake.deleted) != 1 || fake.deleted[0] != id {
		t.Fatalf("删除失败：%d %v", rec.Code, fake.deleted)
	}

	for _, tc := range []struct {
		err  error
		want int
	}{{account.ErrActiveAccount, http.StatusConflict}, {account.ErrNotFound, http.StatusNotFound}} {
		srv = newTestServerWithAccounts(t, &fakeRepo{}, &fakeAccounts{deleteErr: tc.err})
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/accounts/"+id+"?confirm=yes", nil))
		if rec.Code != tc.want {
			t.Errorf("删除错误 %v 应 %d，实际 %d", tc.err, tc.want, rec.Code)
		}
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/accounts/"+id+"/bogus", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知操作应 404，实际 %d", rec.Code)
	}
}

// TestAccountsCheck 测试连接：成功 verified=true，失败仍返回最新账号状态。
func TestAccountsCheck(t *testing.T) {
	const raw = "SESSDATA=canary-sess; bili_jct=canary-csrf"
	fake := &fakeAccounts{checkResult: account.Account{
		ID: "account-1", MID: "42", Name: "小鱼", Cookie: raw, Status: account.StatusActive, IsActive: true,
	}}
	srv := newTestServerWithAccounts(t, &fakeRepo{}, fake)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts/account-1/check", nil))
	if rec.Code != http.StatusBadRequest || len(fake.checked) != 0 {
		t.Fatalf("缺 confirm 应拒绝测试：%d %v", rec.Code, fake.checked)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts/account-1/check?confirm=yes", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"verified":true`) {
		t.Fatalf("测试成功响应不符：%d %s", rec.Code, rec.Body.String())
	}

	fake.checkErr = account.ErrVerify
	fake.checkResult.Status = account.StatusInvalid
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts/account-1/check?confirm=yes", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"verified":false`) ||
		!strings.Contains(body, `"status":"invalid"`) || strings.Contains(body, "canary") {
		t.Fatalf("测试失败响应不符：%d %s", rec.Code, body)
	}

	fake.checkErr = account.ErrNotFound
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts/missing/check?confirm=yes", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404，实际 %d", rec.Code)
	}
}

// TestAccountsUnavailable 未装配账号服务时接口 503，但页面仍可访问。
func TestAccountsUnavailable(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/accounts", nil),
		httptest.NewRequest(http.MethodPost, "/api/accounts?confirm=yes", strings.NewReader(`{"cookie":"x"}`)),
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s 应 503，实际 %d", req.Method, req.URL.Path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("账号页应可访问，实际 %d", rec.Code)
	}
}

// TestAccountsPageBlocks 账号页必须包含关键区块与脱敏提示。
func TestAccountsPageBlocks(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"B站账号管理", "添加账号", "/api/accounts", "cookie_masked", "测试连接", "切换活跃", "删除", "明文保存在本机数据库"} {
		if !strings.Contains(body, want) {
			t.Errorf("账号页缺少 %q", want)
		}
	}
}

// TestEmbeddingPagesShowProgress 概览与知识页都必须展示入库进度、同步时间与 revision。
func TestEmbeddingPagesShowProgress(t *testing.T) {
	srv := newTestServer(t, &fakeRepo{})
	for _, path := range []string{"/", "/knowledge"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s 状态码 = %d", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, want := range []string{"向量入库", "/api/embedding/status", "待嵌入", "最后同步", "revision"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s 缺少 %q", path, want)
			}
		}
	}
}
