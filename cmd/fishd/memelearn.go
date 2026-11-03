package main

// 梗白学习装配：配置默认关闭；依赖缺失或初始化失败只降级，不阻塞采集/消费主链路。

import (
	"context"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	memeLearnInterval = time.Hour
	// 模型要输出 title/usage/tags，中文占 token 较多；128 的默认回复上限不够。
	memeLearnMaxTokens = 512
	memeLearnAction    = "meme_learn"
)

// memeLearnWorker 是学习循环依赖的最小能力，便于装配测试注入。
type memeLearnWorker interface {
	RunOnce(context.Context) (knowledge.MemeLearnStats, error)
}

// startMemeLearn 装配并启动学习循环；返回的 stop 会取消并等待循环退出。
// 未启用或初始化失败时返回空 stop，调用方主链路不受影响。
func startMemeLearn(ctx context.Context, cfg config.Config, db *gorm.DB, runtime knowledgeAssembly, logger *zap.Logger) func() {
	noop := func() {}
	if !cfg.Knowledge.MemeLearn {
		return noop
	}
	if !cfg.LLM.Enabled || len(cfg.LLM.Routing) == 0 {
		logger.Warn("梗白学习已配置但 LLM 未启用，跳过", zap.String("action", memeLearnAction))
		return noop
	}
	if runtime.Reader == nil {
		logger.Warn("梗白学习已配置但知识Reader未装配，跳过", zap.String("action", memeLearnAction))
		return noop
	}
	source, err := database.NewEventRepo(db)
	if err != nil {
		logger.Error("梗白学习装配失败，降级运行", zap.String("action", memeLearnAction), zap.Error(err))
		return noop
	}
	llm, model, err := buildBackgroundLLM(cfg, backgroundRoute(cfg), memeLearnMaxTokens)
	if err != nil {
		logger.Error("梗白学习模型装配失败，降级运行", zap.String("action", memeLearnAction), zap.Error(err))
		return noop
	}
	learner, err := knowledge.NewMemeLearner(cfg.Knowledge.Dir, runtime.Reader, source, llm, knowledge.MemeLearnOptions{
		MaxPerDay: cfg.Knowledge.MemeLearnMaxPerDay,
		Channel:   event.ChLive,
	})
	if err != nil {
		logger.Error("梗白学习初始化失败，降级运行", zap.String("action", memeLearnAction), zap.Error(err))
		return noop
	}

	logger.Info("梗白学习已启动",
		zap.String("action", memeLearnAction),
		zap.String("model", model),
		zap.Int("max_per_day", cfg.Knowledge.MemeLearnMaxPerDay),
		zap.Duration("interval", memeLearnInterval),
	)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMemeLearn(runCtx, learner, memeLearnInterval, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}

// runMemeLearn 立即学一轮，之后按 interval 循环；单轮失败只记日志，等下轮重试。
func runMemeLearn(ctx context.Context, worker memeLearnWorker, interval time.Duration, logger *zap.Logger) {
	runOnce := func() {
		stats, err := worker.RunOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("梗白学习失败，等待下一轮", zap.String("action", memeLearnAction), zap.Error(err))
			}
			return
		}
		logger.Info("梗白学习完成",
			zap.String("action", memeLearnAction),
			zap.Int("scanned", stats.Scanned),
			zap.Int("candidates", stats.Candidates),
			zap.Int("learned", stats.Learned),
			zap.Int("duplicated", stats.Duplicated),
			zap.Int("skipped", stats.Skipped),
			zap.Int("limited", stats.Limited),
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
