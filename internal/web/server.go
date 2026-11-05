package web

// Package web 是运维面板：概览 / 记忆 / 活动流水。
//
// 边界：
//   - 只读为主，唯一的写操作是「归档一条记忆」，且必须带 confirm=yes；
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
}

// Server 是面板本体。
type Server struct {
	addr          string
	repo          store.WebRepo
	log           *zap.Logger
	now           func() time.Time
	knowledgeInfo KnowledgeInfo
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
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Server{addr: addr, repo: o.Repo, log: o.Log, now: now, knowledgeInfo: o.KnowledgeInfo}, nil
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
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/embedding/status", s.handleEmbeddingStatus)
	mux.HandleFunc("/api/memory", s.handleMemory)
	mux.HandleFunc("/api/memory/", s.handleMemoryDelete)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/errors", s.handleErrors)
	mux.HandleFunc("/api/send_log", s.handleSendLog)
	mux.HandleFunc("/", s.handlePage)
	return mux
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
	default:
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticFS, name)
	if err != nil {
		http.Error(w, "页面缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
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
