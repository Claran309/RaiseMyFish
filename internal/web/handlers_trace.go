package web

import (
	"net/http"
	"strconv"
	"strings"
)

// handleTracesRecent 返回最近 trace；?sort=slow 按耗时倒序。
func (s *Server) handleTracesRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if s.trace == nil {
		writeError(w, http.StatusServiceUnavailable, "本地trace未启用")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "limit 需在1至200")
			return
		}
		limit = n
	}
	slow := r.URL.Query().Get("sort") == "slow"
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "traces": s.trace.Recent(limit, slow)})
}

// handleTraceDetail 返回单条 trace 的 span 列表。
func (s *Server) handleTraceDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if s.trace == nil {
		writeError(w, http.StatusServiceUnavailable, "本地trace未启用")
		return
	}
	id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/traces/"))
	if id == "" || strings.ContainsAny(id, "/\r\n") {
		writeError(w, http.StatusBadRequest, "trace id 无效")
		return
	}
	detail := s.trace.Get(id)
	if len(detail.Spans) == 0 {
		writeError(w, http.StatusNotFound, "trace 不存在")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
