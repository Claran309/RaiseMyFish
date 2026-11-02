package proactive

import (
	"testing"
	"time"
)

func TestLimiterSlidingWindow(t *testing.T) {
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	l := newLimiter(2, time.Hour)
	if !l.Reserve(start) || !l.Reserve(start.Add(time.Minute)) {
		t.Fatal("窗口内前两次应放行")
	}
	if l.Allow(start.Add(2*time.Minute)) || l.Reserve(start.Add(2*time.Minute)) {
		t.Fatal("超过上限仍放行")
	}
	if !l.Allow(start.Add(time.Hour)) {
		t.Fatal("满一小时后第一条应过期")
	}
	if !l.Reserve(start.Add(time.Hour)) {
		t.Fatal("过期后应能占用额度")
	}
	if l.Allow(start.Add(time.Hour + 30*time.Second)) {
		t.Fatal("第二条未满一小时，不应有额度")
	}
	if !l.Allow(start.Add(time.Hour + time.Minute + time.Nanosecond)) {
		t.Fatal("第二条过期后应恢复额度")
	}
}
