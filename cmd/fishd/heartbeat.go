package main

// 本文件给 fishd 加心跳：每 30 秒写一次 state/fishd.heartbeat。
//
// 心跳走独立文件（不动状态文件 json），看门狗只看它的 mtime 与里面的 pid。

import (
	"context"
	"os"
	"time"

	"RaiseMyFish/internal/watchdog"
	"go.uber.org/zap"
)

// heartbeatInterval 是心跳写入间隔（派发单固定 30s）。
const heartbeatInterval = 30 * time.Second

// processStartedAt 是本进程的启动时刻（包初始化时记录）。
//
// O11：心跳里带上它，看门狗才能识别「PID 被复用」——
// 崩溃后系统可能把同一个 PID 分给无关进程，只判断存活就去杀会误杀。
var processStartedAt = time.Now()

// startHeartbeat 启动心跳写入；返回 stop 会等待 goroutine 退出。
func startHeartbeat(ctx context.Context, path string, log *zap.Logger) func() {
	return startHeartbeatEvery(ctx, path, heartbeatInterval, log)
}

// startHeartbeatEvery 与 startHeartbeat 相同，但允许测试注入更短间隔。
func startHeartbeatEvery(ctx context.Context, path string, interval time.Duration, log *zap.Logger) func() {
	if ctx == nil || path == "" || log == nil || interval <= 0 {
		return func() {}
	}
	pid := os.Getpid()
	write := func() {
		if err := watchdog.WriteHeartbeat(path, pid, processStartedAt, time.Now()); err != nil {
			log.Warn("心跳写入失败", zap.String("action", "heartbeat_write"), zap.Error(err))
		}
	}
	// 启动立刻写一次，避免看门狗在第一个周期看到「文件不存在」
	write()

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				write()
			}
		}
	}()
	log.Info("心跳已启动",
		zap.String("action", "heartbeat_start"),
		zap.String("path", path),
		zap.Duration("interval", interval),
	)
	return func() {
		cancel()
		<-done
	}
}
