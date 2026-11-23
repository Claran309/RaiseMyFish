package observe

import (
	"strings"
	"testing"
	"time"
)

// TestRenderL4Sections L4：fresh 延迟、死信 Top、单条成本与缓存命中。
func TestRenderL4Sections(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	cost := 0.0123
	hit := 0.42
	out := Render(Snapshot{
		WindowStart: now.Add(-24 * time.Hour), WindowEnd: now,
		Received: 10, Replied: 8, Skipped: 2,
		Latency: map[string]LatencyLine{
			"comment": {Count: 40, P50MS: 3000, P90MS: 6000},
			"dm":      {Count: 5, P50MS: 9000, P90MS: 12000},
		},
		DeadTop:         []DeadLine{{Channel: "comment", Stage: "generate", ErrorKind: "budget", Count: 3}},
		CostPerReplyUSD: &cost, CacheHitRate: &hit,
	})
	for _, want := range []string{
		"fresh 延迟：comment p50=3000ms p90=6000ms(n=40)",
		"dm 样本不足(n=5)",
		"死信 Top：comment/generate/budget=3",
		"单条回复：$0.0123 · 缓存命中 42.0%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("日报缺少 %q\n实际：\n%s", want, out)
		}
	}
}

// TestRenderL4SectionsOmittedWhenEmpty 空数据不新增段落，保持旧输出。
func TestRenderL4SectionsOmittedWhenEmpty(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	out := Render(Snapshot{WindowStart: now.Add(-time.Hour), WindowEnd: now, Received: 1, Replied: 1})
	for _, unwanted := range []string{"fresh 延迟", "死信 Top", "单条回复"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("空数据不应输出 %q：%s", unwanted, out)
		}
	}
}
