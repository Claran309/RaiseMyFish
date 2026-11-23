package main

// 本文件是 C5 会话滚动摘要 worker 的装配层。
//
// 职责：把配置、任务队列、会话状态仓库与后台模型适配器接成 worker；
// 任何装配失败都只降级为「不跑摘要」，不影响采集/回复主链路。

import (
	"context"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/conversationsummary"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// buildConversationSummarizer 装配摘要器：未启用 LLM 时返回 nil，由 worker 走抽取式兜底。
func buildConversationSummarizer(cfg config.Config, db *gorm.DB, logger *zap.Logger) conversationsummary.LLM {
	if !cfg.LLM.Enabled || len(cfg.LLM.Routing) == 0 {
		logger.Info("会话摘要未启用模型，使用抽取式兜底", zap.String("action", "conversation_summary"))
		return nil
	}
	key := backgroundRoute(cfg)
	var usage store.UsageRepo
	if repo, err := database.NewUsageRepo(db); err == nil {
		usage = repo
	} else {
		logger.Warn("会话摘要账本装配失败，本次调用不记成本", zap.String("action", "conversation_summary"), zap.Error(err))
	}
	adapter, model, err := buildBackgroundLLM(cfg, key, cfg.LLM.MaxReplyTokens, llmmeta.PurposeConversationSummary, usage, logger)
	if err != nil {
		logger.Warn("会话摘要模型装配失败，回落抽取式兜底", zap.String("action", "conversation_summary"), zap.Error(err))
		return nil
	}
	logger.Info("会话摘要模型已启用", zap.String("action", "conversation_summary"), zap.String("model", model))
	return adapter
}

// startConversationSummary 装配并启动摘要 worker；返回的 stop 会取消并等待循环退出。
func startConversationSummary(ctx context.Context, cfg config.Config, db *gorm.DB, logger *zap.Logger) func() {
	noop := func() {}
	cc := cfg.Conversation
	if !cc.Enabled || !cc.SummaryEnabled {
		return noop
	}
	jobs, err := database.NewConversationSummaryJobsRepo(db)
	if err != nil {
		logger.Error("会话摘要任务队列装配失败，跳过摘要", zap.String("action", "conversation_summary"), zap.Error(err))
		return noop
	}
	states, err := database.NewConversationRepo(db)
	if err != nil {
		logger.Error("会话状态仓库装配失败，跳过摘要", zap.String("action", "conversation_summary"), zap.Error(err))
		return noop
	}
	worker, err := conversationsummary.New(conversationsummary.Options{
		Jobs:            jobs,
		States:          states,
		LLM:             buildConversationSummarizer(cfg, db, logger),
		Channels:        conversationChannels(cc),
		EveryTurns:      cc.SummaryEveryTurns,
		Idle:            time.Duration(cc.SummaryIdleSeconds) * time.Second,
		Interval:        time.Duration(cc.SummaryIntervalSeconds) * time.Second,
		Timeout:         time.Duration(cc.SummaryTimeoutSeconds) * time.Second,
		MaxAttempts:     cc.SummaryMaxAttempts,
		ContextTurns:    cc.SummaryContextTurns,
		SummaryMaxRunes: cc.SummaryMaxRunes,
		Owner:           extractJobOwner(),
	})
	if err != nil {
		logger.Error("会话摘要 worker 初始化失败，跳过摘要", zap.String("action", "conversation_summary"), zap.Error(err))
		return noop
	}
	logger.Info("会话摘要已启动",
		zap.String("action", "conversation_summary"),
		zap.Int("every_turns", cc.SummaryEveryTurns),
		zap.Int("idle_seconds", cc.SummaryIdleSeconds),
		zap.Int("interval_seconds", cc.SummaryIntervalSeconds),
	)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runConversationSummary(runCtx, worker, time.Duration(cc.SummaryIntervalSeconds)*time.Second, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}

// summaryRunner 便于测试注入；RunOnce 跑一轮，失败只记日志。
type summaryRunner interface {
	RunOnce(context.Context) (conversationsummary.Stats, error)
}

func runConversationSummary(ctx context.Context, runner summaryRunner, interval time.Duration, logger *zap.Logger) {
	runOnce := func() {
		stats, err := runner.RunOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("会话摘要失败，等待下一轮", zap.String("action", "conversation_summary"), zap.Error(err))
			}
			return
		}
		if stats.Claimed == 0 {
			return
		}
		logger.Info("会话摘要完成",
			zap.String("action", "conversation_summary"),
			zap.Int("enqueued", stats.Enqueued),
			zap.Int("claimed", stats.Claimed),
			zap.Int("summarized", stats.Summarized),
			zap.Int("empty", stats.Empty),
			zap.Int("skipped", stats.Skipped),
			zap.Int("failed", stats.Failed),
			zap.Int("lease_lost", stats.LeaseLost),
		)
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
