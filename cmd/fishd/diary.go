package main

// 每日总结装配（#76）：定时生成与面板手动补生成共用同一个 digest。
//
// 定时只在 [summary].enabled=true 时启动；面板手动生成只要 LLM 可用就能用，
// 方便先补历史日报再决定是否开启每日调度。

import (
	"context"
	"fmt"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/diary"
	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const diaryAction = "daily_summary"

// diaryGenerator 是面板手动生成依赖的最小能力。
type diaryGenerator interface {
	Generate(ctx context.Context, day string) (store.DailySummary, error)
}

// dailyDigest 读取当天事件 → LLM 总结 → 按天覆盖保存。
type dailyDigest struct {
	repo       store.DiaryRepo
	summarizer *diary.Summarizer
	model      string
	maxEvents  int
}

// Generate 生成指定日期（东八区自然日）的日报；重复生成覆盖旧日报。
func (d *dailyDigest) Generate(ctx context.Context, day string) (store.DailySummary, error) {
	start, err := diary.ParseDay(day)
	if err != nil {
		return store.DailySummary{}, err
	}
	end := start.AddDate(0, 0, 1)
	rows, err := d.repo.DayEvents(ctx, start.UTC(), end.UTC(), d.maxEvents)
	if err != nil {
		return store.DailySummary{}, fmt.Errorf("读取日报事件失败：%w", err)
	}
	// 仓储按新到旧返回；总结按时间正序，方便模型理解当天脉络。
	events := make([]diary.Event, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		events = append(events, diary.Event{
			Channel: row.Channel, Dir: row.Dir, FromName: row.FromName,
			Text: row.Text, At: row.At, Private: row.Private,
		})
	}
	content, err := d.summarizer.Summarize(ctx, start, events)
	if err != nil {
		return store.DailySummary{}, err
	}
	summary := store.DailySummary{
		Day: start.Format(diary.DayLayout), Content: content, Model: d.model, EventCount: len(rows),
	}
	if err := d.repo.SaveDailySummary(ctx, summary); err != nil {
		return store.DailySummary{}, fmt.Errorf("保存日报失败：%w", err)
	}
	now := time.Now()
	summary.CreatedAt, summary.UpdatedAt = now, now
	return summary, nil
}

// buildDailyDigest 装配日报生成器；账本与数据库都是必需依赖。
func buildDailyDigest(cfg config.Config, db *gorm.DB, usage store.UsageRepo, logger *zap.Logger) (*dailyDigest, error) {
	if db == nil {
		return nil, fmt.Errorf("每日总结缺少数据库资源")
	}
	repo, err := database.NewWebRepo(db)
	if err != nil {
		return nil, fmt.Errorf("装配日报仓储失败：%w", err)
	}
	llm, model, err := buildBackgroundLLM(cfg, backgroundRoute(cfg), cfg.Summary.MaxTokens, llmmeta.PurposeDailySummary, usage, logger)
	if err != nil {
		return nil, fmt.Errorf("装配日报模型失败：%w", err)
	}
	summarizer, err := diary.New(llm, diary.Options{MaxEvents: cfg.Summary.MaxEvents})
	if err != nil {
		return nil, err
	}
	return &dailyDigest{repo: repo, summarizer: summarizer, model: model, maxEvents: cfg.Summary.MaxEvents}, nil
}

// startDailySummary 装配生成器并在启用时启动每日调度。
//
// 返回的生成器始终可用于面板手动生成（只要 LLM 可用）；stop 在未启用时为空函数。
func startDailySummary(ctx context.Context, cfg config.Config, db *gorm.DB, usage store.UsageRepo, logger *zap.Logger) (diaryGenerator, func()) {
	noop := func() {}
	var generator *dailyDigest
	if cfg.LLM.Enabled {
		built, err := buildDailyDigest(cfg, db, usage, logger)
		if err != nil {
			logger.Warn("每日总结装配失败，日报生成不可用",
				zap.String("action", diaryAction), zap.Error(err))
		} else {
			generator = built
		}
	}
	if !cfg.Summary.Enabled {
		logger.Info("每日总结定时任务未启用", zap.String("action", diaryAction), zap.String("reason", "disabled"))
		if generator == nil {
			return nil, noop
		}
		return generator, noop
	}
	if generator == nil {
		return nil, noop
	}
	logger.Info("每日总结已启动",
		zap.String("action", diaryAction),
		zap.String("model", generator.model),
		zap.Int("hour", cfg.Summary.Hour),
		zap.Int("minute", cfg.Summary.Minute),
		zap.Int("max_events", cfg.Summary.MaxEvents),
	)
	return generator, startDailySummaryLoop(ctx, generator, cfg.Summary, time.Now, logger)
}

// startDailySummaryLoop 每天在固定时刻生成前一天日报；单轮失败只记日志等明天。
func startDailySummaryLoop(ctx context.Context, generator diaryGenerator, summary config.Summary, now func() time.Time, logger *zap.Logger) func() {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			next := nextDailyRun(now(), summary.Hour, summary.Minute)
			timer := time.NewTimer(time.Until(next))
			select {
			case <-runCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
				day := diary.Yesterday(now())
				started := time.Now()
				saved, err := generator.Generate(runCtx, day)
				if err != nil {
					if runCtx.Err() == nil {
						logger.Error("每日总结生成失败",
							zap.String("action", diaryAction), zap.String("day", day), zap.Error(err))
					}
					continue
				}
				logger.Info("每日总结完成",
					zap.String("action", diaryAction),
					zap.String("day", saved.Day),
					zap.Int("events", saved.EventCount),
					zap.Duration("elapsed", time.Since(started)),
				)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// nextDailyRun 返回 now 之后最近一次 hour:minute（东八区）；恰好等于触发时刻时取次日。
func nextDailyRun(now time.Time, hour, minute int) time.Time {
	local := now.In(diary.Zone)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, diary.Zone)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
