package proactive

import (
	"sync"
	"time"
)

// limiter 是滑动窗口限流：窗口内最多 max 次「发送尝试」，且两次尝试间隔不小于 gap。
// 按尝试而非成功计数，失败/不确定的发送也占额度，宁可少发也不刷屏。
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	gap    time.Duration
	times  []time.Time
	last   time.Time
}

func newLimiter(max int, window, gap time.Duration) *limiter {
	return &limiter{max: max, window: window, gap: gap}
}

// Check 返回本轮不可发送的原因；空字符串表示可以发送。
func (l *limiter) Check(now time.Time) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.check(now)
}

// Allow 只检查，不占额度。
func (l *limiter) Allow(now time.Time) bool { return l.Check(now) == "" }

// Reserve 检查并占用一次额度；返回 false 表示本轮应跳过。
func (l *limiter) Reserve(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.check(now) != "" {
		return false
	}
	l.times = append(l.times, now)
	l.last = now
	return true
}

// check 调用方必须持有锁。
func (l *limiter) check(now time.Time) string {
	l.prune(now)
	if len(l.times) >= l.max {
		return ReasonLimited
	}
	// 首次发送没有 last；恰好满 gap 时放行。
	if l.gap > 0 && !l.last.IsZero() && now.Sub(l.last) < l.gap {
		return ReasonGap
	}
	return ""
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
