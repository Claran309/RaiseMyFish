package main

// 观众侧写定时任务装配（#63/#64）。
//
// 默认每小时对近期活跃观众的公开弹幕做一次总结；LLM 未启用或调用失败时
// 回落到统计聚合（旧家效果），不阻塞采集/消费主链路。
// 知识库未启用时只写档案与记忆，跳过 viewers.md。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	// 侧写要输出五字段 JSON，默认回复上限不够。
	viewerSummaryMaxTokens = 512
	viewerSummaryAction    = "viewer_summary"
)

// viewerSummaryWorker 是侧写循环依赖的最小能力，便于装配测试注入。
type viewerSummaryWorker interface {
	RunOnce(context.Context) (viewer.SummaryStats, error)
	RunBackfill(context.Context, int) (viewer.SummaryStats, error)
}

// viewerKnowledgeAdapter 把知识写入器适配成 viewer.SummaryKnowledge。
type viewerKnowledgeAdapter struct {
	writer *knowledge.ViewerWriter
}

func (a viewerKnowledgeAdapter) UpsertViewer(ctx context.Context, view viewer.SummaryView) error {
	if a.writer == nil {
		return errors.New("观众知识写入器未装配")
	}
	return a.writer.Upsert(ctx, knowledge.ViewerEntry{
		UID:        view.UID,
		Name:       view.Name,
		Summary:    view.Summary,
		Stats:      view.Stats,
		Keywords:   view.Keywords,
		UpdatedAt:  view.UpdatedAt,
		SourceHash: view.SourceHash,
		Generator:  view.Generator,
	})
}

func (a viewerKnowledgeAdapter) RemoveViewer(ctx context.Context, uid string) error {
	if a.writer == nil {
		return errors.New("观众知识写入器未装配")
	}
	return a.writer.Remove(ctx, uid)
}

// viewerPurgeProjection 在档案重建撤回观众后同步清理 viewers.md 自动条目。
type viewerPurgeProjection struct {
	writer *knowledge.ViewerWriter
}

