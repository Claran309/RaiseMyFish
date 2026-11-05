package main

// 本文件是 0.14 的 watch 接线：直播采集运行期间旁路触发看直播，不影响采集/调度主链路。

import (
	"context"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
)

// watchRunner 是定时循环依赖的最小能力，Pipeline 直接满足。
type watchRunner interface {
	Run(context.Context) (watch.Result, error)
}

// buildWatchPipeline 只用已校验的配置构造流水线；构造期不联网、不执行 ffmpeg。
func buildWatchPipeline(cfg config.Config, logger *zap.Logger) (*watch.Pipeline, error) {
	_, provider, model, err := cfg.LLM.Resolve(cfg.Watch.LLMRoute)
	if err != nil {
		return nil, err
	}
	recorder, err := watch.NewStreamRecorder(watch.StreamRecorderOptions{
		RoomID:     cfg.Collector.Live.RoomID,
		Cookie:     cfg.Watch.Cookie,
		Quality:    cfg.Watch.StreamQuality,
		FFmpegPath: cfg.Watch.FFmpegPath,
		TempDir:    cfg.Watch.TempDir,
	})
	if err != nil {
		return nil, err
	}
	framer, err := watch.NewFFmpegFramer(watch.FFmpegFramerOptions{
		FFmpegPath:  cfg.Watch.FFmpegPath,
		FFprobePath: cfg.Watch.FFprobePath,
		TempDir:     cfg.Watch.TempDir,
	})
	if err != nil {
		return nil, err
	}
	audio, err := watch.NewFFmpegAudioExtractor(watch.FFmpegAudioExtractorOptions{
		FFmpegPath:  cfg.Watch.FFmpegPath,
		FFprobePath: cfg.Watch.FFprobePath,
		TempDir:     cfg.Watch.TempDir,
	})
	if err != nil {
		return nil, err
	}
	describer, err := watch.NewOpenAIDescriber(provider.BaseURL, provider.APIKey, model, nil, 0)
	if err != nil {
		return nil, err
	}
	transcriber, err := watch.NewHTTPTranscriber(cfg.Watch.ASRURL, nil, 0)
	if err != nil {
		return nil, err
	}
	return watch.NewPipeline(recorder, framer, describer, transcriber, audio, watch.PipelineOptions{
		Seconds: cfg.Watch.Seconds,
		Frames:  cfg.Watch.Frames,
		Prompt:  watch.DefaultPrompt,
		Logger:  logger,
	})
}

// runWatch 按 ticks 逐次触发；单次失败只记日志，不退出循环。
// ticks 由调用方提供，测试可注入 fake clock 的通道而不依赖真实计时。
func runWatch(ctx context.Context, runner watchRunner, ticks <-chan time.Time, now func() time.Time, logger *zap.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			started := now()
			result, err := runner.Run(ctx)
			elapsed := now().Sub(started)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				logger.Error("看直播失败，等待下一轮",
					zap.String("action", "watch"),
					zap.Int64("latency_ms", elapsed.Milliseconds()),
					zap.Error(err),
				)
				continue
			}
			fields := []zap.Field{
				zap.String("action", "watch"),
				zap.Int64("latency_ms", elapsed.Milliseconds()),
				zap.Bool("degraded", result.Degraded()),
				zap.Int("failures", len(result.Failures)),
				zap.Int("warnings", len(result.Warnings)),
				zap.Bool("has_description", result.Description != ""),
				zap.Bool("has_transcript", result.Transcript != ""),
			}
			if result.Degraded() {
				logger.Warn("看直播完成（有降级）", fields...)
			} else {
				logger.Info("看直播完成", fields...)
			}
		}
	}
}

// startWatch 仅在 [watch] enabled=true 时装配并启动；初始化失败只降级，不阻塞主链路。
// 返回的 stop 会取消并等待定时循环退出。
func startWatch(ctx context.Context, cfg config.Config, logger *zap.Logger) func() {
	noop := func() {}
	if !cfg.Watch.Enabled {
		return noop
	}
	pipeline, err := buildWatchPipeline(cfg, logger)
	if err != nil {
		logger.Error("看直播装配失败，降级运行", zap.String("action", "watch"), zap.Error(err))
		return noop
	}
	interval := time.Duration(cfg.Watch.IntervalSeconds) * time.Second
	stop := startWatchLoop(ctx, pipeline, interval, logger)
	logger.Info("看直播已启动",
		zap.String("action", "watch"),
		zap.Int("interval_seconds", cfg.Watch.IntervalSeconds),
		zap.Int("seconds", cfg.Watch.Seconds),
		zap.Int("frames", cfg.Watch.Frames),
	)
	return stop
}

// startWatchLoop 起定时循环；interval 非正时按 10 分钟兜底。
func startWatchLoop(ctx context.Context, runner watchRunner, interval time.Duration, logger *zap.Logger) func() {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		runWatch(runCtx, runner, ticker.C, time.Now, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}
