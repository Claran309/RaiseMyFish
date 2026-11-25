// analyze 汇总 bench/raw 下的 staging 运行结果，输出每组中位数与 Markdown 表。
//
// 只读本地原始 JSON/日志，不连接数据库；原始数据目录已被 gitignore。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type meta struct {
	RunID           string  `json:"run_id"`
	Scenario        string  `json:"scenario"`
	Rate            int     `json:"rate"`
	Mode            string  `json:"mode"`
	WarmupSeconds   int     `json:"warmup_seconds"`
	DurationSeconds int     `json:"duration_seconds"`
	Workers         int     `json:"workers"`
	MockLatency     string  `json:"mock_latency"`
	Commit          string  `json:"commit"`
	Recovery        *string `json:"recovery"`
}

type counts struct {
	Events         int64 `json:"events"`
	Inbound        int64 `json:"inbound"`
	SendLog        int64 `json:"send_log"`
	SendOK         int64 `json:"send_ok"`
	SendFailed     int64 `json:"send_failed"`
	LLMUsage       int64 `json:"llm_usage"`
	Dead           int64 `json:"dead"`
	Skipped        int64 `json:"skipped"`
	DuplicateSends int64 `json:"duplicate_sends"`
	Unfinished     int64 `json:"unfinished"`
}

type latency struct {
	Channel string  `json:"channel"`
	Samples int     `json:"samples"`
	P50     float64 `json:"p50_s"`
	P90     float64 `json:"p90_s"`
	P99     float64 `json:"p99_s"`
	Max     float64 `json:"max_s"`
}

type snapshot struct {
	EventsPending     int64              `json:"events_pending"`
	OldestEventAge    float64            `json:"oldest_event_age_seconds"`
	SchedulerPending  float64            `json:"scheduler_pending"`
	SchedulerActive   float64            `json:"scheduler_active"`
	SchedulerWaiting  float64            `json:"scheduler_waiting"`
	SchedulerCooldown float64            `json:"scheduler_cooldowns"`
	LeaseRenew        float64            `json:"lease_renew_total"`
	LeaseLost         float64            `json:"lease_lost_total"`
	DuplicateEvent    float64            `json:"duplicate_event_total"`
	DuplicateSend     float64            `json:"duplicate_send_total"`
	DBWaitCount       int64              `json:"db_pool_wait_count"`
	DBWaitSeconds     float64            `json:"db_pool_wait_duration_seconds"`
	Goroutines        float64            `json:"goroutines"`
	HeapBytes         float64            `json:"heap_bytes"`
	SkipReasons       map[string]float64 `json:"skip_reasons"`
	MetricsAvailable  bool               `json:"metrics_available"`
}

type mockStats struct {
	Requests int64 `json:"requests"`
	Errors   int64 `json:"errors"`
	Timeouts int64 `json:"timeouts"`
	Peak     int64 `json:"peak"`
}

type run struct {
	Meta       meta
	Counts     counts
	Latency    map[string]latency
	Snapshots  []snapshot
	Mock       mockStats
	Injections int
}

type group struct {
	Scenario  string  `json:"scenario"`
	Rate      int     `json:"rate"`
	Runs      int     `json:"runs"`
	Inbound   int64   `json:"inbound_median"`
	SendOK    int64   `json:"send_ok_median"`
	Failed    int64   `json:"send_failed_median"`
	Dead      int64   `json:"dead_median"`
	Duplicate int64   `json:"duplicate_median"`
	P50       float64 `json:"live_p50_median_s"`
	P90       float64 `json:"live_p90_median_s"`
	P99       float64 `json:"live_p99_median_s"`
	MaxPend   float64 `json:"max_events_pending"`
	Oldest    float64 `json:"max_oldest_event_age_s"`
	PeakLLM   int64   `json:"mock_peak_median"`
}

