package main

// 本文件是灵魂记忆的读取缓存：文件 mtime 没变就不重复解析。
//
// Generate 每轮都会取一次长期设定，文件虽然小（<2KB），但每次读盘 + YAML 解析
// 在高频弹幕下也是白花的；这里按 mtime 做进程内缓存，worker 重建后 mtime 变化会自动失效。

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/memory"
)

// soulCache 缓存 soul-memories.md 的渲染结果。
type soulCache struct {
	path string

	mu      sync.Mutex
	modTime time.Time
	size    int64
	lines   []string
}

// newSoulCache 构造缓存；path 为空返回 nil（调用方按「未配置」处理）。
func newSoulCache(path string) *soulCache {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return &soulCache{path: path}
}

// load 返回最新设定行；文件没变直接返回缓存。
//
// 文件不存在/解析失败一律返回 nil（不注入），不阻塞回复。
func (c *soulCache) load(ctx context.Context) []string {
	if c == nil {
		return nil
	}
	info, err := os.Stat(c.path)
	if err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.modTime.IsZero() && info.ModTime().Equal(c.modTime) && info.Size() == c.size {
		return c.lines
	}
	entries, err := memory.LoadSoul(ctx, c.path)
	if err != nil || len(entries) == 0 {
		// 记录「这次时间戳」也要更新，避免坏文件每轮重试解析
		c.modTime, c.size, c.lines = info.ModTime(), info.Size(), nil
		return nil
	}
	text := memory.RenderSoul(entries)
	c.modTime, c.size = info.ModTime(), info.Size()
	if text == "" {
		c.lines = nil
		return nil
	}
	c.lines = strings.Split(text, "\n")
	return c.lines
}
