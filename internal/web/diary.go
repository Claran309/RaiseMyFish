package web

// 日报接口：列表、详情、Markdown 导出与手动生成。
// 生成会调用模型，写操作与其它面板动作一致，要求同源/令牌 + confirm=yes。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"RaiseMyFish/internal/diary"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// DiaryGenerator 是面板手动生成日报依赖的最小能力（cmd/fishd 的 digest 实现）。
type DiaryGenerator interface {
	Generate(ctx context.Context, day string) (store.DailySummary, error)
}

// handleDiary 处理 /api/diary：GET 列出最近日报。
func (s *Server) handleDiary(w http.ResponseWriter, r *http.Request) {
	if s.diary == nil {
		writeError(w, http.StatusServiceUnavailable, "日报功能未启用")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	rows, err := s.diary.DailySummaries(r.Context(), limitParam(r, 30))
	if err != nil {
		s.diaryFailure(w, "读取日报列表失败", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"day":         row.Day,
			"preview":     clip(row.Content),
			"model":       row.Model,
			"event_count": row.EventCount,
			"created_at":  formatDiaryTime(row.CreatedAt),
			"updated_at":  formatDiaryTime(row.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// handleDiaryItem 处理 /api/diary/{day}、/api/diary/{day}/export 与 /api/diary/generate。
func (s *Server) handleDiaryItem(w http.ResponseWriter, r *http.Request) {
	if s.diary == nil {
		writeError(w, http.StatusServiceUnavailable, "日报功能未启用")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/diary/")
	if path == "generate" {
		s.generateDiary(w, r)
		return
	}
	if strings.HasSuffix(path, "/export") {
		s.exportDiary(w, r, strings.TrimSuffix(path, "/export"))
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	summary, ok := s.loadDiary(w, r, strings.Trim(path, "/"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, diaryDetailView(summary))
}

// generateDiary 手动生成（或覆盖）指定日期日报；缺省 day 为东八区昨天。
func (s *Server) generateDiary(w http.ResponseWriter, r *http.Request) {
	if s.diaryGen == nil {
		writeError(w, http.StatusServiceUnavailable, "日报生成未启用")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	if !confirmYes(w, r, "生成日报") {
		return
	}
	day := strings.TrimSpace(r.URL.Query().Get("day"))
	if day == "" {
		day = diary.Yesterday(s.now())
	}
	if _, err := diary.ParseDay(day); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	summary, err := s.diaryGen.Generate(r.Context(), day)
	if err != nil {
		switch {
		case errors.Is(err, diary.ErrNoEvents):
			writeError(w, http.StatusBadRequest, "当天没有可总结的事件")
		case errors.Is(err, store.ErrInvalid):
			writeError(w, http.StatusBadRequest, "日报参数不合法")
		default:
			s.fail(w, "生成日报失败", err)
		}
		return
	}
	s.log.Info("面板生成日报",
		zap.String("action", "web_diary_generate"),
		zap.String("day", summary.Day),
		zap.Int("events", summary.EventCount),
		zap.String("model", summary.Model),
	)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": diaryDetailView(summary)})
}

// exportDiary 返回 Markdown 附件；文件名只由校验过的日期构成。
func (s *Server) exportDiary(w http.ResponseWriter, r *http.Request, day string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	summary, ok := s.loadDiary(w, r, day)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "diary-"+summary.Day+".md"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = fmt.Fprintf(w, "# 日报 %s\n\n%s\n", summary.Day, summary.Content)
}

// loadDiary 校验日期并读取日报；失败时已写响应，ok=false。
func (s *Server) loadDiary(w http.ResponseWriter, r *http.Request, day string) (store.DailySummary, bool) {
	if _, err := diary.ParseDay(day); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return store.DailySummary{}, false
	}
	summary, err := s.diary.DailySummary(r.Context(), strings.TrimSpace(day))
	if err != nil {
		s.diaryFailure(w, "读取日报失败", err)
		return store.DailySummary{}, false
	}
	return summary, true
}

func (s *Server) diaryFailure(w http.ResponseWriter, message string, err error) {
	switch {
	case errors.Is(err, store.ErrDiaryNotFound):
		writeError(w, http.StatusNotFound, "该日期还没有日报")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, "日报参数不合法")
	default:
		s.fail(w, message, err)
	}
}

func diaryDetailView(summary store.DailySummary) map[string]any {
	return map[string]any{
		"day":         summary.Day,
		"content":     summary.Content,
		"model":       summary.Model,
		"event_count": summary.EventCount,
		"created_at":  formatDiaryTime(summary.CreatedAt),
		"updated_at":  formatDiaryTime(summary.UpdatedAt),
	}
}

func formatDiaryTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
