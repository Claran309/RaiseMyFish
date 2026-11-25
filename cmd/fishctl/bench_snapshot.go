package main

// `fishctl bench snapshot` 输出单行 JSON，合并数据库积压与 fishd /metrics 快照。
//
// 只读：数据库只执行 SELECT；/metrics 由 fishd 自己维护。该命令不写任何表。

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
)

// BenchSnapshot 是压测脚本对账用的稳定 JSON 结构。
type BenchSnapshot struct {
	CapturedAt time.Time `json:"captured_at"`
	Database   string    `json:"database"`

	EventsPending         int64              `json:"events_pending"`
	OldestEventAgeSeconds float64            `json:"oldest_event_age_seconds"`
	SchedulerPending      float64            `json:"scheduler_pending"`
	SchedulerActive       float64            `json:"scheduler_active"`
	SchedulerWaiting      float64            `json:"scheduler_waiting"`
	SchedulerCooldowns    float64            `json:"scheduler_cooldowns"`
	LeaseRenewTotal       float64            `json:"lease_renew_total"`
	LeaseLostTotal        float64            `json:"lease_lost_total"`
	DuplicateEventTotal   float64            `json:"duplicate_event_total"`
	DuplicateSendTotal    float64            `json:"duplicate_send_total"`
	DBPoolInUse           float64            `json:"db_pool_in_use"`
	DBPoolWaitCount       int64              `json:"db_pool_wait_count"`
	DBPoolWaitDurationSec float64            `json:"db_pool_wait_duration_seconds"`
	Goroutines            float64            `json:"goroutines"`
	HeapBytes             float64            `json:"heap_bytes"`
	SkipReasons           map[string]float64 `json:"skip_reasons,omitempty"`
	DegradeReasons        map[string]float64 `json:"degrade_reasons,omitempty"`
	MetricsAvailable      bool               `json:"metrics_available"`
	MetricsError          string             `json:"metrics_error,omitempty"`
}

func runBenchSnapshot(ctx context.Context, configPath, envPath string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("bench snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	metricsURL := fs.String("metrics-url", "", "fishd /metrics 地址；留空则读取 [metrics] 配置")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("未知参数：%s", strings.Join(fs.Args(), " "))
	}
	cfg, err := config.LoadForStatus(configPath, envPath)
	if err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.TimeoutSeconds)*time.Second)
	defer cancel()
	resources, err := infra.Setup(probe, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = resources.Close(context.Background()) }()
	if err := database.Ready(probe, resources.DB); err != nil {
		return err
	}
	snapshot, err := collectBenchSnapshot(ctx, resources, *metricsURL, cfg, time.Now)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(snapshot)
}

func collectBenchSnapshot(ctx context.Context, resources *infra.Resources, metricsURL string, cfg config.Config, now func() time.Time) (BenchSnapshot, error) {
	if resources == nil || resources.DB == nil {
		return BenchSnapshot{}, errors.New("数据库未就绪")
	}
	snapshot := BenchSnapshot{CapturedAt: now().UTC(), SkipReasons: map[string]float64{}, DegradeReasons: map[string]float64{}}
	if err := resources.DB.WithContext(ctx).Raw("SELECT current_database()").Scan(&snapshot.Database).Error; err != nil {
		return snapshot, fmt.Errorf("读取数据库名失败：%w", err)
	}
	var row struct {
		Pending int64   `gorm:"column:pending"`
		Oldest  float64 `gorm:"column:oldest"`
	}
	if err := resources.DB.WithContext(ctx).Raw(`
SELECT count(*) FILTER (WHERE status='pending') AS pending,
       COALESCE(EXTRACT(EPOCH FROM (now() - min(at) FILTER (WHERE status='pending'))), 0)::float8 AS oldest
  FROM events`).Scan(&row).Error; err != nil {
		return snapshot, fmt.Errorf("读取事件积压失败：%w", err)
	}
	snapshot.EventsPending = row.Pending
	snapshot.OldestEventAgeSeconds = row.Oldest
	if pool, err := resources.DB.DB(); err == nil {
		stats := pool.Stats()
		snapshot.DBPoolWaitCount = stats.WaitCount
		snapshot.DBPoolWaitDurationSec = stats.WaitDuration.Seconds()
		snapshot.DBPoolInUse = float64(stats.InUse)
	}
	snapshot.Goroutines = float64(runtime.NumGoroutine())
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	snapshot.HeapBytes = float64(mem.HeapAlloc)

	if strings.TrimSpace(metricsURL) == "" && cfg.Metrics.Enabled {
		metricsURL = "http://" + cfg.Metrics.Addr + "/metrics"
	}
	if strings.TrimSpace(metricsURL) == "" {
		snapshot.MetricsError = "metrics 未启用"
		return snapshot, nil
	}
	values, err := fetchMetrics(ctx, metricsURL)
	if err != nil {
		snapshot.MetricsError = err.Error()
		return snapshot, nil
	}
	snapshot.MetricsAvailable = true
	applyMetrics(&snapshot, values)
	return snapshot, nil
}

