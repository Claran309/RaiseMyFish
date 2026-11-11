package web

// 本文件是观众侧写的面板接口：状态查询 + 暂停/恢复/重建/撤回。
//
// 状态里只给计数与时间，不返回侧写正文、来源事件 ID 或私密内容；
// 写操作沿用面板约定：confirm=yes + 同源/令牌校验，错误文案不含凭据。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// ViewerSummaryControl 是观众侧写的面板控制面；由 cmd/fishd 运行时控制面实现。
type ViewerSummaryControl interface {
	// ViewerSummaryStatus 汇总运行状态与落库计数。
	ViewerSummaryStatus(ctx context.Context) ViewerSummaryState
	// SetViewerSummaryPaused 暂停/恢复后台侧写调度（不清理已有侧写）。
	SetViewerSummaryPaused(paused bool) error
	// TriggerViewerBackfill 排队一次首次侧写回填，最多 limit 人。
	TriggerViewerBackfill(ctx context.Context, limit int) error
	// RebuildViewerSummary 把一位观众标记为下一轮重建。
	RebuildViewerSummary(ctx context.Context, uid string) error
	// RevokeViewerSummary 立即撤回一位观众的侧写并安排知识清理。
	RevokeViewerSummary(ctx context.Context, uid string) error
}

// ViewerSummaryRun 是最近一轮侧写的统计投影。
type ViewerSummaryRun struct {
	Candidates       int `json:"candidates"`
	Summarized       int `json:"summarized"`
	Fallback         int `json:"fallback"`
	Retained         int `json:"retained"`
	Revoked          int `json:"revoked"`
	Expired          int `json:"expired"`
	Skipped          int `json:"skipped"`
	BusySkipped      int `json:"busy_skipped"`
	BudgetSkipped    int `json:"budget_skipped"`
	Failed           int `json:"failed"`
	Knowledge        int `json:"knowledge"`
	Projections      int `json:"projections"`
	Purged           int `json:"purged"`
	ProjectionFailed int `json:"projection_failed"`
	PendingJobs      int `json:"pending_jobs"`
}

// ViewerSummaryProfiles 是落库侧写的计数概览。
type ViewerSummaryProfiles struct {
	Summarized int64 `json:"summarized"`
	Failed     int64 `json:"failed"`
	Fallback   int64 `json:"fallback"`
	Sources    int64 `json:"sources"`
	// LastSummaryAt 是最近一次模型成功生成侧写的时间。
	LastSummaryAt string `json:"last_summary_at,omitempty"`
}

// ViewerSummaryState 是面板一次取全的观众侧写状态。
type ViewerSummaryState struct {
	Available bool `json:"available"`
	// Enabled 是配置层是否允许侧写（记忆/观众档案开启且间隔非 0）。
	Enabled bool `json:"enabled"`
	// Running 是循环 goroutine 是否在跑；暂停时仍为 true。
	Running bool `json:"running"`
	Paused  bool `json:"paused"`
	// BackfillPending/BackfillRunning 是手动首次回填的排队/执行状态。
	BackfillPending int  `json:"backfill_pending"`
	BackfillRunning bool `json:"backfill_running"`
	// Frozen 是记忆冻结状态；冻结时循环保留但跳过生成。
	Frozen          bool                  `json:"frozen"`
	FrozenReason    string                `json:"frozen_reason,omitempty"`
	IntervalSeconds int                   `json:"interval_seconds"`
	LastRunAt       string                `json:"last_run_at,omitempty"`
	LastSuccessAt   string                `json:"last_success_at,omitempty"`
	LastError       string                `json:"last_error,omitempty"`
	LastRun         ViewerSummaryRun      `json:"last_run"`
	Profiles        ViewerSummaryProfiles `json:"profiles"`
	PendingPublish  int64                 `json:"pending_publish"`
	PendingPurge    int64                 `json:"pending_purge"`
	// Error 是状态汇总失败（如数据库不可用）时的简短说明。
	Error       string    `json:"error,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

// 手动回填的默认与上限：与侧写存储层的 1..500 校验保持一致。
const (
	viewerSummaryBackfillDefault = 50
	viewerSummaryBackfillMax     = 500
)

// handleViewerSummary 处理 /api/viewer/summary：GET 状态，POST 按白名单动作操作。
func (s *Server) handleViewerSummary(w http.ResponseWriter, r *http.Request) {
	if s.viewerSummary == nil {
		writeError(w, http.StatusServiceUnavailable, "观众侧写控制未启用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.viewerSummary.ViewerSummaryStatus(r.Context()))
	case http.MethodPost:
		if !confirmYes(w, r, "修改观众侧写") {
			return
		}
		var req struct {
			Action string `json:"action"`
			UID    string `json:"uid"`
			Limit  int    `json:"limit"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		uid := strings.TrimSpace(req.UID)
		if (req.Action == "rebuild" || req.Action == "revoke") && (uid == "" || len(uid) > 64) {
			writeError(w, http.StatusBadRequest, "action="+req.Action+" 需要 uid（不超过 64 字符）")
			return
		}
		var err error
		switch req.Action {
		case "pause":
			err = s.viewerSummary.SetViewerSummaryPaused(true)
		case "resume":
			err = s.viewerSummary.SetViewerSummaryPaused(false)
		case "backfill":
			limit := req.Limit
			if limit == 0 {
				limit = viewerSummaryBackfillDefault
			}
			if limit < 1 || limit > viewerSummaryBackfillMax {
				writeError(w, http.StatusBadRequest, "action=backfill 的 limit 必须在 1 至 500 之间")
				return
			}
			err = s.viewerSummary.TriggerViewerBackfill(r.Context(), limit)
		case "rebuild":
			err = s.viewerSummary.RebuildViewerSummary(r.Context(), uid)
		case "revoke":
			err = s.viewerSummary.RevokeViewerSummary(r.Context(), uid)
		default:
			writeError(w, http.StatusBadRequest, "action 只支持 pause/resume/backfill/rebuild/revoke")
			return
		}
		if err != nil {
			if errors.Is(err, store.ErrViewerNotFound) {
				writeError(w, http.StatusNotFound, "观众档案不存在")
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.log.Info("面板修改观众侧写",
			zap.String("action", "web_viewer_summary"),
			zap.String("target", req.Action),
			zap.String("uid", uid),
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "state": s.viewerSummary.ViewerSummaryStatus(r.Context()),
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET/POST")
	}
}