func (p viewerPurgeProjection) PurgeViewers(ctx context.Context, uids []string) error {
	if p.writer == nil {
		return errors.New("观众知识写入器未装配")
	}
	var failures []error
	for _, uid := range uids {
		if err := p.writer.Remove(ctx, uid); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// viewerSummaryOptions 把配置映射成侧写参数；预算字段超出范围时 NewSummarizer 会拒绝装配。
func viewerSummaryOptions(cfg config.Config) viewer.SummaryOptions {
	return viewer.SummaryOptions{
		MinHits:        cfg.Viewer.MinHits,
		MaxPerRun:      cfg.Memory.ViewerSummaryMaxPerRun,
		BackfillLimit:  cfg.Memory.ViewerSummaryBackfillLimit,
		MinNewMessages: cfg.Memory.ViewerSummaryMinNewMessages,
		RecentTTL:      time.Duration(cfg.Memory.ViewerSummaryRecentTTLSeconds) * time.Second,
		DailyViewers:   cfg.Memory.ViewerSummaryDailyViewers,
	}
}

// newViewerSummarizer 装配侧写器；LLM 或知识库缺失只降级，不阻止任务启动。
func newViewerSummarizer(cfg config.Config, db *gorm.DB, runtime knowledgeAssembly, logger *zap.Logger) (viewerSummaryWorker, string, error) {
	if db == nil {
		return nil, "", errors.New("观众侧写缺少数据库资源")
	}
	profiles, err := database.NewViewerRepo(db)
	if err != nil {
		return nil, "", fmt.Errorf("装配观众档案失败：%w", err)
	}
	events, err := database.NewEventRepo(db)
	if err != nil {
		return nil, "", fmt.Errorf("装配事件来源失败：%w", err)
	}

	var llm viewer.SummaryLLM
	model := "stats-only"
	if cfg.LLM.Enabled && len(cfg.LLM.Routing) > 0 {
		// 账本是增强能力：装配失败时照旧侧写，只是这次调用不进成本统计。
		var usage store.UsageRepo
		if repo, uerr := database.NewUsageRepo(db); uerr == nil {
			usage = repo
		} else if logger != nil {
			logger.Warn("观众侧写账本装配失败，本次调用不记成本", zap.String("action", viewerSummaryAction), zap.Error(uerr))
		}
		built, name, berr := buildBackgroundLLM(cfg, backgroundRoute(cfg), viewerSummaryMaxTokens, llmmeta.PurposeViewerSummary, usage, logger)
		if berr != nil {
			if logger != nil {
				logger.Warn("观众侧写 LLM 装配失败，回落统计聚合", zap.String("action", viewerSummaryAction), zap.Error(berr))
			}
		} else {
			llm = built
			model = name
		}
	}

	var views viewer.SummaryKnowledge
	if cfg.Knowledge.Enabled && runtime.Reader != nil && strings.TrimSpace(cfg.Knowledge.Dir) != "" {
		// 生产路径复用 main 装配的唯一写器；测试等直接调用时再按目录兜底构造。
		writer := runtime.ViewerWriter
		if writer == nil {
			built, werr := knowledge.NewViewerWriter(cfg.Knowledge.Dir)
			if werr != nil {
				if logger != nil {
					logger.Warn("观众侧写知识写入器装配失败，跳过 viewers.md", zap.String("action", viewerSummaryAction), zap.Error(werr))
				}
			} else {
				writer = built
			}
		}
		if writer != nil {
			views = viewerKnowledgeAdapter{writer: writer}
		}
	}

	summarizer, err := viewer.NewSummarizer(profiles, events, llm, views, viewerSummaryOptions(cfg))
	if err != nil {
		return nil, "", err
	}
	return summarizer, model, nil
}

// viewerSummaryFreeze 判断本轮是否应冻结跳过：配置冻结优先，其次看运行时状态文件，
// 与记忆提炼 worker 的语义一致（冻结只停写，不清理已生成的侧写）。
type viewerSummaryFreeze struct {
	cfgFrozen bool
	path      string
	// isFrozen 可注入状态检查（测试用）；nil 时读取 memory.IsFrozen。
	isFrozen func(string) (bool, error)
}

func (g viewerSummaryFreeze) frozen() (bool, string, error) {
	if g.cfgFrozen {
		return true, "config", nil
	}
	if strings.TrimSpace(g.path) == "" {
		return false, "", nil
	}
	check := g.isFrozen
	if check == nil {
		check = memory.IsFrozen
	}
	frozen, err := check(g.path)
	if err != nil {
		return false, "", err
	}
	if frozen {
		return true, "state_file", nil
	}
	return false, "", nil
}

// startViewerSummary 装配并启动侧写循环；返回的 stop 会取消并等待循环退出。
//
// 停用条件（R07）：
//   - Memory.Enabled=false 或 Viewer.Enabled=false：不装配、不调度；
//   - 配置冻结 Memory.Frozen=true：不启动循环（解冻需重启，与提炼 worker 一致）；
//   - 运行时间隔为 0：不调度，但已生成的侧写保留；
//   - 运行时冻结文件 state/memory.frozen 存在：循环保留、每轮跳过，解冻后自动恢复。
func startViewerSummary(ctx context.Context, cfg config.Config, db *gorm.DB, runtime knowledgeAssembly, freezePath string, state *viewerSummaryRuntime, logger *zap.Logger) func() {
	noop := func() {}
	if !cfg.Memory.Enabled {
		logger.Info("观众侧写停用：记忆提炼未启用", zap.String("action", viewerSummaryAction), zap.String("reason", "memory_disabled"))
		return noop
	}
	if !cfg.Viewer.Enabled {
		logger.Info("观众侧写停用：观众档案未启用", zap.String("action", viewerSummaryAction), zap.String("reason", "viewer_disabled"))
		return noop
	}
	seconds := cfg.Memory.ViewerSummaryIntervalSeconds
	if seconds == 0 {
		return noop
	}
	if seconds < 60 || seconds > 86400 {
		logger.Warn("观众侧写间隔无效，跳过",
			zap.String("action", viewerSummaryAction), zap.Int("interval_seconds", seconds))
		return noop
	}
	if cfg.Memory.Frozen {
		logger.Info("观众侧写停用：记忆已冻结", zap.String("action", viewerSummaryAction), zap.String("reason", "config"))
		return noop
	}
	worker, model, err := newViewerSummarizer(cfg, db, runtime, logger)
	if err != nil {
		logger.Error("观众侧写装配失败，降级运行", zap.String("action", viewerSummaryAction), zap.Error(err))
		return noop
	}
	interval := time.Duration(seconds) * time.Second
	logger.Info("观众侧写已启动",
		zap.String("action", viewerSummaryAction),
		zap.String("model", model),
		zap.Bool("knowledge", runtime.Reader != nil && cfg.Knowledge.Enabled),
		zap.Duration("interval", interval),
	)
	return startViewerSummaryLoop(ctx, worker, interval, viewerSummaryFreeze{path: freezePath}, state, logger)
}

// startViewerSummaryLoop 起周期循环；返回 stop 取消并等待退出。
func startViewerSummaryLoop(ctx context.Context, worker viewerSummaryWorker, interval time.Duration, freeze viewerSummaryFreeze, state *viewerSummaryRuntime, logger *zap.Logger) func() {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if state != nil {
			state.setRunning(true)
			defer state.setRunning(false)
		}
		runViewerSummary(runCtx, worker, interval, freeze, state, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}

// runViewerSummary 立即跑一轮，之后按 interval 循环；单轮失败只记日志，等下轮重试。
// 面板手动回填走同一循环串行执行，避免与定时轮并发调用模型。
func runViewerSummary(ctx context.Context, worker viewerSummaryWorker, interval time.Duration, freeze viewerSummaryFreeze, state *viewerSummaryRuntime, logger *zap.Logger) {
	// allowed 统一检查冻结：冻结只停写，不清理已生成的侧写（R07）。
	allowed := func() bool {
		frozen, reason, err := freeze.frozen()
		if err != nil {
			logger.Warn("读取记忆冻结状态失败，观众侧写本轮跳过",
				zap.String("action", viewerSummaryAction), zap.Error(err))
			return false
		}
		if frozen {
			logger.Info("记忆已冻结，观众侧写本轮跳过",
				zap.String("action", viewerSummaryAction), zap.String("reason", reason))
			return false
		}
		return true
	}
	// run 统一执行一轮并记录运行快照与日志；extra 用于标注回填等触发方式。
	run := func(execute func() (viewer.SummaryStats, error), extra ...zap.Field) {
		stats, err := execute()
		if state != nil {
			state.record(stats, err, time.Now())
		}
		fields := []zap.Field{
			zap.String("action", viewerSummaryAction),
			zap.Int("candidates", stats.Candidates),
			zap.Int("summarized", stats.Summarized),
			zap.Int("fallback", stats.Fallback),
			zap.Int("retained", stats.Retained),
			zap.Int("revoked", stats.Revoked),
			zap.Int("expired", stats.Expired),
			zap.Int("source_changed", stats.SourceChanged),
			zap.Int("skipped", stats.Skipped),
			zap.Int("busy_skipped", stats.BusySkipped),
			zap.Int("budget_skipped", stats.BudgetSkipped),
			zap.Int("failed", stats.Failed),
			zap.Int("knowledge", stats.Knowledge),
			zap.Int("projections", stats.Projections),
			zap.Int("purged", stats.Purged),
			zap.Int("projection_failed", stats.ProjectionFailed),
			zap.Int("pending_jobs", stats.PendingJobs),
		}
		fields = append(fields, extra...)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("观众侧写本轮有失败", append(fields, zap.Error(err))...)
			}
			return
		}
		logger.Info("观众侧写完成", fields...)
	}
	runOnce := func() {
		if !allowed() {
			return
		}
		if state != nil && state.Paused() {
			logger.Info("观众侧写已暂停，本轮跳过", zap.String("action", viewerSummaryAction))
			return
		}
		run(func() (viewer.SummaryStats, error) { return worker.RunOnce(ctx) }, zap.String("trigger", "scheduled"))
	}
	runBackfill := func(limit int) {
		if state != nil {
			state.beginBackfill()
			defer state.finishBackfill()
		}
		// 手动回填忽略暂停（显式操作），但冻结仍然优先：冻结期间不写任何派生内容。
		if !allowed() {
			return
		}
		run(func() (viewer.SummaryStats, error) { return worker.RunBackfill(ctx, limit) },
			zap.String("trigger", "backfill"), zap.Int("requested", limit))
	}
	runOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var backfill <-chan int
	if state != nil {
		backfill = state.BackfillRequests()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		case limit := <-backfill:
			runBackfill(limit)
		}
	}
}
