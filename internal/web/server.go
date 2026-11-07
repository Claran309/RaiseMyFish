package web

// Package web 是运维面板：概览 / 记忆 / 活动流水 / B站账号。
//
// 边界：
//   - 写操作（归档记忆、账号增删切换/测试）必须带 confirm=yes；
//   - 账号接口只返回 cookie_masked，绝不回显 cookie/csrf 原值；
//   - 只监听回环地址（127.0.0.1 / ::1 / localhost），拒绝 0.0.0.0；
//   - 列表统一截断正文，响应里不出现任何凭据/完整 prompt。

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

//go:embed static/*.html
var staticFS embed.FS

const (
	// maxTextRunes 是列表里单条正文的展示上限。
	maxTextRunes = 200
	// defaultLimit 是未指定 limit 时的条数。
	defaultLimit = 50
)

// KnowledgeInfo 返回知识库快照的条目数与 revision；知识未启用时可为 nil。
type KnowledgeInfo func() (entries int, revision string)

// AccountService 是面板账号管理依赖（*account.Service 实现）。
type AccountService interface {
	Load(ctx context.Context) ([]account.Account, error)
	Add(ctx context.Context, cookie, note string) (account.Account, error)
	Activate(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	Check(ctx context.Context, id string) (account.Account, error)
}

// Options 是面板参数。
type Options struct {
	// Addr 形如 127.0.0.1:8090；只允许回环。
	Addr string
	// Repo 是数据访问口。
	Repo store.WebRepo
	// Log 必须非 nil。
	Log *zap.Logger
	// Now 可注入时钟（测试用）。
	Now func() time.Time
	// KnowledgeInfo 可选：注入当前知识快照的条目数与 revision。
	KnowledgeInfo KnowledgeInfo
	// Accounts 可选：账号管理服务；nil 时 /accounts 接口返回 503。
	Accounts AccountService
	// Control 可选：运行时控制面；nil 时控制类接口返回 503。
	Control RuntimeController
	// Logs 可选：日志读取口；nil 时日志接口返回 503。
	Logs LogSource
	// Token 可选：写接口的本地令牌；为空时每次启动随机生成。
	Token string
}

// Server 是面板本体。
type Server struct {
	addr          string
	repo          store.WebRepo
	log           *zap.Logger
	now           func() time.Time
	knowledgeInfo KnowledgeInfo
	accounts      AccountService
	control       RuntimeController
	logs          LogSource
	// token 是本地写接口令牌（O04）：页面从 HTML 读、CLI 从 /api/token 取。
	token string
	// port 是面板端口，Host/Origin 校验必须与它一致。
	port string
}

// New 校验地址与依赖；非回环地址直接拒绝。
func New(o Options) (*Server, error) {
	if o.Repo == nil {
		return nil, errors.New("面板数据源不能为 nil")
	}
	if o.Log == nil {
		return nil, errors.New("日志器不能为 nil")
	}
	addr := strings.TrimSpace(o.Addr)
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	if err := ValidateAddr(addr); err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errors.New("面板地址必须形如 127.0.0.1:8090")
	}
	token := strings.TrimSpace(o.Token)
	if token == "" {
		token, err = newPanelToken()
		if err != nil {
			return nil, err
		}
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Server{
		addr: addr, repo: o.Repo, log: o.Log, now: now,
		knowledgeInfo: o.KnowledgeInfo, accounts: o.Accounts, control: o.Control, logs: o.Logs,
		token: token, port: port,
	}, nil
}

// ValidateAddr 只允许回环监听，避免把运维面板暴露到公网。
func ValidateAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("面板地址必须形如 127.0.0.1:8090")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return errors.New("面板端口必须是数字")
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return errors.New("面板地址缺少主机（禁止监听全部网卡）")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("面板只允许监听回环地址（127.0.0.1 或 ::1）")
	}
	return nil
}

