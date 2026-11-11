package web

// 面板「关闭 fishd」：POST /api/shutdown?confirm=yes（写接口：token/来源校验由 guard 统一做）。
// 返回 ok 后异步触发 shutdown（延迟一点让响应先回）。

import (
	"net/http"
	"time"
)

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if !confirmYes(w, r, "关闭 fishd") {
		return
	}
	if s.shutdown == nil {
		writeError(w, http.StatusServiceUnavailable, "关闭入口未装配")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "正在关闭 fishd"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		s.shutdown()
	}()
}
