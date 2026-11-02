package proactive

// 本文件测 gap 限流：0.19 只保留「两次发送最小间隔」。

import (
	"testing"
	"time"
)

// TestGapLimiterBlocksWithinGap 间隔内拒绝，正好满间隔放行。
func TestGapLimiterBlocksWithinGap(t *testing.T) {
	l := newGapLimiter(30 * time.Second)
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	if reason := l.Check(start); reason != "" {
		t.Fatalf("首次应放行，实际 %q", reason)
	}
	if !l.Reserve(start) {
		t.Fatal("首次 Reserve 应成功")
	}
	if reason := l.Check(start.Add(29 * time.Second)); reason != ReasonGap {
		t.Errorf("29 秒时应跳过，实际 %q", reason)
	}
	if l.Reserve(start.Add(29 * time.Second)) {
		t.Error("间隔内 Reserve 应失败")
	}
	if reason := l.Check(start.Add(30 * time.Second)); reason != "" {
		t.Errorf("正好满 30 秒应放行，实际 %q", reason)
	}
	if !l.Reserve(start.Add(30 * time.Second)) {
		t.Error("满间隔后 Reserve 应成功")
	}
}

// TestGapLimiterZeroGapAlwaysAllows gap=0 表示不限（测试/特殊场景）。
func TestGapLimiterZeroGapAlwaysAllows(t *testing.T) {
	l := newGapLimiter(0)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if reason := l.Check(now); reason != "" {
			t.Fatalf("第 %d 次被拦：%q", i+1, reason)
		}
		if !l.Reserve(now) {
			t.Fatalf("第 %d 次占位失败", i+1)
		}
	}
}

// TestGapLimiterNoFixedWindow 0.19 取消了小时窗口：一小时能发多少次只由 gap 决定。
func TestGapLimiterNoFixedWindow(t *testing.T) {
	const gap = 30 * time.Second
	l := newGapLimiter(gap)
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	sent := 0
	for i := 0; i < 40; i++ { // 40 × 30s = 20 分钟
		at := start.Add(time.Duration(i) * gap)
		if l.Reserve(at) {
			sent++
		}
	}
	if sent != 40 {
		t.Errorf("每满 gap 都应放行，实际只发了 %d 次", sent)
	}
}