// Handler 返回挂好路由的 http.Handler（测试直接用它）。
//
// 所有 /api/* 都套 guardWrite：读请求直接放行，写请求必须过 Host/来源/Content-Type 校验。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/token", s.handleToken)
	mux.HandleFunc("/api/status", s.guardWrite(s.handleStatus))
	mux.HandleFunc("/api/embedding/status", s.guardWrite(s.handleEmbeddingStatus))
	mux.HandleFunc("/api/memory", s.guardWrite(s.handleMemory))
	mux.HandleFunc("/api/memory/", s.guardWrite(s.handleMemoryDelete))
	mux.HandleFunc("/api/events", s.guardWrite(s.handleEvents))
	mux.HandleFunc("/api/events/", s.guardWrite(s.handleEventDetail))
	mux.HandleFunc("/api/comment/recent", s.guardWrite(s.handleCommentRecent))
	mux.HandleFunc("/api/dm/recent", s.guardWrite(s.handleDMRecent))
	mux.HandleFunc("/api/errors", s.guardWrite(s.handleErrors))
	mux.HandleFunc("/api/send_log", s.guardWrite(s.handleSendLog))
	mux.HandleFunc("/api/accounts", s.guardWrite(s.handleAccounts))
	mux.HandleFunc("/api/accounts/", s.guardWrite(s.handleAccountAction))
	mux.HandleFunc("/api/live", s.guardWrite(s.handleLive))
	mux.HandleFunc("/api/message", s.guardWrite(s.handleMessage))
	mux.HandleFunc("/api/config", s.guardWrite(s.handleConfig))
	mux.HandleFunc("/api/watchdog", s.guardWrite(s.handleWatchdog))
	mux.HandleFunc("/api/logs/stream", s.guardWrite(s.handleLogsStream))
	mux.HandleFunc("/api/gift/log", s.guardWrite(s.handleGiftLog))
	mux.HandleFunc("/api/gift/quota", s.guardWrite(s.handleGiftQuota))
	mux.HandleFunc("/api/proactive/log", s.guardWrite(s.handleProactiveLog))
	mux.HandleFunc("/api/like/log", s.guardWrite(s.handleLikeLog))
	mux.HandleFunc("/", s.handlePage)
	return mux
}

// handleToken 返回面板令牌，供本地 CLI 取用（GET /api/token）。
//
// 跨站页面拿不到响应体：这里不返回 CORS 头，浏览器的同源策略会拦住读取。
// 因此「任何网页都能请求到」不等于「任何网页都能读到令牌」。
func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": s.token})
}

// Run 启动服务并在 ctx 取消时优雅关闭。
//
//nolint:contextcheck // 关闭超时必须用独立 context，不能继承已取消的父 ctx
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	s.log.Info("Web 面板已启动", zap.String("action", "web_start"), zap.String("addr", s.addr))
	select {
	case <-ctx.Done():
		// 关闭时必须脱离已取消的父 ctx，否则 Shutdown 立刻返回
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:contextcheck
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errc:
		return fmt.Errorf("面板服务异常：%w", err)
	}
}

// handlePage 输出内嵌静态页。
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	var name string
	switch r.URL.Path {
	case "/", "/index.html":
		name = "static/index.html"
	case "/memory":
		name = "static/memory.html"
	case "/knowledge":
		name = "static/knowledge.html"
	case "/activity":
		name = "static/activity.html"
	case "/accounts":
		name = "static/accounts.html"
	case "/live":
		name = "static/live.html"
	case "/message":
		name = "static/message.html"
	case "/config":
		name = "static/config.html"
	case "/logs":
		name = "static/logs.html"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticFS, name)
	if err != nil {
		http.Error(w, "页面缺失", http.StatusInternalServerError)
		return
	}
	// 把令牌注入页面：页面里的写操作要带 X-Panel-Token，跨站页面读不到这份 HTML。
	body := strings.ReplaceAll(string(data), pageTokenPlaceholder, s.token)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(body))
}

