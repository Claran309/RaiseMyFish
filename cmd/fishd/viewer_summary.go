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

	summarizer, err := viewer.NewSummarizer(profiles, events, llm, views, viewer.SummaryOptions{})
	if err != nil {
		return nil, "", err
	}
	return summarizer, model, nil
}

// startViewerSummary 装配并启动侧写循环；返回的 stop 会取消并等待循环退出。
// 间隔为 0 表示关闭；初始化失败只降级，调用方主链路不受影响。
func startViewerSummary(ctx context.Context, cfg config.Config, db *gorm.DB, runtime knowledgeAssembly, logger *zap.Logger) func() {
	noop := func() {}
	seconds := cfg.Memory.ViewerSummaryIntervalSeconds
	if seconds == 0 {
		return noop
	}
	if seconds < 60 || seconds > 86400 {
		logger.Warn("观众侧写间隔无效，跳过",
			zap.String("action", viewerSummaryAction), zap.Int("interval_seconds", seconds))
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
	return startViewerSummaryLoop(ctx, worker, interval, logger)
}

// startViewerSummaryLoop 起周期循环；返回 stop 取消并等待退出。
func startViewerSummaryLoop(ctx context.Context, worker viewerSummaryWorker, interval time.Duration, logger *zap.Logger) func() {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runViewerSummary(runCtx, worker, interval, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}

// runViewerSummary 立即跑一轮，之后按 interval 循环；单轮失败只记日志，等下轮重试。
func runViewerSummary(ctx context.Context, worker viewerSummaryWorker, interval time.Duration, logger *zap.Logger) {
	runOnce := func() {
		stats, err := worker.RunOnce(ctx)
		fields := []zap.Field{
			zap.String("action", viewerSummaryAction),
			zap.Int("candidates", stats.Candidates),
			zap.Int("summarized", stats.Summarized),
			zap.Int("fallback", stats.Fallback),
			zap.Int("retained", stats.Retained),
			zap.Int("revoked", stats.Revoked),
			zap.Int("source_changed", stats.SourceChanged),
			zap.Int("skipped", stats.Skipped),
			zap.Int("failed", stats.Failed),
			zap.Int("knowledge", stats.Knowledge),
			zap.Int("projections", stats.Projections),
			zap.Int("purged", stats.Purged),
			zap.Int("projection_failed", stats.ProjectionFailed),
			zap.Int("pending_jobs", stats.PendingJobs),
		}
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("观众侧写本轮有失败", append(fields, zap.Error(err))...)
			}
			return
		}
		logger.Info("观众侧写完成", fields...)
	}
	runOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}
