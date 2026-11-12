package web

// 面板手动新增记忆：POST /api/memory?confirm=yes（写接口，token/来源校验由 guard 统一做）。
// 手动记忆是「人写的」：Source=manual、DerivedBy 留空、不做模型推断；
// kind/subject 走 store.MemoryRecord.Validate 的白名单，非法输入直接 400。

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"

	"RaiseMyFish/internal/store"
)

// MemoryWriter 是手动记忆的最小写入口（database.MemoryRepo 满足）。
type MemoryWriter interface {
	InsertRecord(ctx context.Context, m store.MemoryRecord) (bool, error)
}

type manualMemoryRequest struct {
	Kind       string  `json:"kind"`
	Subject    string  `json:"subject"`
	Title      string  `json:"title"`
	Summary    string  `json:"summary"`
	Body       string  `json:"body"`
	Importance float64 `json:"importance"`
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	if s.memoryWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "手动记忆未启用")
		return
	}
	if !confirmYes(w, r, "新增记忆") {
		return
	}
	var req manualMemoryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	summary := strings.TrimSpace(req.Summary)
	kind := strings.TrimSpace(req.Kind)
	subject := strings.TrimSpace(req.Subject)
	if title == "" || summary == "" {
		writeError(w, http.StatusBadRequest, "标题和摘要不能为空")
		return
	}
	if utf8.RuneCountInString(title) > 200 || utf8.RuneCountInString(summary) > 500 || utf8.RuneCountInString(req.Body) > 4000 {
		writeError(w, http.StatusBadRequest, "内容过长（标题≤200/摘要≤500/正文≤4000）")
		return
	}
	importance := req.Importance
	if importance <= 0 || importance > 1 {
		importance = 0.8
	}
	rec := store.MemoryRecord{
		Source:  "manual",
		Kind:    kind,
		Title:   title,
		Body:    strings.TrimSpace(req.Body),
		Subject: subject,
	}
	rec.ID = "mem_manual_" + strconv.FormatInt(s.now().UnixNano(), 36)
	rec.Summary = summary
	rec.Importance = importance
	rec.Confidence = 1
	rec.ValidFrom = s.now().UTC()
	if err := rec.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "记忆字段不合法："+err.Error())
		return
	}
	if _, err := s.memoryWriter.InsertRecord(r.Context(), rec); err != nil {
		s.fail(w, "写入记忆失败", err)
		return
	}
	s.log.Info("面板手动新增记忆",
		zap.String("action", "web_memory_create"),
		zap.String("kind", rec.Kind),
		zap.String("subject", rec.Subject),
	)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}
