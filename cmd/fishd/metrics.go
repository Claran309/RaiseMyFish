package main

// 本文件装配 Prometheus 指标端点（`[metrics]`）与进程内指标收集。
//
// 边界：
//   - 关闭时零开销：不注册路由、不创建 goroutine、不改任何既有行为；
//   - 只监听回环（config.Metrics.Validate 已限制）；
//   - 抓取时才查 DB，失败只跳过该指标，不影响其它指标输出。

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/metrics"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// newMetricsRegistry 注册本项目要暴露的指标名。
func newMetricsRegistry() *metrics.Registry {
	reg := metrics.New()
	reg.Gauge("fishd_events_pending", "待处理入向事件数（status=pending）")
	reg.Gauge("fishd_oldest_event_age_seconds", "最老 pending 事件距今秒数")
	reg.Gauge("fishd_scheduler_pending", "调度器待派发任务数")
	reg.Gauge("fishd_scheduler_active", "调度器活跃任务数")
	reg.Gauge("fishd_scheduler_waiting", "调度器等待租约续期任务数")
	reg.Gauge("fishd_scheduler_cooldowns", "调度器会话冷却数")
	reg.Counter("fishd_lease_renew_total", "租约续期成功次数")
	reg.Counter("fishd_lease_lost_total", "租约丢失次数")
	reg.Counter("fishd_duplicate_event_total", "幂等拦截的重复事件数")
	reg.Gauge("fishd_duplicate_send_total", "同一事件成功发送超过一次的条数")
	reg.Counter("fishd_degrade_total", "ReAct 降级次数", "reason")
	reg.Counter("fishd_skip_total", "调度跳过次数", "reason")
	reg.Gauge("fishd_reply_latency_seconds", "近 24h 回复延迟分位（秒）", "channel", "quantile")
	reg.Gauge("fishd_db_pool_in_use", "数据库连接池使用中连接数")
	reg.Gauge("fishd_goroutines", "当前 goroutine 数")
	reg.Gauge("fishd_heap_bytes", "当前堆分配字节数")
	return reg
}

// startMetricsServer 启动 /metrics；未启用时返回空 stop。
func startMetricsServer(ctx context.Context, cfg config.Config, resources *infra.Resources, reg *metrics.Registry, logger *zap.Logger) func() {
	if !cfg.Metrics.Enabled || reg == nil {
		return func() {}
	}
	if ctx == nil || ctx.Err() != nil {
		return func() {}
	}
	var db *gorm.DB
	if resources != nil {
		db = resources.DB
	}
	handler := reg.Handler(func() { collectDBMetrics(db, reg) })
	mux := http.NewServeMux()
	mux.Handle("/metrics", handler)
	srv := &http.Server{Addr: cfg.Metrics.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("metrics 服务退出", zap.String("action", "metrics_exit"), zap.Error(err))
		}
	}()
	logger.Info("metrics 已启动", zap.String("action", "metrics_start"), zap.String("addr", cfg.Metrics.Addr))
	return func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = srv.Shutdown(shutdown)
		<-done
	}
}

// collectDBMetrics 抓取时刷新 DB/运行时指标；任何失败只跳过对应指标。
func collectDBMetrics(db *gorm.DB, reg *metrics.Registry) {
	reg.Set("fishd_goroutines", nil, float64(runtime.NumGoroutine()))
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	reg.Set("fishd_heap_bytes", nil, float64(ms.HeapAlloc))
	if db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var pending int64
	if err := db.WithContext(ctx).Raw(`SELECT count(*) FROM events WHERE status='pending'`).Scan(&pending).Error; err == nil {
		reg.Set("fishd_events_pending", nil, float64(pending))
	}
	var age float64
	if err := db.WithContext(ctx).Raw(`
SELECT COALESCE(EXTRACT(EPOCH FROM (now() - min(at))), 0)
  FROM events WHERE status='pending'`).Scan(&age).Error; err == nil {
		reg.Set("fishd_oldest_event_age_seconds", nil, age)
	}
	var dup int64
	if err := db.WithContext(ctx).Raw(`
SELECT count(*) FROM (
  SELECT event_id FROM send_log WHERE ok IS TRUE GROUP BY event_id HAVING count(*) > 1
) t`).Scan(&dup).Error; err == nil {
		reg.Set("fishd_duplicate_send_total", nil, float64(dup))
	}
	var lat []struct {
		Channel string
		P50     float64
		P90     float64
	}
	if err := db.WithContext(ctx).Raw(`
WITH first_ok AS (
  SELECT DISTINCT ON (l.event_id) e.channel, e.at AS event_at, l.at AS sent_at
    FROM send_log l JOIN events e ON e.id = l.event_id
   WHERE l.ok IS TRUE AND e.dir='in' AND e.at >= now() - interval '24 hours'
     AND l.at >= e.at
   ORDER BY l.event_id, l.at ASC
)
SELECT channel,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at-event_at)))::float8 AS p50,
       percentile_cont(0.9) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at-event_at)))::float8 AS p90
  FROM first_ok GROUP BY channel`).Scan(&lat).Error; err == nil {
		for _, row := range lat {
			reg.Set("fishd_reply_latency_seconds", map[string]string{"channel": row.Channel, "quantile": "0.5"}, row.P50)
			reg.Set("fishd_reply_latency_seconds", map[string]string{"channel": row.Channel, "quantile": "0.9"}, row.P90)
		}
	}
	if pool, err := db.DB(); err == nil {
		stats := pool.Stats()
		reg.Set("fishd_db_pool_in_use", nil, float64(stats.InUse))
	}
}

// metricsBus 包住 Publish：返回 false 表示幂等拦截，累加重复事件指标。
type metricsBus struct {
	event.Bus
	reg *metrics.Registry
}

var _ event.Bus = metricsBus{}

func (b metricsBus) Publish(ctx context.Context, ev event.Event) (bool, error) {
	inserted, err := b.Bus.Publish(ctx, ev)
	if err == nil && !inserted && b.reg != nil {
		b.reg.Inc("fishd_duplicate_event_total", nil)
	}
	return inserted, err
}
