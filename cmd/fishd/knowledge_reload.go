package main

// 本文件实现知识库热重载：轮询目录 mtime，变更则让 FileReader 重建快照。
//
// 设计取舍：
//   - 轮询而不是 fsnotify：不引新依赖，30s 粒度对「梗白学习」够用；
//   - 失败只记日志，不影响正在跑的召回（旧快照继续服务）；
//   - 只在 mtime/文件数变化时才重建，避免每轮白扫一遍解析。

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/knowledge"
	"go.uber.org/zap"
)

// knowledgeStamp 是目录的「指纹」：最大 mtime + 文件数。
type knowledgeStamp struct {
	mod   time.Time
	count int
}

// scanKnowledgeStamp 递归扫描目录；目录不存在返回零值指纹（不报错）。
func scanKnowledgeStamp(dir string) knowledgeStamp {
	var stamp knowledgeStamp
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // 读不到的文件跳过，不打断整轮扫描
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil //nolint:nilerr // 同上：单个文件失败不影响指纹
		}
		stamp.count++
		if info.ModTime().After(stamp.mod) {
			stamp.mod = info.ModTime()
		}
		return nil
	})
	return stamp
}

// startKnowledgeHotReload 起一个轮询 goroutine；返回 stop 函数（取消并等待退出）。
//
// reader 为 nil 或目录为空时直接返回空 stop；是否启用由调用方（运行时控制面）决定。
func startKnowledgeHotReload(ctx context.Context, reader *knowledge.FileReader, cfg config.Knowledge, log *zap.Logger) func() {
	if reader == nil || cfg.Dir == "" || log == nil {
		return func() {}
	}
	interval := time.Duration(cfg.ReloadIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	last := scanKnowledgeStamp(cfg.Dir)
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
				stamp := scanKnowledgeStamp(cfg.Dir)
				if stamp == last {
					continue
				}
				if err := reader.Reload(runCtx); err != nil {
					// 失败保留旧快照：下次再试
					log.Warn("知识热重载失败，继续使用旧快照",
						zap.String("action", "knowledge_reload"),
						zap.Error(err),
					)
					continue
				}
				last = stamp
				log.Info("知识库已热重载", zap.String("action", "knowledge_reload"))
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