func fetchMetrics(ctx context.Context, url string) (map[string]map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("抓取 metrics 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics 状态码 %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 metrics 失败：%w", err)
	}
	return parsePrometheus(string(data)), nil
}

// parsePrometheus 解析本项目用到的文本格式：metric{label="v"} value。
func parsePrometheus(text string) map[string]map[string]float64 {
	out := make(map[string]map[string]float64)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, ok := splitPromLine(line)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		if out[name] == nil {
			out[name] = make(map[string]float64)
		}
		out[name][labels] = f
	}
	return out
}

func splitPromLine(line string) (name, labels, value string, ok bool) {
	space := strings.IndexByte(line, ' ')
	if space <= 0 {
		return "", "", "", false
	}
	name, rest := line[:space], strings.TrimSpace(line[space+1:])
	value = rest
	if strings.HasPrefix(name, "{") {
		return "", "", "", false
	}
	if open := strings.IndexByte(name, '{'); open >= 0 {
		if !strings.HasSuffix(name, "}") {
			return "", "", "", false
		}
		labels = name[open+1 : len(name)-1]
		name = name[:open]
	}
	if name == "" || value == "" {
		return "", "", "", false
	}
	return name, normalizeLabels(labels), value, true
}

func normalizeLabels(raw string) string {
	if raw == "" {
		return ""
	}
	parts := splitLabels(raw)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

func splitLabels(raw string) []string {
	var parts []string
	start, inQuote, escaped := 0, false, false
	for i, r := range raw {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			parts = append(parts, raw[start:i])
			start = i + 1
		}
	}
	parts = append(parts, raw[start:])
	return parts
}

func applyMetrics(snapshot *BenchSnapshot, values map[string]map[string]float64) {
	snapshot.SchedulerPending = metricValue(values, "fishd_scheduler_pending")
	snapshot.SchedulerActive = metricValue(values, "fishd_scheduler_active")
	snapshot.SchedulerWaiting = metricValue(values, "fishd_scheduler_waiting")
	snapshot.SchedulerCooldowns = metricValue(values, "fishd_scheduler_cooldowns")
	snapshot.LeaseRenewTotal = metricValue(values, "fishd_lease_renew_total")
	snapshot.LeaseLostTotal = metricValue(values, "fishd_lease_lost_total")
	snapshot.DuplicateEventTotal = metricValue(values, "fishd_duplicate_event_total")
	snapshot.DuplicateSendTotal = metricValue(values, "fishd_duplicate_send_total")
	snapshot.DBPoolInUse = metricValue(values, "fishd_db_pool_in_use")
	snapshot.Goroutines = metricValue(values, "fishd_goroutines")
	snapshot.HeapBytes = metricValue(values, "fishd_heap_bytes")
	snapshot.SkipReasons = labelValues(values, "fishd_skip_total", "reason")
	snapshot.DegradeReasons = labelValues(values, "fishd_degrade_total", "reason")
}

func metricValue(values map[string]map[string]float64, name string) float64 {
	series := values[name]
	if len(series) == 0 {
		return 0
	}
	if value, ok := series[""]; ok {
		return value
	}
	for _, value := range series {
		return value
	}
	return 0
}

func labelValues(values map[string]map[string]float64, name, label string) map[string]float64 {
	out := make(map[string]float64)
	for key, value := range values[name] {
		prefix := label + `="`
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		raw := strings.TrimPrefix(key, prefix)
		if end := strings.LastIndex(raw, `"`); end >= 0 {
			raw = raw[:end]
		}
		out[raw] = value
	}
	return out
}
