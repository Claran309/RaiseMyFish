package web

// 看板注入接口：GET 列表、POST 新建、PUT 编辑、DELETE 删除。
// 写操作与其它面板动作一致，要求同源/令牌 + confirm=yes。

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

const (
	// defaultInjectionTTL 是未指定有效期时的默认值（1 小时）。
	defaultInjectionTTL = time.Hour
	// maxInjectionTTL 防止写入超长有效期（30 天）。
	maxInjectionTTL = 30 * 24 * time.Hour
)

type injectionRequest struct {
	Content string `json:"content"`
	// TTLSeconds 为 nil 表示未指定（默认 1 小时），0 表示永久。
	TTLSeconds *int `json:"ttl_seconds"`
}

// parseExpiry 把请求里的有效期换算成绝对时间；nil 表示永久。
func (s *Server) parseExpiry(ttl *int) (*time.Time, error) {
	if ttl == nil {
		expires := s.now().Add(defaultInjectionTTL)
		return &expires, nil
	}
	if *ttl == 0 {
		return nil, nil
	}
	d := time.Duration(*ttl) * time.Second
	if d < time.Minute || d > maxInjectionTTL {
		return nil, errors.New("有效期需为 0（永久）或 60 秒至 30 天")
	}
	expires := s.now().Add(d)
	return &expires, nil
}

// injectionView 是对外展示结构；不包含任何凭据。
func injectionView(row store.WebInjection, now time.Time) map[string]any {
	created := row.CreatedAt.UTC().Format(time.RFC3339)
	updated := row.UpdatedAt.UTC().Format(time.RFC3339)
	expires := ""
	remaining := int64(0)
	permanent := row.ExpiresAt == nil
	if row.ExpiresAt != nil {
		expires = row.ExpiresAt.UTC().Format(time.RFC3339)
		if left := row.ExpiresAt.Sub(now); left > 0 {
			remaining = int64(left.Seconds())
		}
	}
	return map[string]any{
		"id":                row.ID,
		"content":           row.Content,
		"created_at":        created,
		"updated_at":        updated,
		"expires_at":        expires,
		"permanent":         permanent,
		"remaining_seconds": remaining,
	}
}

// handleInject 处理 /api/inject：GET 列出有效注入，POST 新建。
func (s *Server) handleInject(w http.ResponseWriter, r *http.Request) {
	if s.injections == nil {
		writeError(w, http.StatusServiceUnavailable, "注入功能未启用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listInjections(w, r)
	case http.MethodPost:
		s.createInjection(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET/POST")
	}
}

func (s *Server) listInjections(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	rows, err := s.injections.ActiveInjections(r.Context(), now)
	if err != nil {
		s.fail(w, "读取看板注入失败", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, injectionView(row, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) createInjection(w http.ResponseWriter, r *http.Request) {
	if !confirmYes(w, r, "新建注入") {
		return
	}
	var req injectionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	expiresAt, err := s.parseExpiry(req.TTLSeconds)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := s.injections.CreateInjection(r.Context(), req.Content, expiresAt)
	if err != nil {
		s.injectionFailure(w, "新建注入失败", err)
		return
	}
	s.log.Info("面板新建注入",
		zap.String("action", "web_inject_create"),
		zap.Int64("id", row.ID),
		zap.Bool("permanent", row.ExpiresAt == nil),
	)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "item": injectionView(row, s.now())})
}

// handleInjectItem 处理 /api/inject/{id} 的编辑与删除。
func (s *Server) handleInjectItem(w http.ResponseWriter, r *http.Request) {
	if s.injections == nil {
		writeError(w, http.StatusServiceUnavailable, "注入功能未启用")
		return
	}
	id, err := parsePositiveID(strings.TrimPrefix(r.URL.Path, "/api/inject/"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "缺少注入 id")
		return
	}
	if !confirmYes(w, r, "修改注入") {
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req injectionRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		expiresAt, err := s.parseExpiry(req.TTLSeconds)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		row, err := s.injections.UpdateInjection(r.Context(), id, req.Content, expiresAt)
		if err != nil {
			s.injectionFailure(w, "更新注入失败", err)
			return
		}
		s.log.Info("面板更新注入", zap.String("action", "web_inject_update"), zap.Int64("id", row.ID))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": injectionView(row, s.now())})
	case http.MethodDelete:
		if err := s.injections.DeleteInjection(r.Context(), id); err != nil {
			s.injectionFailure(w, "删除注入失败", err)
			return
		}
		s.log.Info("面板删除注入", zap.String("action", "web_inject_delete"), zap.Int64("id", id))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
	default:
		writeError(w, http.StatusMethodNotAllowed, "只支持 PUT/DELETE")
	}
}

func (s *Server) injectionFailure(w http.ResponseWriter, message string, err error) {
	switch {
	case errors.Is(err, store.ErrInjectionInvalid):
		writeError(w, http.StatusBadRequest, "注入内容需为 1 至 500 字且不含控制字符")
	case errors.Is(err, store.ErrWebNotFound):
		writeError(w, http.StatusNotFound, "注入不存在或已删除")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "注入参数不合法")
	default:
		s.fail(w, message, err)
	}
}

// parsePositiveID 只接受十进制正整数，拒绝带路径的输入。
func parsePositiveID(raw string) (int64, error) {
	raw = strings.TrimSpace(strings.Trim(raw, "/"))
	if raw == "" || strings.Contains(raw, "/") {
		return 0, errors.New("缺少 id")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("id 不合法")
	}
	return id, nil
}