func main() {
	raw := flag.String("raw", "bench/raw", "原始数据目录")
	out := flag.String("out", "", "可选 JSON 输出路径")
	flag.Parse()
	runs, err := loadRuns(*raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	groups := groupRuns(runs)
	printMarkdown(groups)
	if *out != "" {
		data, err := json.MarshalIndent(map[string]any{"runs": runs, "groups": groups}, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, data, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func loadRuns(root string) ([]run, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var runs []run
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		var r run
		if err := readJSON(filepath.Join(dir, "meta.json"), &r.Meta); err != nil {
			continue
		}
		// 30 分钟 soak 不进容量三组中位数；单独在报告里引用。
		if r.Meta.DurationSeconds >= 1800 {
			continue
		}
		// 旧冒烟/缺少 counts.json 的运行不进入报告，避免零值污染中位数。
		if _, err := os.Stat(filepath.Join(dir, "counts.json")); err != nil {
			continue
		}
		r.Latency = map[string]latency{}
		_ = readJSON(filepath.Join(dir, "counts.json"), &r.Counts)
		_ = readJSON(filepath.Join(dir, "mock-stats.json"), &r.Mock)
		var latencyRows []latency
		if err := readJSON(filepath.Join(dir, "latency.json"), &latencyRows); err == nil {
			for _, row := range latencyRows {
				r.Latency[row.Channel] = row
			}
		} else {
			r.Latency = parseLatencyText(filepath.Join(dir, "latency.txt"))
		}
		r.Snapshots = readSnapshots(filepath.Join(dir, "snapshots.jsonl"))
		r.Injections = countLines(filepath.Join(dir, "inject.jsonl"))
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].Meta.Scenario != runs[j].Meta.Scenario {
			return runs[i].Meta.Scenario < runs[j].Meta.Scenario
		}
		if runs[i].Meta.Rate != runs[j].Meta.Rate {
			return runs[i].Meta.Rate < runs[j].Meta.Rate
		}
		return runs[i].Meta.RunID < runs[j].Meta.RunID
	})
	return runs, nil
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func readSnapshots(path string) []snapshot {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []snapshot
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var s snapshot
		if json.Unmarshal(scanner.Bytes(), &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

var countLine = regexp.MustCompile(`^\s*([a-z_]+)\s*\|\s*([0-9]+)`)

func parseCountsText(path string) counts {
	var c counts
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := countLine.FindStringSubmatch(scanner.Text())
		if len(m) != 3 {
			continue
		}
		n, _ := strconv.ParseInt(m[2], 10, 64)
		switch m[1] {
		case "events":
			c.Events = n
		case "send_log":
			c.SendLog = n
		case "llm_usage":
			c.LLMUsage = n
		}
	}
	return c
}

var latencyLine = regexp.MustCompile(`^\s*(live|comment|dm)\s*\|\s*([0-9]+)\s*\|\s*([0-9.]+)\s*\|\s*([0-9.]+)\s*\|\s*([0-9.]+)\s*\|\s*([0-9.]+)`)

func parseLatencyText(path string) map[string]latency {
	out := map[string]latency{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := latencyLine.FindStringSubmatch(scanner.Text())
		if len(m) != 7 {
			continue
		}
		samples, _ := strconv.Atoi(m[2])
		p50, _ := strconv.ParseFloat(m[3], 64)
		p90, _ := strconv.ParseFloat(m[4], 64)
		p99, _ := strconv.ParseFloat(m[5], 64)
		max, _ := strconv.ParseFloat(m[6], 64)
		out[m[1]] = latency{Channel: m[1], Samples: samples, P50: p50, P90: p90, P99: p99, Max: max}
	}
	return out
}

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			n++
		}
	}
	return n
}

func groupRuns(runs []run) []group {
	type key struct {
		scenario string
		rate     int
	}
	byKey := map[key][]run{}
	var order []key
	for _, r := range runs {
		k := key{r.Meta.Scenario, r.Meta.Rate}
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], r)
	}
	var groups []group
	for _, k := range order {
		list := byKey[k]
		g := group{Scenario: k.scenario, Rate: k.rate, Runs: len(list)}
		g.Inbound = medianInt(list, func(r run) int64 { return r.Counts.Inbound })
		g.SendOK = medianInt(list, func(r run) int64 { return r.Counts.SendOK })
		g.Failed = medianInt(list, func(r run) int64 { return r.Counts.SendFailed })
		g.Dead = medianInt(list, func(r run) int64 { return r.Counts.Dead })
		g.Duplicate = medianInt(list, func(r run) int64 { return r.Counts.DuplicateSends })
		g.PeakLLM = medianInt(list, func(r run) int64 { return r.Mock.Peak })
		g.P50 = medianFloat(list, func(r run) float64 { return r.Latency["live"].P50 })
		g.P90 = medianFloat(list, func(r run) float64 { return r.Latency["live"].P90 })
		g.P99 = medianFloat(list, func(r run) float64 { return r.Latency["live"].P99 })
		g.MaxPend = maxFloat(list, func(r run) float64 {
			return maxSnapshot(r.Snapshots, func(s snapshot) float64 { return float64(s.EventsPending) })
		})
		g.Oldest = maxFloat(list, func(r run) float64 {
			return maxSnapshot(r.Snapshots, func(s snapshot) float64 { return s.OldestEventAge })
		})
		groups = append(groups, g)
	}
	return groups
}

func medianInt(list []run, pick func(run) int64) int64 {
	values := make([]int64, 0, len(list))
	for _, r := range list {
		values = append(values, pick(r))
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	if len(values) == 0 {
		return 0
	}
	return values[len(values)/2]
}

func medianFloat(list []run, pick func(run) float64) float64 {
	values := make([]float64, 0, len(list))
	for _, r := range list {
		values = append(values, pick(r))
	}
	sort.Float64s(values)
	if len(values) == 0 {
		return 0
	}
	return values[len(values)/2]
}

func maxFloat(list []run, pick func(run) float64) float64 {
	var max float64
	for _, r := range list {
		if v := pick(r); v > max {
			max = v
		}
	}
	return max
}

func maxSnapshot(list []snapshot, pick func(snapshot) float64) float64 {
	var max float64
	for _, s := range list {
		if v := pick(s); v > max {
			max = v
		}
	}
	return max
}

func printMarkdown(groups []group) {
	fmt.Println("| Scenario | Rate/min | Runs | Inbound | Send OK | Failed | Dead | Dup | Live P50 | Live P90 | Live P99 | Max pending | Max oldest age | LLM peak |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, g := range groups {
		fmt.Printf("| %s | %d | %d | %d | %d | %d | %d | %d | %.3fs | %.3fs | %.3fs | %.0f | %.1fs | %d |\n",
			g.Scenario, g.Rate, g.Runs, g.Inbound, g.SendOK, g.Failed, g.Dead, g.Duplicate, g.P50, g.P90, g.P99, g.MaxPend, g.Oldest, g.PeakLLM)
	}
}
