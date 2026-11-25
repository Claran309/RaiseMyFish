package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

// TestMetricsRegistryHasRequiredNames 目标要求的指标名必须全部注册。
func TestMetricsRegistryHasRequiredNames(t *testing.T) {
	reg := newMetricsRegistry()
	for _, want := range []string{
		"fishd_events_pending",
		"fishd_oldest_event_age_seconds",
		"fishd_scheduler_active",
		"fishd_lease_lost_total",
		"fishd_duplicate_send_total",
		"fishd_degrade_total",
		"fishd_reply_latency_seconds",
		"fishd_db_pool_in_use",
	} {
		if !reg.Has(want) {
			t.Errorf("指标未注册：%s", want)
		}
	}
	reg.Set("fishd_events_pending", nil, 1)
	reg.Inc("fishd_lease_lost_total", nil)
	out := reg.Render()
	for _, want := range []string{"# TYPE fishd_events_pending gauge", "# TYPE fishd_lease_lost_total counter"} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q\n%s", want, out)
		}
	}
}

// TestMetricsHandlerWithoutDB 无数据库时仍能输出运行时指标。
func TestMetricsHandlerWithoutDB(t *testing.T) {
	reg := newMetricsRegistry()
	rec := httptest.NewRecorder()
	reg.Handler(func() { collectDBMetrics(nil, reg) }).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"fishd_goroutines", "fishd_heap_bytes", "# TYPE fishd_goroutines gauge"} {
		if !strings.Contains(body, want) {
			t.Errorf("缺少 %q\n%s", want, body)
		}
	}
}

// TestMetricsBusCountsDuplicate 幂等拦截（Publish=false）要计入重复事件指标。
func TestMetricsBusCountsDuplicate(t *testing.T) {
	reg := newMetricsRegistry()
	inner := &dupBus{fakeBus: newFakeBus("live:1:1"), seen: map[string]bool{}}
	bus := metricsBus{Bus: inner, reg: reg}
	ev := event.Event{
		ID: "live:1:1", Channel: event.ChLive, Dir: event.DirIn, Session: "1",
		Text: "在吗", At: time.Now(), Status: event.StatusPending,
	}
	if ok, err := bus.Publish(context.Background(), ev); err != nil || !ok {
		t.Fatalf("首次发布失败：%v %v", ok, err)
	}
	if ok, err := bus.Publish(context.Background(), ev); err != nil || ok {
		t.Fatalf("重复发布未被拦截：%v %v", ok, err)
	}
	if !strings.Contains(reg.Render(), "fishd_duplicate_event_total 1") {
		t.Fatalf("重复事件未计数：%s", reg.Render())
	}
}

// dupBus 在 fakeBus 上补一个真正的幂等 Publish。
type dupBus struct {
	*fakeBus
	seen map[string]bool
}

func (b *dupBus) Publish(_ context.Context, ev event.Event) (bool, error) {
	if b.seen[ev.ID] {
		return false, nil
	}
	b.seen[ev.ID] = true
	return true, nil
}

// TestStartMetricsServerDisabled 关闭时不监听端口、不 panic。
func TestStartMetricsServerDisabled(t *testing.T) {
	stop := startMetricsServer(context.Background(), config.Config{}, nil, newMetricsRegistry(), zap.NewNop())
	stop()
}
