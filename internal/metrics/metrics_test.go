package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestRenderCountersAndGauges 输出必须带 HELP/TYPE，按名字稳定排序。
func TestRenderCountersAndGauges(t *testing.T) {
	r := New()
	r.Counter("fishd_lease_lost_total", "租约丢失次数")
	r.Gauge("fishd_scheduler_active", "调度器活跃任务数")
	r.Inc("fishd_lease_lost_total", nil)
	r.Inc("fishd_lease_lost_total", nil)
	r.Set("fishd_scheduler_active", nil, 3)

	out := r.Render()
	for _, want := range []string{
		"# HELP fishd_lease_lost_total 租约丢失次数",
		"# TYPE fishd_lease_lost_total counter",
		"fishd_lease_lost_total 2",
		"# TYPE fishd_scheduler_active gauge",
		"fishd_scheduler_active 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q\n%s", want, out)
		}
	}
	if strings.Index(out, "fishd_lease_lost_total") > strings.Index(out, "fishd_scheduler_active") {
		t.Fatal("指标未按名字排序")
	}
}

// TestRenderLabels 标签输出与转义。
func TestRenderLabels(t *testing.T) {
	r := New()
	r.Counter("fishd_degrade_total", "降级次数", "reason")
	r.Inc("fishd_degrade_total", map[string]string{"reason": "parse"})
	r.Inc("fishd_degrade_total", map[string]string{"reason": "timeout"})
	r.Inc("fishd_degrade_total", map[string]string{"reason": "parse"})
	out := r.Render()
	if !strings.Contains(out, `fishd_degrade_total{reason="parse"} 2`) ||
		!strings.Contains(out, `fishd_degrade_total{reason="timeout"} 1`) {
		t.Fatalf("标签输出错误：\n%s", out)
	}
}

// TestHandlerCallsCollect 抓取前执行 collect，method 限制为 GET/HEAD。
func TestHandlerCallsCollect(t *testing.T) {
	r := New()
	r.Gauge("fishd_events_pending", "待处理事件")
	called := 0
	h := r.Handler(func() {
		called++
		r.Set("fishd_events_pending", nil, 7)
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || called != 1 || !strings.Contains(rec.Body.String(), "fishd_events_pending 7") {
		t.Fatalf("handler 错误：code=%d called=%d body=%s", rec.Code, called, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 未被拒绝：%d", rec.Code)
	}
}

// TestConcurrentUpdates 并发写不 panic，最终计数正确。
func TestConcurrentUpdates(t *testing.T) {
	r := New()
	r.Counter("c", "计数")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Inc("c", nil)
			}
		}()
	}
	wg.Wait()
	if !strings.Contains(r.Render(), "c 5000") {
		t.Fatalf("并发计数错误：%s", r.Render())
	}
}

// TestUnknownAndNegativeIgnored 未注册指标与负增量被忽略。
func TestUnknownAndNegativeIgnored(t *testing.T) {
	r := New()
	r.Inc("missing", nil)
	r.Counter("c", "计数")
	r.Add("c", nil, -5)
	if strings.Contains(r.Render(), "missing") || strings.Contains(r.Render(), "c -") {
		t.Fatalf("非法更新被接受：%s", r.Render())
	}
}
