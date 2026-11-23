package conversation

// 本文件实现 C3 的直播短窗口：只保留「刚说完立刻追问」的几条消息。
//
// 边界（见 会话状态升级清单-20261123 C3）：
//   - 纯内存、进程内，不写 conversation_state、不生成摘要；
//   - 默认关闭；开启后按 TTL（默认 30 秒）自动过期；
//   - 键按「房间 + 用户」隔离，不按用户全局合并；
//   - 有界：单个键最多保留 N 条，键总数超过上限时先清过期、再淘汰最旧的。

import (
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/event"
)

// WindowOptions 是短窗口参数。
type WindowOptions struct {
	// TTL 是消息存活时间；<=0 时用 30 秒。
	TTL time.Duration
	// PerUser 是单个用户保留的条数；<=0 时用 3。
	PerUser int
	// MaxKeys 是键总数上限；<=0 时用 5000。
	MaxKeys int
	// Now 注入时钟，仅测试需要。
	Now func() time.Time
}

// WindowEntry 是窗口里的一条消息。
type WindowEntry struct {
	FromID string
	From   string
	Text   string
	Dir    string
	At     time.Time
}

// Window 是按 (房间, 用户) 隔离的内存短窗口。
type Window struct {
	opts WindowOptions
	mu   sync.Mutex
	data map[string][]WindowEntry
}

// NewWindow 构造短窗口；TTL<=0 表示关闭，返回 nil。
func NewWindow(opts WindowOptions) *Window {
	if opts.TTL <= 0 {
		return nil
	}
	if opts.PerUser <= 0 {
		opts.PerUser = 3
	}
	if opts.MaxKeys <= 0 {
		opts.MaxKeys = 5000
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Window{opts: opts, data: make(map[string][]WindowEntry)}
}

// Key 返回直播短窗口键；非直播或缺少房间/用户时返回空串。
//
// 不写持久会话：直播是高频公开场景，只有「刚说完马上追问」才值得接上下文。
func WindowKey(ev event.Event) string {
	if ev.Channel != event.ChLive || ev.Private {
		return ""
	}
	room := strings.TrimSpace(ev.Session)
	user := strings.TrimSpace(ev.FromID)
	if room == "" || user == "" {
		return ""
	}
	return "live:" + room + ":" + user
}

// Add 记录一条消息；窗口为 nil 时是空操作。
func (w *Window) Add(ev event.Event) {
	if w == nil {
		return
	}
	key := WindowKey(ev)
	text := strings.TrimSpace(ev.Text)
	if key == "" || text == "" {
		return
	}
	now := w.opts.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked(now)
	entries := append(w.data[key], WindowEntry{
		FromID: ev.FromID, From: ev.FromName, Text: text, Dir: string(ev.Dir), At: now,
	})
	if len(entries) > w.opts.PerUser {
		entries = entries[len(entries)-w.opts.PerUser:]
	}
	w.data[key] = entries
}

// Recent 返回该事件对应用户在 TTL 内的最近消息（旧→新），不含当前这条。
func (w *Window) Recent(ev event.Event) []WindowEntry {
	if w == nil {
		return nil
	}
	key := WindowKey(ev)
	if key == "" {
		return nil
	}
	now := w.opts.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	// 读路径也清理过期键：高频直播下不能只靠写入触发回收，
	// 否则「没人再说话」的用户会一直占着内存。
	w.pruneLocked(now)
	cutoff := now.Add(-w.opts.TTL)
	entries := w.data[key]
	out := make([]WindowEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.At.After(cutoff) && entry.Text != strings.TrimSpace(ev.Text) {
			out = append(out, entry)
		}
	}
	return out
}

// Len 返回当前键数（测试与面板用）。
func (w *Window) Len() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.data)
}

// pruneLocked 清理过期键；键数超过上限时淘汰最旧的一个键。
func (w *Window) pruneLocked(now time.Time) {
	cutoff := now.Add(-w.opts.TTL)
	for key, entries := range w.data {
		fresh := entries[:0]
		for _, entry := range entries {
			if entry.At.After(cutoff) {
				fresh = append(fresh, entry)
			}
		}
		if len(fresh) == 0 {
			delete(w.data, key)
			continue
		}
		w.data[key] = fresh
	}
	if len(w.data) < w.opts.MaxKeys {
		return
	}
	oldestKey, oldest := "", time.Time{}
	for key, entries := range w.data {
		if len(entries) == 0 {
			continue
		}
		if oldestKey == "" || entries[len(entries)-1].At.Before(oldest) {
			oldestKey, oldest = key, entries[len(entries)-1].At
		}
	}
	if oldestKey != "" {
		delete(w.data, oldestKey)
	}
}