// handleStatus 返回概览数字。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	st, err := s.repo.Status(r.Context())
	if err != nil {
		s.fail(w, "读取概览失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events_total":   st.EventsTotal,
		"events_pending": st.EventsPending,
		"memory_total":   st.MemoryTotal,
		"memory_by_kind": st.MemoryByKind,
		"send_total":     st.SendTotal,
		"send_failed":    st.SendFailed,
		"cost_usd":       st.CostUSD,
		"generated_at":   st.GeneratedAt.UTC().Format(time.RFC3339),
	})
}

// handleEmbeddingStatus 返回知识/记忆/档案的向量入库进度。
//
// 知识条目总数来自文件快照（数据库只知道已入库的向量），因此在这里用
// KnowledgeInfo 覆盖 repo 给出的近似值；知识未启用时 knowledgeInfo 为 nil，
// 此时总数按已入库数量展示，待嵌入为 0。
func (s *Server) handleEmbeddingStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	st, err := s.repo.EmbeddingStatus(r.Context())
	if err != nil {
		s.fail(w, "读取向量入库状态失败", err)
		return
	}
	if s.knowledgeInfo != nil {
		entries, revision := s.knowledgeInfo()
		st.Knowledge.Total = int64(entries)
		st.Knowledge.Revision = revision
		st.Knowledge.Pending = st.Knowledge.Total - st.Knowledge.Embedded
	}
	if st.Knowledge.Pending < 0 {
		st.Knowledge.Pending = 0
	}
	if st.GeneratedAt.IsZero() {
		st.GeneratedAt = s.now()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"knowledge":    embeddingSectionJSON(st.Knowledge),
		"memory":       embeddingSectionJSON(st.Memory),
		"viewer":       embeddingSectionJSON(st.Viewer),
		"generated_at": st.GeneratedAt.UTC().Format(time.RFC3339),
	})
}

// embeddingSectionJSON 统一字段；最后同步时间未知时返回空字符串。
func embeddingSectionJSON(s store.WebEmbeddingSection) map[string]any {
	last := ""
	if !s.LastUpdated.IsZero() {
		last = s.LastUpdated.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"total":        s.Total,
		"embedded":     s.Embedded,
		"pending":      s.Pending,
		"last_updated": last,
		"revision":     s.Revision,
	}
}

// handleMemory 列出记忆。
func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	limit := limitParam(r, defaultLimit)
	rows, err := s.repo.Memory(r.Context(), r.URL.Query().Get("kind"), r.URL.Query().Get("q"), limit)
	if err != nil {
		s.fail(w, "读取记忆失败", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, m := range rows {
		items = append(items, map[string]any{
			"id":              m.ID,
			"kind":            m.Kind,
			"title":           clip(m.Title),
			"summary":         clip(m.Summary),
			"importance":      math.Round(m.Importance*100) / 100,
			"importance_text": strconv.FormatFloat(m.Importance, 'f', 2, 64),
			"valid_from":      m.ValidFrom.UTC().Format(time.RFC3339),
			"created_at":      m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// handleMemoryDelete 处理 DELETE /api/memory/{id}?confirm=yes。
func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "只支持 DELETE")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/memory/")
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "缺少记忆 id")
		return
	}
	if r.URL.Query().Get("confirm") != "yes" {
		writeError(w, http.StatusBadRequest, "删除需要 confirm=yes")
		return
	}
	if err := s.repo.ArchiveMemory(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrWebNotFound) {
			writeError(w, http.StatusNotFound, "记忆不存在或已删除")
			return
		}
		s.fail(w, "删除记忆失败", err)
		return
	}
	s.log.Info("面板删除记忆", zap.String("action", "web_memory_delete"), zap.String("id", id))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleAccounts 处理账号列表与添加。
func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listAccounts(w, r)
	case http.MethodPost:
		s.addAccount(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET/POST")
	}
}

