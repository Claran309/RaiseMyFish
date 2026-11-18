package main

// 本文件是本地 ASR 的轻量守护：只探测 /health，掉线时按冷却时间执行启动脚本。
// 不负责录音/转写本身，也不阻塞主动弹幕或看直播主链路。

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
)

const (
	asrCheckTimeout  = 2 * time.Second
	asrStartCooldown = 2 * time.Minute
)

type asrSupervisor struct {
	endpoint string
	interval time.Duration
	cooldown time.Duration
	log      *zap.Logger
	check    func(context.Context) error
	start    func(context.Context) error
	now      func() time.Time

	mu         sync.Mutex
	checked    bool
	available  bool
	lastError  string
	lastCheck  time.Time
	lastStart  time.Time
	startCount int
}

// checkOnce 探测一次；不可用时在冷却结束后请求拉起，拉起失败不退出守护。
func (s *asrSupervisor) checkOnce(ctx context.Context) {
	if s == nil || s.check == nil || s.log == nil {
		return
	}
	err := s.check(ctx)
	now := s.now()

	s.mu.Lock()
	wasChecked := s.checked
	wasAvailable := s.available
	s.checked = true
	s.available = err == nil
	s.lastCheck = now
	if err != nil {
		s.lastError = err.Error()
	} else {
		s.lastError = ""
	}
	shouldStart := false
	if err != nil && s.start != nil && (s.lastStart.IsZero() || now.Sub(s.lastStart) >= s.cooldown) {
		shouldStart = true
		s.lastStart = now
		s.startCount++
	}
	s.mu.Unlock()

	if err == nil {
		if !wasChecked || !wasAvailable {
			s.log.Info("ASR服务可用", zap.String("action", "asr_supervisor"), zap.String("host", watch.ASRHost(s.endpoint)))
		}
		return
	}
	if !wasChecked || wasAvailable {
		s.log.Warn("ASR服务不可用，等待自动拉起", zap.String("action", "asr_supervisor"), zap.String("host", watch.ASRHost(s.endpoint)), zap.Error(err))
	}
	if !shouldStart {
		return
	}
	s.log.Warn("尝试拉起ASR服务", zap.String("action", "asr_autostart"), zap.String("host", watch.ASRHost(s.endpoint)))
	if startErr := s.start(ctx); startErr != nil {
		s.log.Error("拉起ASR服务失败", zap.String("action", "asr_autostart"), zap.Error(startErr))
		return
	}
	s.log.Info("已请求拉起ASR服务", zap.String("action", "asr_autostart"), zap.String("host", watch.ASRHost(s.endpoint)))
}

// run 先立即探一次，之后按 interval 轮询；ctx 取消即退出。
func (s *asrSupervisor) run(ctx context.Context) {
	if s == nil || s.interval <= 0 {
		return
	}
	s.checkOnce(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkOnce(ctx)
		}
	}
}

// startASRSupervisor 只在显式开启且配置了启动脚本时装配；失败只影响自动拉起。
func startASRSupervisor(ctx context.Context, cfg config.Watch, logger *zap.Logger) {
	if logger == nil || !cfg.ASRAutoStart || strings.TrimSpace(cfg.ASRURL) == "" || strings.TrimSpace(cfg.ASRStartScript) == "" {
		return
	}
	interval := time.Duration(cfg.ASRCheckSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	s := &asrSupervisor{
		endpoint: cfg.ASRURL,
		interval: interval,
		cooldown: asrStartCooldown,
		log:      logger,
		check: func(ctx context.Context) error {
			return watch.CheckASR(ctx, cfg.ASRURL, nil, asrCheckTimeout)
		},
		start: startASRBatch(cfg.ASRStartScript),
		now:   time.Now,
	}
	logger.Info("ASR 守护已启动",
		zap.String("action", "asr_supervisor"),
		zap.String("host", watch.ASRHost(cfg.ASRURL)),
		zap.Int("interval_seconds", cfg.ASRCheckSeconds),
		zap.Duration("cooldown", asrStartCooldown),
	)
	go s.run(ctx)
}

// startASRBatch 通过 cmd start /min 拉起独立的 ASR 窗口；不等待服务就绪。
func startASRBatch(script string) func(context.Context) error {
	return func(ctx context.Context) error {
		if runtime.GOOS != "windows" {
			return fmt.Errorf("ASR 自动拉起只支持 Windows 启动脚本")
		}
		// #nosec G204 -- 脚本路径来自可信部署配置，逐参数传递且不经 shell 解析业务数据。
		cmd := exec.CommandContext(ctx, "cmd", "/c", "start", "", "/min", filepath.FromSlash(script))
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("启动 ASR 脚本失败：%w", err)
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
}
