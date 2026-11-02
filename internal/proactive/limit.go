package proactive

// 本文件只保留「两次主动发送之间的最小间隔」。
//
// 0.19 重构：取消固定时间窗口（max_per_hour）——间隔本身已经限住了频率，
// 再叠一层小时窗口只会让「刚满一小时就集中发」这种反直觉行为出现。
// 像素变化检测也一并删除（用户判定没用，画面没变同样可以说话）。

import (
	"sync"
	"time"
)

// gapLimiter 保证两次发送之间至少隔 gap。
type gapLimiter struct {
	mu   sync.Mutex
	gap  time.Duration
	last time.Time
}

func newGapLimiter(gap time.Duration) *gapLimiter {
	return &gapLimiter{gap: gap}
}

// Check 返回本轮不可发送的原因；空字符串表示可以发送。
func (l *gapLimiter) Check(now time.Time) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.check(now)
}

// Reserve 检查并占用一次发送机会；返回 false 表示本轮应跳过。
//
// 按「尝试」占位：发送失败/结果不确定也算一次，宁可不发也不刷屏。
func (l *gapLimiter) Reserve(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.check(now) != "" {
		return false
	}
	l.last = now
	return true
}

// check 调用方必须持有锁；恰好满 gap 时放行。
func (l *gapLimiter) check(now time.Time) string {
	if l.gap > 0 && !l.last.IsZero() && now.Sub(l.last) < l.gap {
		return ReasonGap
	}
	return ""
}
