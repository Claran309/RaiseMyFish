package proactive

import (
	"sync"
	"time"
)

// limiter 是滑动窗口限流：窗口内最多 max 次「发送尝试」。
// 按尝试而非成功计数，失败/不确定的发送也占额度，宁可少发也不刷屏。
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	times  []time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window}
}

// Allow 只检查，不占额度。
func (l *limiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	return len(l.times) < l.max
}

// Reserve 检查并占用一次额度；返回 false 表示本轮应跳过。
func (l *limiter) Reserve(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	if len(l.times) >= l.max {
		return false
	}
	l.times = append(l.times, now)
	return true
}

func (l *limiter) prune(now time.Time) {
	cutoff := now.Add(-l.window)
	kept := l.times[:0]
	for _, at := range l.times {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	l.times = kept
}