// listAccounts 只返回脱敏凭据，绝不回显 cookie/csrf 原值。
func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	rows, err := s.accounts.Load(r.Context())
	if err != nil {
		s.fail(w, "读取账号列表失败", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, accountView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// addAccount 接收粘贴的 cookie，验证身份后落库；写操作必须 confirm=yes。
func (s *Server) addAccount(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	if !confirmYes(w, r, "添加账号") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		Cookie string `json:"cookie"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是 JSON（cookie/note）")
		return
	}
	if strings.TrimSpace(req.Cookie) == "" {
		writeError(w, http.StatusBadRequest, "缺少 cookie")
		return
	}
	stored, err := s.accounts.Add(r.Context(), req.Cookie, req.Note)
	if err != nil {
		s.accountFailure(w, "添加账号失败", err)
		return
	}
	s.log.Info("面板添加B站账号",
		zap.String("action", "web_account_add"),
		zap.String("id", stored.ID),
		zap.String("mid", stored.MID),
		zap.Bool("active", stored.IsActive),
	)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "account": accountView(stored)})
}

// handleAccountAction 处理 /api/accounts/{id}[/activate|/check]。
func (s *Server) handleAccountAction(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "账号服务未启用")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/accounts/"), "/")
	parts := strings.Split(rest, "/")
	id := strings.TrimSpace(parts[0])
	if id == "" || len(id) > 64 {
		writeError(w, http.StatusNotFound, "缺少账号 id")
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "只支持 DELETE")
			return
		}
		s.deleteAccount(w, r, id)
		return
	}
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "未知的账号操作")
		return
	}
	switch parts[1] {
	case "activate":
		if r.Method != http.MethodPut {
			writeError(w, http.StatusMethodNotAllowed, "只支持 PUT")
			return
		}
		s.activateAccount(w, r, id)
	case "check":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
			return
		}
		s.checkAccount(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "未知的账号操作")
	}
}

func (s *Server) activateAccount(w http.ResponseWriter, r *http.Request, id string) {
	if !confirmYes(w, r, "切换活跃账号") {
		return
	}
	if err := s.accounts.Activate(r.Context(), id); err != nil {
		s.accountFailure(w, "切换活跃账号失败", err)
		return
	}
	s.log.Info("面板切换B站活跃账号", zap.String("action", "web_account_activate"), zap.String("id", id))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "active": true})
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, id string) {
	if !confirmYes(w, r, "删除账号") {
		return
	}
	if err := s.accounts.Delete(r.Context(), id); err != nil {
		s.accountFailure(w, "删除账号失败", err)
		return
	}
	s.log.Info("面板删除B站账号", zap.String("action", "web_account_delete"), zap.String("id", id))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// checkAccount 重新验证凭据；验证失败也返回 200 + verified=false，
// 让前端能看到最新状态（invalid 或保持原状态），而不是只能看到一个错误。
func (s *Server) checkAccount(w http.ResponseWriter, r *http.Request, id string) {
	if !confirmYes(w, r, "测试连接") {
		return
	}
	stored, err := s.accounts.Check(r.Context(), id)
	if err != nil {
		if errors.Is(err, account.ErrVerify) {
			s.log.Info("面板账号测试未通过",
				zap.String("action", "web_account_check"),
				zap.String("id", stored.ID),
				zap.String("status", stored.Status),
			)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "verified": false, "account": accountView(stored),
				"message": "验证失败，账号状态已更新",
			})
			return
		}
		s.accountFailure(w, "测试连接失败", err)
		return
	}
	s.log.Info("面板账号测试通过",
		zap.String("action", "web_account_check"),
		zap.String("id", stored.ID),
		zap.String("status", stored.Status),
	)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "verified": true, "account": accountView(stored)})
}

// accountFailure 把账号领域错误映射成合适的 HTTP 状态；不输出凭据。
func (s *Server) accountFailure(w http.ResponseWriter, message string, err error) {
	switch {
	case errors.Is(err, account.ErrInvalidCookie):
		writeError(w, http.StatusBadRequest, "cookie 格式无效（需包含 SESSDATA）")
	case errors.Is(err, account.ErrVerify):
		writeError(w, http.StatusBadGateway, "B站账号验证失败，请检查 cookie 是否过期")
	case errors.Is(err, account.ErrNotFound):
		writeError(w, http.StatusNotFound, "账号不存在")
	case errors.Is(err, account.ErrActiveAccount):
		writeError(w, http.StatusConflict, "不能删除当前活跃账号，请先切换")
	case errors.Is(err, account.ErrSwitchInProgress):
		writeError(w, http.StatusConflict, "账号切换正在进行，请稍后重试")
	default:
		s.fail(w, message, err)
	}
}

// accountView 是账号的对外展示结构：只含脱敏凭据。
func accountView(a account.Account) map[string]any {
	last := ""
	if !a.LastCheck.IsZero() {
		last = a.LastCheck.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"id":            a.ID,
		"mid":           a.MID,
		"name":          a.Name,
		"status":        a.Status,
		"last_check":    last,
		"note":          a.Note,
		"is_active":     a.IsActive,
		"cookie_masked": account.MaskCookie(a.Cookie),
	}
}

func confirmYes(w http.ResponseWriter, r *http.Request, action string) bool {
	if r.URL.Query().Get("confirm") != "yes" {
		writeError(w, http.StatusBadRequest, action+"需要 confirm=yes")
		return false
	}
	return true
}

// handleEvents 返回活动流水。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	limit := limitParam(r, defaultLimit)
	rows, err := s.repo.Events(r.Context(), r.URL.Query().Get("dir"), limit)
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "参数不合法（dir 只支持 in/out）")
			return
		}
		s.fail(w, "读取活动流水失败", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		items = append(items, map[string]any{
			"id":        e.ID,
			"channel":   e.Channel,
			"dir":       e.Dir,
			"session":   e.Session,
			"from_name": clip(e.FromName),
			"text":      clip(e.Text),
			"at":        e.At.UTC().Format(time.RFC3339),
			"status":    e.Status,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// handleErrors 返回最近异常事件（dead/retry）。
func (s *Server) handleErrors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	limit := limitParam(r, 5)
	if limit > 50 {
		limit = 50
	}
	rows, err := s.repo.Errors(r.Context(), limit)
	if err != nil {
		s.fail(w, "读取异常事件失败", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		items = append(items, map[string]any{
			"id":        e.ID,
			"channel":   e.Channel,
			"session":   clip(e.Session),
			"from_name": clip(e.FromName),
			"text":      clip(e.Text),
			"at":        e.At.UTC().Format(time.RFC3339),
			"status":    e.Status,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// handleSendLog 返回发送记录。
func (s *Server) handleSendLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	limit := limitParam(r, defaultLimit)
	rows, err := s.repo.SendLogs(r.Context(), limit)
	if err != nil {
		s.fail(w, "读取发送记录失败", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		items = append(items, map[string]any{
			"event_id": e.EventID,
			"channel":  e.Channel,
			"ok":       e.OK,
			"msg_id":   clip(e.MsgID),
			"at":       e.At.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// fail 统一错误出口：日志记详情，响应只给简短文案。
func (s *Server) fail(w http.ResponseWriter, message string, err error) {
	s.log.Warn("面板请求失败", zap.String("action", "web_error"), zap.String("reason", message), zap.Error(err))
	writeError(w, http.StatusInternalServerError, message)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// limitParam 解析 limit，越界夹到 1..200。
func limitParam(r *http.Request, fallback int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return fallback
	}
	if n > 200 {
		return 200
	}
	return n
}

// clip 截断正文，避免列表里出现整段长文本（也可能是敏感内容）。
func clip(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxTextRunes {
		return s
	}
	return string([]rune(s)[:maxTextRunes]) + "…"
}
