package main

// 常驻点赞：fishd 在线期间按固定间隔给当前直播间点赞。
//
// 边界：
//   - 只复用 bili.Client.Like（同一套 cookie/csrf 校验），不额外封装平台协议；
//   - 无凭据、缺 bili_jct 或直播采集未启用时不启动，只记降级原因；
//   - 单次失败只记日志，不重试、不退出循环；日志不含任何凭据。

import (
	"context"
	"time"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/config"
	"go.uber.org/zap"
)

const (
	// heartbeatLikeCount 是单次点赞数；B站接口允许 1-3，"一下"取 1。
	heartbeatLikeCount   = 1
	heartbeatLikeTimeout = 10 * time.Second
	heartbeatLikeAction  = "heartbeat_like"
)

// heartbeatLiker 是点赞循环依赖的最小能力，便于测试注入。
type heartbeatLiker interface {
	Like(ctx context.Context, room string, count int) error
}

// startHeartbeatLike 按 [presence] heartbeat_like_seconds 启动点赞循环；
// 返回的 stop 会取消并等待循环退出。未启用或降级时返回空 stop。
func startHeartbeatLike(ctx context.Context, cfg config.Config, logger *zap.Logger) func() {
	noop := func() {}
	seconds := cfg.Presence.HeartbeatLikeSeconds
	if seconds == 0 {
		return noop
	}
	if !cfg.Channels.Live.Enabled {
		logger.Info("常驻点赞未启动：直播采集未启用",
			zap.String("action", heartbeatLikeAction),
			zap.String("reason", "live_disabled"),
		)
		return noop
	}
	if cfg.BiliResolver == nil && cfg.Collector.Live.Cookie == "" {
		logger.Info("常驻点赞未启动：未配置凭据",
			zap.String("action", heartbeatLikeAction),
			zap.String("reason", "no_cookie"),
		)
		return noop
	}
	client, err := bili.New(bili.Options{Cookie: cfg.Collector.Live.Cookie, Resolver: cfg.BiliResolver, Timeout: heartbeatLikeTimeout})
	if err != nil {
		logger.Warn("常驻点赞未启动：凭据不可用",
			zap.String("action", heartbeatLikeAction),
			zap.String("reason", "bad_cookie"),
			zap.Error(err),
		)
		return noop
	}
	if !client.HasCSRF() {
		logger.Warn("常驻点赞未启动：凭据缺少 bili_jct",
			zap.String("action", heartbeatLikeAction),
			zap.String("reason", "no_csrf"),
		)
		return noop
	}

	interval := time.Duration(seconds) * time.Second
	logger.Info("常驻点赞已启动",
		zap.String("action", heartbeatLikeAction),
		zap.Int("interval_seconds", seconds),
	)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		runHeartbeatLike(runCtx, client, cfg.Collector.Live.RoomID, interval, ticker.C, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}

// runHeartbeatLike 每收到一个 tick 点赞一次；失败只记日志，循环继续。
func runHeartbeatLike(ctx context.Context, liker heartbeatLiker, room string, interval time.Duration, ticks <-chan time.Time, logger *zap.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			started := time.Now()
			if err := liker.Like(ctx, room, heartbeatLikeCount); err != nil {
				logger.Warn("常驻点赞失败，等待下一轮",
					zap.String("action", heartbeatLikeAction),
					zap.Int("interval_seconds", int(interval/time.Second)),
					zap.Error(err),
				)
				continue
			}
			logger.Info("常驻点赞成功",
				zap.String("action", heartbeatLikeAction),
				zap.Int("interval_seconds", int(interval/time.Second)),
				zap.Int64("latency_ms", time.Since(started).Milliseconds()),
			)
		}
	}
}
