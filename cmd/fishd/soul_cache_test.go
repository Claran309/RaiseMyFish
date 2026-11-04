package main

// 本文件测灵魂记忆 mtime 缓存：文件没变不重解析、改了能读到新内容。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"RaiseMyFish/internal/memory"
)

func writeSoulFile(t *testing.T, path, summary string, at time.Time) {
	t.Helper()
	entries := []memory.SoulMemory{{Title: "主人是学生", Summary: summary, Importance: 0.9, ValidFrom: at}}
	if err := memory.WriteSoulAtomic(context.Background(), path, entries); err != nil {
		t.Fatal(err)
	}
}

// TestSoulCacheReusesUntilMtimeChanges 文件不变时返回同一份缓存。
func TestSoulCacheReusesUntilMtimeChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	writeSoulFile(t, path, "大二", time.Now())
	c := newSoulCache(path)

	first := c.load(context.Background())
	if len(first) == 0 {
		t.Fatal("首次应读到内容")
	}
	// 直接篡改内存缓存，验证第二次确实没重读文件
	c.mu.Lock()
	c.lines = []string{"- 缓存命中"}
	c.mu.Unlock()
	if got := c.load(context.Background()); len(got) != 1 || got[0] != "- 缓存命中" {
		t.Fatalf("文件未变时应命中缓存：%+v", got)
	}

	// 改文件（新 mtime + 新大小）→ 重新解析
	writeSoulFile(t, path, "大三了", time.Now())
	if got := c.load(context.Background()); len(got) != 1 || got[0] == "- 缓存命中" {
		t.Fatalf("文件变化后应重读：%+v", got)
	}
}

// TestSoulCacheMissingFile 文件不存在返回 nil 且不 panic。
func TestSoulCacheMissingFile(t *testing.T) {
	c := newSoulCache(filepath.Join(t.TempDir(), "nope.md"))
	if got := c.load(context.Background()); got != nil {
		t.Errorf("缺失文件应返回 nil：%+v", got)
	}
	var nilCache *soulCache
	if got := nilCache.load(context.Background()); got != nil {
		t.Errorf("nil 缓存应安全返回 nil：%+v", got)
	}
}

// TestSoulCacheEmptyPath 空路径不建缓存。
func TestSoulCacheEmptyPath(t *testing.T) {
	if c := newSoulCache("  "); c != nil {
		t.Error("空路径应返回 nil")
	}
	// 顺带确认目录不存在时不报错
	if _, err := os.Stat(filepath.Join(t.TempDir(), "nope")); !os.IsNotExist(err) {
		t.Skip("环境异常")
	}
}
