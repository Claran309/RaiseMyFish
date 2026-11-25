package main

import (
	"strings"
	"testing"
	"time"
)

// TestRenderLineChart 图表必须包含标题、坐标与折线，且不出现 NaN。
func TestRenderLineChart(t *testing.T) {
	svg := renderLineChart(
		[]string{"2026-11-01", "2026-11-02", "2026-11-03"},
		[]series{
			{Label: "live", Points: []point{{0, 10}, {1, 20}, {2, 0}}},
			{Label: "comment", Points: []point{{0, 1}, {1, 2}, {2, 3}}},
		},
		"测试图",
	)
	for _, want := range []string{"<svg", "测试图", "live", "comment", "<path", "2026-11-01"} {
		if !strings.Contains(svg, want) {
			t.Errorf("SVG 缺少 %q", want)
		}
	}
	if strings.Contains(svg, "NaN") || strings.Contains(svg, "+Inf") {
		t.Fatal("SVG 含非法数值")
	}
}

// TestRenderLineChartZeroData 全零数据不能除零。
func TestRenderLineChartZeroData(t *testing.T) {
	svg := renderLineChart([]string{"a", "b"}, []series{{Label: "x", Points: []point{{0, 0}, {1, 0}}}}, "零")
	if strings.Contains(svg, "NaN") || !strings.Contains(svg, "<svg") {
		t.Fatalf("零数据渲染异常：%s", svg)
	}
}

// TestFormatValue 时间统一 RFC3339，nil 为空串而不是 "<nil>"。
func TestFormatValue(t *testing.T) {
	at := time.Date(2026, 11, 24, 1, 2, 3, 0, time.UTC)
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{at, "2026-11-24T01:02:03Z"},
		{int64(42), "42"},
		{3.5, "3.5"},
		{true, "true"},
		{[]byte("x"), "x"},
	}
	for _, tc := range cases {
		if got := formatValue(tc.in); got != tc.want {
			t.Errorf("formatValue(%v)=%q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestDayKey 图表日期只取日期部分，兼容历史 RFC3339 输出。
func TestDayKey(t *testing.T) {
	if got := dayKey("2026-11-24T00:00:00Z"); got != "2026-11-24" {
		t.Fatalf("dayKey=%q", got)
	}
	if got := dayKey("2026-11-24"); got != "2026-11-24" {
		t.Fatalf("dayKey=%q", got)
	}
}
