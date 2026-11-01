package observe

// 本文件测 0.10b 心跳检测：阈值边界、多通道部分超时、空/null map、时钟注入、排序稳定性。

import (
	"strings"
	"testing"
	"time"
)

// hbNow 是固定的「当前时间」，所有用例都基于它构造，绝不依赖真实时钟。
var hbNow = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

// TestCheckThresholdBoundary 阈值边界：正好 30 分钟不告警，多 1 秒才告警。
func TestCheckThresholdBoundary(t *testing.T) {
	cases := []struct {
		name   string
		silent time.Duration
		want   int
	}{
		{"刚有事件", time.Second, 0},
		{"29分59秒", 29*time.Minute + 59*time.Second, 0},
		{"正好30分钟", 30 * time.Minute, 0}, // 严格「超过」才告警
		{"30分1秒", 30*time.Minute + time.Second, 1},
		{"两小时", 2 * time.Hour, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lastSeen := map[string]time.Time{"live": hbNow.Add(-tc.silent)}
			got := Check(hbNow, lastSeen)
			if len(got) != tc.want {
				t.Fatalf("告警数 = %d，期望 %d：%+v", len(got), tc.want, got)
			}
			if tc.want == 1 {
				if got[0].Channel != "live" {
					t.Errorf("通道 = %q，期望 live", got[0].Channel)
				}
				if got[0].Silent != tc.silent {
					t.Errorf("Silent = %v，期望 %v", got[0].Silent, tc.silent)
				}
				if !got[0].LastSeen.Equal(hbNow.Add(-tc.silent)) {
					t.Errorf("LastSeen = %v，期望原样带回", got[0].LastSeen)
				}
			}
		})
	}
}

// TestCheckPartialTimeout 多通道只有部分超时：只报超时的，且按通道名排序。
func TestCheckPartialTimeout(t *testing.T) {
	lastSeen := map[string]time.Time{
		"qq":      hbNow.Add(-2 * time.Hour),    // 超时
		"live":    hbNow.Add(-10 * time.Second), // 正常
		"comment": hbNow.Add(-45 * time.Minute), // 超时
		"dm":      hbNow.Add(-30 * time.Minute), // 正好阈值，不告警
	}

	got := Check(hbNow, lastSeen)
	if len(got) != 2 {
		t.Fatalf("告警数 = %d，期望 2：%+v", len(got), got)
	}
	if got[0].Channel != "comment" || got[1].Channel != "qq" {
		t.Errorf("顺序 = %q,%q，期望 comment,qq（通道名升序）", got[0].Channel, got[1].Channel)
	}
}

// TestCheckEmptyAndNilMap 空 map / nil map 都不告警、不 panic。
func TestCheckEmptyAndNilMap(t *testing.T) {
	if got := Check(hbNow, map[string]time.Time{}); len(got) != 0 {
		t.Errorf("空 map 应无告警，实际 %+v", got)
	}
	if got := Check(hbNow, nil); len(got) != 0 {
		t.Errorf("nil map 应无告警，实际 %+v", got)
	}
	// 零值 now 也不能崩
	if got := Check(time.Time{}, map[string]time.Time{"live": time.Time{}}); len(got) != 1 {
		t.Errorf("零值 now + 零值 lastSeen 应产生 1 条「从未收到」，实际 %+v", got)
	}
}

// TestCheckZeroLastSeen 零值 lastSeen 表示「从未收到事件」，要告警且不做时长计算。
func TestCheckZeroLastSeen(t *testing.T) {
	got := Check(hbNow, map[string]time.Time{"live": {}})
	if len(got) != 1 {
		t.Fatalf("告警数 = %d，期望 1", len(got))
	}
	w := got[0]
	if w.Channel != "live" {
		t.Errorf("通道 = %q", w.Channel)
	}
	if !w.LastSeen.IsZero() {
		t.Errorf("LastSeen 应保持零值，实际 %v", w.LastSeen)
	}
	if w.Silent != 0 {
		t.Errorf("Silent = %v，期望 0（不做溢出计算）", w.Silent)
	}
	if !strings.Contains(w.String(), "从未收到事件") {
		t.Errorf("文案不符：%q", w.String())
	}
}

// TestCheckFutureLastSeen 对端时钟比本机快时，静默时长为负 → 不能误报告警。
func TestCheckFutureLastSeen(t *testing.T) {
	for _, offset := range []time.Duration{time.Hour, 24 * time.Hour} {
		lastSeen := map[string]time.Time{"live": hbNow.Add(offset)}
		if got := Check(hbNow, lastSeen); len(got) != 0 {
			t.Errorf("未来时间（+%v）不应告警，实际 %+v", offset, got)
		}
	}
}

// TestCheckDeterministic 固定时钟注入：重复调用结果完全一致，且与真实时间无关。
func TestCheckDeterministic(t *testing.T) {
	lastSeen := map[string]time.Time{
		"live":    hbNow.Add(-time.Hour),
		"comment": hbNow.Add(-29 * time.Minute), // 差 1 分钟到阈值，推后 2 分钟就会告警
		"dm":      hbNow.Add(-72 * time.Minute),
		"qq":      hbNow.Add(-31 * time.Minute),
	}

	first := Check(hbNow, lastSeen)
	if len(first) != 3 {
		t.Fatalf("告警数 = %d，期望 3", len(first))
	}
	for i := 0; i < 20; i++ {
		again := Check(hbNow, lastSeen)
		if len(again) != len(first) {
			t.Fatalf("第 %d 次数量不同", i+1)
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("第 %d 次结果不同：%+v vs %+v", i+1, again[j], first[j])
			}
		}
	}

	// 换了 now，结论必须跟着变——证明用的是注入值而不是系统时钟
	later := Check(hbNow.Add(2*time.Minute), lastSeen)
	if len(later) != 4 {
		t.Errorf("把 now 推后 2 分钟应变成 4 条告警，实际 %d", len(later))
	}
}

// TestWarningString 告警文案可读、含关键信息。
func TestWarningString(t *testing.T) {
	w := Warning{
		Channel:  "live",
		LastSeen: time.Date(2026, 11, 1, 23, 10, 0, 0, time.UTC),
		Silent:   50 * time.Minute,
	}
	got := w.String()
	for _, want := range []string{"live", "50 分钟", "2026-11-01 23:10"} {
		if !strings.Contains(got, want) {
			t.Errorf("文案 %q 缺少 %q", got, want)
		}
	}

	// 不足一分钟按秒显示
	short := Warning{Channel: "dm", LastSeen: hbNow.Add(-45 * time.Second), Silent: 45 * time.Second}
	if got := short.String(); !strings.Contains(got, "45 秒") {
		t.Errorf("短时长文案不符：%q", got)
	}
}
