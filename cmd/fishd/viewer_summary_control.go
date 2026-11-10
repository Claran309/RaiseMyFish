package main

// 观众侧写运行时状态与面板控制面（R08）。
//
// 循环把最近一轮的时间/统计/错误写成内存快照，面板按需读取；
// 落库计数由 database.ViewerRepo.SummaryPanel 汇总，两者都不含侧写正文。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/viewer"
	"RaiseMyFish/internal/web"
	"go.uber.org/zap"
)

// viewerSummaryRuntime 是进程内的侧写运行状态；并发安全。
type viewerSummaryRuntime struct {
	paused atomic.Bool

	// backfillCh 是面板手动回填的请求队列，容量 1：同一时间只允许一个回填任务。
	backfillOnce sync.Once
	backfillCh   chan int

	mu              sync.RWMutex
	running         bool
	backfillPending int
	backfillRunning bool
	lastRunAt       time.Time
	lastSuccessAt   time.Time
	lastError       string
	lastStats       viewer.SummaryStats
}

func (r *viewerSummaryRuntime) SetPaused(paused bool) { r.paused.Store(paused) }

func (r *viewerSummaryRuntime) Paused() bool { return r.paused.Load() }

// BackfillRequests 返回手动回填请求队列；循环 select 它触发 RunBackfill。
func (r *viewerSummaryRuntime) BackfillRequests() <-chan int {
	return r.backfillChannel()
}

func (r *viewerSummaryRuntime) backfillChannel() chan int {
	if r == nil {
		return nil
	}
	r.backfillOnce.Do(func() { r.backfillCh = make(chan int, 1) })
	return r.backfillCh
}

// QueueBackfill 非阻塞排队一次回填请求；已有请求或正在执行时拒绝。
func (r *viewerSummaryRuntime) QueueBackfill(limit int) error {
	ch := r.backfillChannel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.backfillPending > 0 || r.backfillRunning {
		return errors.New("已有观众侧写回填任务在执行")
	}
	select {
	case ch <- limit:
		r.backfillPending = limit
		return nil
	default:
		return errors.New("观众侧写回填队列已满")
	}
}

// beginBackfill 取出队列后标记为执行中，面板据此显示「回填中」。
func (r *viewerSummaryRuntime) beginBackfill() {
	r.mu.Lock()
	r.backfillPending = 0
	r.backfillRunning = true
	r.mu.Unlock()
}

func (r *viewerSummaryRuntime) finishBackfill() {
	r.mu.Lock()
	r.backfillRunning = false
	r.mu.Unlock()
}

func (r *viewerSummaryRuntime) setRunning(running bool) {
	r.mu.Lock()
	r.running = running
	r.mu.Unlock()
}

// record 记录一轮结束（无论成败）；成功轮同时推进最后成功时间。
func (r *viewerSummaryRuntime) record(stats viewer.SummaryStats, runErr error, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastRunAt = at.UTC()
	r.lastStats = stats
	if runErr != nil {
		r.lastError = clipRuntimeError(runErr.Error())
		return
	}
	r.lastSuccessAt = at.UTC()
	r.lastError = ""
}

// clipRuntimeError 只保留安全文案且限长，避免把长 SQL/连接串带进面板。
func clipRuntimeError(text string) string {
	text = fmt.Sprint(text)
	if utf8.RuneCountInString(text) <= 160 {
		return text
	}
	return string([]rune(text)[:160]) + "…"
}

type viewerSummarySnapshot struct {
	running         bool
	backfillPending int
	backfillRunning bool
	paused          bool
	lastRunAt       time.Time
	lastSuccessAt   time.Time
	lastError       string
	lastStats       viewer.SummaryStats
}

