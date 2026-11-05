package web

// 本文件是日志接口：SSE 实时 tail 与活动页的送礼/主动弹幕/点赞流水。
//
// 日志只从配置目录读取；内容已由 logview 做白名单与脱敏。

import (
	"encoding/json"
	"fmt"
	"net/http"

	"RaiseMyFish/internal/logview"
	"go.uber.org/zap"
)

// handleLogsStream 用 SSE 从当前日志末尾持续推送新行。
func (s *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if s.logs == nil {
		writeError(w, http.StatusServiceUnavailable, "日志读取未启用")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前连接不支持流式输出")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "event: ready\ndata: {}\n\n"); err != nil {
		return
	}
	flusher.Flush()

	err := s.logs.Tail(r.Context(), func(entry logview.Entry) error {
		data, err := json.Marshal(entry)
		if err != nil {
			return nil
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		s.log.Debug("日志流已结束", zap.String("action", "web_logs_stream"), zap.Error(err))
	}
}

// handleGiftLog 返回送礼相关日志（工具副作用 + 结果未知待核对）。
func (s *Server) handleGiftLog(w http.ResponseWriter, r *http.Request) {
	s.handleLogQuery(w, r, logview.Query{
		Actions: []string{"tool_side_effect", "gift_pending_review"},
		// tool_side_effect 里只取 tool=gift；gift_pending_review 没有 tool 字段，用空组放行。
		Matches: []map[string]string{{"tool": "gift"}, {}},
	})
}

// handleProactiveLog 返回主动弹幕日志。
func (s *Server) handleProactiveLog(w http.ResponseWriter, r *http.Request) {
	s.handleLogQuery(w, r, logview.Query{Actions: []string{"proactive", "proactive_react"}})
}

// handleLikeLog 返回常驻点赞日志。
func (s *Server) handleLikeLog(w http.ResponseWriter, r *http.Request) {
	s.handleLogQuery(w, r, logview.Query{Actions: []string{"heartbeat_like"}})
}

func (s *Server) handleLogQuery(w http.ResponseWriter, r *http.Request, q logview.Query) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if s.logs == nil {
		writeError(w, http.StatusServiceUnavailable, "日志读取未启用")
		return
	}
	q.Limit = limitParam(r, defaultLimit)
	entries, err := s.logs.Recent(q)
	if err != nil {
		s.fail(w, "读取日志失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries, "count": len(entries)})
}
