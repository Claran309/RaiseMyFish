package proactive

import (
	"testing"
	"time"
)

func TestLimiterSlidingWindow(t *testing.T) {
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	l := newLimiter(2, time.Hour, 0)
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

func TestLimiterSendGap(t *testing.T) {
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	l := newLimiter(10, time.Hour, 30*time.Second)
	if reason := l.Check(start); reason != "" {
		t.Fatalf("首次发送不应被间隔拦截：%s", reason)
	}
	if !l.Reserve(start) {
		t.Fatal("首次发送应占用额度")
	}
	if reason := l.Check(start.Add(29 * time.Second)); reason != ReasonGap {
		t.Fatalf("30秒内应跳过且 reason=gap，实际 %q", reason)
	}
	if l.Reserve(start.Add(29 * time.Second)) {
		t.Fatal("30秒内不应再次占用额度")
	}
	if reason := l.Check(start.Add(30 * time.Second)); reason != "" {
		t.Fatalf("恰好满30秒应放行：%s", reason)
	}
	if !l.Reserve(start.Add(30 * time.Second)) {
		t.Fatal("满30秒应能占用额度")
	}
	if reason := l.Check(start.Add(59 * time.Second)); reason != ReasonGap {
		t.Fatalf("第二次发送后30秒内应继续跳过：%s", reason)
	}
	// 小时上限优先于间隔，日志只报一个原因。
	hourly := newLimiter(1, time.Hour, 30*time.Second)
	if !hourly.Reserve(start) {
		t.Fatal("小时限流首次应放行")
	}
	if reason := hourly.Check(start.Add(10 * time.Second)); reason != ReasonLimited {
		t.Fatalf("同时命中小时上限与间隔时应报 limited，实际 %q", reason)
	}
}