func (r *viewerSummaryRuntime) snapshot() viewerSummarySnapshot {
	if r == nil {
		return viewerSummarySnapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return viewerSummarySnapshot{
		running: r.running, paused: r.paused.Load(),
		backfillPending: r.backfillPending, backfillRunning: r.backfillRunning,
		lastRunAt: r.lastRunAt, lastSuccessAt: r.lastSuccessAt,
		lastError: r.lastError, lastStats: r.lastStats,
	}
}

// ViewerSummaryStatus 汇总面板需要的侧写状态（只含计数与时间）。
func (c *runtimeControl) ViewerSummaryStatus(ctx context.Context) web.ViewerSummaryState {
	now := c.now().UTC()
	st := web.ViewerSummaryState{
		Enabled:         c.base.Memory.Enabled && c.base.Viewer.Enabled && c.base.Memory.ViewerSummaryIntervalSeconds > 0,
		IntervalSeconds: c.base.Memory.ViewerSummaryIntervalSeconds,
		GeneratedAt:     now,
	}
	snap := c.viewerSummary.snapshot()
	st.Running = snap.running
	st.Paused = snap.paused
	st.BackfillPending = snap.backfillPending
	st.BackfillRunning = snap.backfillRunning
	if !snap.lastRunAt.IsZero() {
		st.LastRunAt = snap.lastRunAt.Format(time.RFC3339)
	}
	if !snap.lastSuccessAt.IsZero() {
		st.LastSuccessAt = snap.lastSuccessAt.Format(time.RFC3339)
	}
	st.LastError = snap.lastError
	st.LastRun = web.ViewerSummaryRun{
		Candidates: snap.lastStats.Candidates, Summarized: snap.lastStats.Summarized,
		Fallback: snap.lastStats.Fallback, Retained: snap.lastStats.Retained,
		Revoked: snap.lastStats.Revoked, Expired: snap.lastStats.Expired,
		Skipped: snap.lastStats.Skipped, BusySkipped: snap.lastStats.BusySkipped,
		BudgetSkipped: snap.lastStats.BudgetSkipped, Failed: snap.lastStats.Failed,
		Knowledge: snap.lastStats.Knowledge, Projections: snap.lastStats.Projections,
		Purged: snap.lastStats.Purged, ProjectionFailed: snap.lastStats.ProjectionFailed,
		PendingJobs: snap.lastStats.PendingJobs,
	}
	frozen, reason, err := viewerSummaryFreeze{cfgFrozen: c.base.Memory.Frozen, path: c.freezePath}.frozen()
	switch {
	case err != nil:
		st.Error = "读取记忆冻结状态失败"
	case frozen:
		st.Frozen = true
		st.FrozenReason = reason
	}
	if c.viewerProfiles == nil {
		if st.Error == "" {
			st.Error = "观众侧写仓储未装配"
		}
		return st
	}
	st.Available = true
	panel, err := c.viewerProfiles.SummaryPanel(ctx)
	if err != nil {
		st.Error = clipRuntimeError(err.Error())
		return st
	}
	st.Profiles = web.ViewerSummaryProfiles{
		Summarized: panel.Summarized, Failed: panel.Failed,
		Fallback: panel.Fallback, Sources: panel.Sources,
	}
	if !panel.LastSummaryAt.IsZero() {
		st.Profiles.LastSummaryAt = panel.LastSummaryAt.UTC().Format(time.RFC3339)
	}
	st.PendingPublish = panel.PendingPublish
	st.PendingPurge = panel.PendingPurge
	return st
}

// SetViewerSummaryPaused 暂停/恢复后台侧写调度；不清理已有侧写。
func (c *runtimeControl) SetViewerSummaryPaused(paused bool) error {
	if c.viewerSummary == nil {
		return errors.New("观众侧写未装配")
	}
	c.viewerSummary.SetPaused(paused)
	return nil
}

// TriggerViewerBackfill 排队一次首次侧写回填；实际执行在侧写循环里串行进行。
//
// 显式人工操作：不受日预算/定时回填名额限制，但仍有 1..500 的人数上限，
// 且循环执行时照样遵守记忆冻结与前台繁忙让路。
func (c *runtimeControl) TriggerViewerBackfill(ctx context.Context, limit int) error {
	if c.viewerSummary == nil {
		return errors.New("观众侧写未装配")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if limit < 1 || limit > 500 {
		return errors.New("回填人数必须在1至500之间")
	}
	if err := c.viewerSummary.QueueBackfill(limit); err != nil {
		return err
	}
	c.log.Info("面板触发观众侧写回填",
		zap.String("action", "web_viewer_summary_backfill"), zap.Int("limit", limit))
	return nil
}

// RebuildViewerSummary 把一位观众标记为下一轮重建。
func (c *runtimeControl) RebuildViewerSummary(ctx context.Context, uid string) error {
	if c.viewerProfiles == nil {
		return errors.New("观众侧写仓储未装配")
	}
	if err := c.viewerProfiles.RequeueSummary(ctx, uid); err != nil {
		return err
	}
	c.log.Info("面板标记观众侧写重建", zap.String("action", "web_viewer_summary_rebuild"), zap.String("uid", uid))
	return nil
}

// RevokeViewerSummary 立即撤回一位观众的侧写，知识清理走下一轮投影任务。
func (c *runtimeControl) RevokeViewerSummary(ctx context.Context, uid string) error {
	if c.viewerProfiles == nil {
		return errors.New("观众侧写仓储未装配")
	}
	if err := c.viewerProfiles.RevokeSummary(ctx, uid); err != nil {
		return err
	}
	c.log.Info("面板撤回观众侧写", zap.String("action", "web_viewer_summary_revoke"), zap.String("uid", uid))
	return nil
}

// 确保接口实现（编译期检查）。
var _ web.ViewerSummaryControl = (*runtimeControl)(nil)
