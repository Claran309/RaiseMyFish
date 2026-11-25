// bench/report 生成线上只读运维报告：执行 bench/sql 下的 SQL，输出 CSV 与 Markdown。
//
// 安全边界：
//   - 只读：每个查询都跑在 `SET TRANSACTION READ ONLY` 事务里，写操作会被 PG 拒绝；
//   - 不导出正文/昵称：SQL 只做计数与聚合，不 SELECT text/from_name；
//   - 不连 bench 库：默认只读 DSN 来自 FISH_DB_DSN（生产），需要显式 --dsn-env 切换。
//
// 用法：
//
//	go run ./bench/report --out bench/out/report-<ts> --report docs/reports/30day-ops.md
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"RaiseMyFish/internal/database"
	"gorm.io/gorm"
)

type namedQuery struct {
	Name string
	File string
}

var queries = []namedQuery{
	{"daily_events", "01_daily_events.sql"},
	{"channel_replies", "02_channel_replies.sql"},
	{"daily_cost", "03_daily_cost.sql"},
	{"dead_distribution", "04_dead_distribution.sql"},
	{"status_distribution", "05_status_distribution.sql"},
	{"minute_peak", "06_minute_peak.sql"},
	{"daily_latency", "07_daily_latency.sql"},
	{"dead_attribution", "08_dead_attribution.sql"},
	{"send_failures", "09_send_failures.sql"},
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("bench-report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dsnEnv := fs.String("dsn-env", "FISH_DB_DSN", "只读 DSN 所在环境变量")
	sqlDir := fs.String("sql-dir", "bench/sql", "SQL 目录")
	outDir := fs.String("out", "", "输出目录（CSV 与基线 JSON）")
	reportPath := fs.String("report", "", "Markdown 报告路径")
	logsDir := fs.String("logs", "logs", "fishd 日志目录（用于重启与降级统计）")
	if err := fs.Parse(args); err != nil {
		return errors.New("参数无效")
	}
	if fs.NArg() != 0 || *outDir == "" {
		return errors.New("用法：bench-report --out <目录> [--report <md>]")
	}
	dsn := strings.TrimSpace(os.Getenv(*dsnEnv))
	if dsn == "" {
		return fmt.Errorf("环境变量 %s 为空", *dsnEnv)
	}
	db, err := database.Open(ctx, dsn, 5*time.Second)
	if err != nil {
		return err
	}
	defer database.Close(db)
	if err := os.MkdirAll(filepath.Join(*outDir, "csv"), 0o700); err != nil {
		return err
	}

	baseline, err := baselineCounts(ctx, db)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "baseline.json"), append(raw, '\n'), 0o600); err != nil {
		return err
	}

	results := map[string]*table{}
	for _, q := range queries {
		path := filepath.Join(*sqlDir, q.File)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取 %s：%w", path, err)
		}
		tbl, err := runReadOnly(ctx, db, string(data))
		if err != nil {
			return fmt.Errorf("执行 %s：%w", q.Name, err)
		}
		results[q.Name] = tbl
		if err := writeCSV(filepath.Join(*outDir, "csv", q.Name+".csv"), tbl); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: %d 行\n", q.Name, len(tbl.Rows))
	}
	if *reportPath != "" {
		logs, err := scanLogs(*logsDir)
		if err != nil {
			fmt.Fprintf(stdout, "日志统计跳过：%v\n", err)
		}
		if err := writeMarkdown(*reportPath, *outDir, results, logs); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "报告：%s\n", *reportPath)
	}
	return nil
}

type table struct {
	Columns []string
	Rows    [][]string
}

// runReadOnly 在一个只读事务里执行查询，动态列转成字符串。
func runReadOnly(ctx context.Context, db *gorm.DB, query string) (*table, error) {
	var out table
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET TRANSACTION READ ONLY").Error; err != nil {
			return err
		}
		rows, err := tx.Raw(query).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		out.Columns = cols
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			row := make([]string, len(cols))
			for i, v := range values {
				row[i] = formatValue(v)
			}
			out.Rows = append(out.Rows, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case []byte:
		return string(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

func writeCSV(path string, tbl *table) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	if err := w.Write(tbl.Columns); err != nil {
		return err
	}
	for _, row := range tbl.Rows {
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func baselineCounts(ctx context.Context, db *gorm.DB) (map[string]int64, error) {
	queries := map[string]string{
		"events":       "SELECT count(*) FROM events",
		"send_log":     "SELECT count(*) FROM send_log",
		"llm_usage":    "SELECT count(*) FROM llm_usage",
		"memory_valid": "SELECT count(*) FROM memory_records WHERE valid_to IS NULL AND NOT archived",
	}
	names := make([]string, 0, len(queries))
	for name := range queries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[string]int64, len(queries))
	for _, name := range names {
		tbl, err := runReadOnly(ctx, db, queries[name])
		if err != nil {
			return nil, err
		}
		if len(tbl.Rows) != 1 || len(tbl.Rows[0]) != 1 {
			return nil, fmt.Errorf("基线计数 %s 返回异常", name)
		}
		n, err := strconv.ParseInt(tbl.Rows[0][0], 10, 64)
		if err != nil {
			return nil, err
		}
		out[name] = n
	}
	out["captured_at_unix"] = time.Now().Unix()
	return out, nil
}

// writeMarkdown 生成报告：表格 + 3 张轻量 SVG。
type logStats struct {
	StartsByDay    map[string]int `json:"starts_by_day"`
	TotalStarts    int            `json:"total_starts"`
	DegradedTotal  int            `json:"degraded_total"`
	DegradedReason map[string]int `json:"degraded_by_reason"`
	DegradedAction map[string]int `json:"degraded_by_action"`
	Files          int            `json:"files"`
}

// scanLogs 统计 fishd 启动次数与 ReAct 降级原因；日志缺失时返回空统计。
func scanLogs(dir string) (logStats, error) {
	out := logStats{
		StartsByDay:    map[string]int{},
		DegradedReason: map[string]int{},
		DegradedAction: map[string]int{},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		file, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return out, err
		}
		out.Files++
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		for scanner.Scan() {
			var line struct {
				TS       string `json:"ts"`
				Action   string `json:"action"`
				Degraded bool   `json:"degraded"`
				Reason   string `json:"degrade_reason"`
			}
			if json.Unmarshal(scanner.Bytes(), &line) != nil {
				continue
			}
			if line.Action == "heartbeat_start" {
				out.TotalStarts++
				day := line.TS
				if len(day) >= 10 {
					day = day[:10]
				}
				out.StartsByDay[day]++
			}
			if line.Degraded {
				out.DegradedTotal++
				reason := line.Reason
				if reason == "" {
					reason = "unknown"
				}
				out.DegradedReason[reason]++
				action := line.Action
				if action == "" {
					action = "unknown"
				}
				out.DegradedAction[action]++
			}
		}
		closeErr := file.Close()
		if err := scanner.Err(); err != nil {
			return out, err
		}
		if closeErr != nil {
			return out, closeErr
		}
	}
	return out, nil
}

func writeMarkdown(path, outDir string, results map[string]*table, logs logStats) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return err
	}
	var b strings.Builder
	b.WriteString("# RaiseMyFish · 30 天线上运行报告\n\n")
	b.WriteString("> 数据来源：生产库只读事务（每条查询 `SET TRANSACTION READ ONLY`）。\n")
	b.WriteString("> 原始 CSV：" + filepath.ToSlash(outDir) + "/csv/。所有数字可由 `bench/sql/` 复现。\n\n")

	baselinePath := filepath.Join(outDir, "baseline.json")
	if data, err := os.ReadFile(baselinePath); err == nil {
		var baseline map[string]int64
		if json.Unmarshal(data, &baseline) == nil {
			b.WriteString("## 基线计数\n\n")
			b.WriteString("| 表 | 行数 |\n|---|---:|\n")
			for _, name := range []string{"events", "send_log", "llm_usage", "memory_valid"} {
				fmt.Fprintf(&b, "| %s | %d |\n", name, baseline[name])
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## 每日事件量\n\n")
	appendChart(&b, filepath.Join(filepath.Dir(path), "assets"), "daily-events", "每日入站事件量", results["daily_events"], chartDailyEvents)
	appendTable(&b, results["daily_events"])

	b.WriteString("## 通道回复与失败\n\n")
	appendTable(&b, results["channel_replies"])

	b.WriteString("## 每日成本与 token\n\n")
	appendChart(&b, filepath.Join(filepath.Dir(path), "assets"), "daily-cost", "每日费用（USD）", results["daily_cost"], chartDailyCost)
	appendTable(&b, results["daily_cost"])

	b.WriteString("## 每日延迟分位\n\n")
	appendChart(&b, filepath.Join(filepath.Dir(path), "assets"), "daily-latency", "每日 P90 延迟（秒）", results["daily_latency"], chartDailyLatency)
	appendTable(&b, results["daily_latency"])

	b.WriteString("## 分钟峰值\n\n")
	appendTable(&b, results["minute_peak"])

	b.WriteString("## dead 分布\n\n")
	appendTable(&b, results["dead_distribution"])

	b.WriteString("## dead 归因覆盖率\n\n")
	appendTable(&b, results["dead_attribution"])

	b.WriteString("## 失败发送\n\n")
	appendTable(&b, results["send_failures"])

	b.WriteString("## 状态分布\n\n")
	appendTable(&b, results["status_distribution"])

	b.WriteString("## 进程重启与降级（日志）\n\n")
	if logs.Files == 0 {
		b.WriteString("_日志目录为空，未统计。_\n\n")
	} else {
		fmt.Fprintf(&b, "- 日志文件：%d 个；fishd 启动（heartbeat_start）：**%d 次**\n", logs.Files, logs.TotalStarts)
		days := make([]string, 0, len(logs.StartsByDay))
		for day := range logs.StartsByDay {
			days = append(days, day)
		}
		sort.Strings(days)
		for _, day := range days {
			fmt.Fprintf(&b, "  - %s：%d 次\n", day, logs.StartsByDay[day])
		}
		fmt.Fprintf(&b, "- ReAct 降级：**%d 次**\n", logs.DegradedTotal)
		for _, item := range topN(logs.DegradedReason, 5) {
			fmt.Fprintf(&b, "  - 原因 %s：%d 次\n", item.Key, item.N)
		}
		for _, item := range topN(logs.DegradedAction, 5) {
			fmt.Fprintf(&b, "  - 来源 %s：%d 次\n", item.Key, item.N)
		}
		b.WriteString("\n")
	}

	appendDefects(&b, results, logs)

	b.WriteString("\n## 口径说明\n\n")
	b.WriteString("- 延迟口径：`events.at → 首次 send_log.ok`，未改变历史定义。\n")
	b.WriteString("- 成本：`llm_usage.cost_usd` 求和；`unknown_calls` 为未配置报价的调用数，不能当 0 成本。\n")
	b.WriteString("- dead：`events.status='dead'` 且近 30 天；`event_errors` 只做原因分类。\n")
	b.WriteString("- 所有时间按 UTC 切日。\n")

	return os.WriteFile(path, []byte(b.String()), 0o600)
}

type keyCount struct {
	Key string
	N   int
}

func topN(m map[string]int, n int) []keyCount {
	out := make([]keyCount, 0, len(m))
	for key, v := range m {
		out = append(out, keyCount{Key: key, N: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// appendDefects 生成「真实缺陷清单」：只写能从表里算出来的事实。
func appendDefects(b *strings.Builder, results map[string]*table, logs logStats) {
	b.WriteString("## 真实缺陷清单\n\n")
	dead := map[string]int{}
	if tbl := results["status_distribution"]; tbl != nil {
		idx := columnIndex(tbl.Columns)
		for _, row := range tbl.Rows {
			if row[idx["dir"]] == "in" && row[idx["status"]] == "dead" {
				n, _ := strconv.Atoi(row[idx["n"]])
				dead[row[idx["channel"]]] = n
			}
		}
	}
	if tbl := results["channel_replies"]; tbl != nil {
		idx := columnIndex(tbl.Columns)
		for _, row := range tbl.Rows {
			ch := row[idx["channel"]]
			received, _ := strconv.Atoi(row[idx["received"]])
			if received > 0 && dead[ch] > 0 {
				rate := float64(dead[ch]) * 100 / float64(received)
				severity := "需要关注"
				if rate >= 10 {
					severity = "止血优先级最高"
				}
				fmt.Fprintf(b, "- **%s dead 率 %.1f%%**（%d/%d）：%s。\n",
					ch, rate, dead[ch], received, severity)
			}
		}
	}
	if tbl := results["dead_attribution"]; tbl != nil && len(tbl.Rows) == 1 {
		without := tbl.Rows[0][2]
		fmt.Fprintf(b, "- **dead 归因缺失**：%s 条 dead 事件没有 `event_errors` 记录（历史数据早于 0026 迁移），\n  当前无法回答「死在哪一环」；新事件必须保证 stage/error_kind 落库。\n", without)
	}
	if logs.TotalStarts > 1 {
		days := len(logs.StartsByDay)
		if days == 0 {
			days = 1
		}
		fmt.Fprintf(b, "- **进程重启频繁**：日志覆盖 %d 天出现 %d 次启动（约 %.1f 次/天）；\n  日志中没有 watchdog 重启记录（watchdog 未启用），这些是人工/脚本重启，不是自动恢复。\n",
			days, logs.TotalStarts, float64(logs.TotalStarts)/float64(days))
	}
	if logs.DegradedTotal > 0 {
		top := topN(logs.DegradedReason, 1)
		if len(top) == 1 {
			fmt.Fprintf(b, "- **ReAct 降级 %d 次**，最大原因是 `%s`（%d 次）；降级会回落单步，\n  回答质量与工具查证能力都会下降，需要按原因分别治理。\n",
				logs.DegradedTotal, top[0].Key, top[0].N)
		}
	}
	b.WriteString("\n")
}

func appendTable(b *strings.Builder, tbl *table) {
	if tbl == nil || len(tbl.Rows) == 0 {
		b.WriteString("_无数据_\n\n")
		return
	}
	b.WriteString("| " + strings.Join(tbl.Columns, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(tbl.Columns)) + "\n")
	limit := len(tbl.Rows)
	if limit > 100 {
		limit = 100
	}
	for _, row := range tbl.Rows[:limit] {
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	if len(tbl.Rows) > limit {
		fmt.Fprintf(b, "\n_其余 %d 行见 CSV。_\n", len(tbl.Rows)-limit)
	}
	b.WriteString("\n")
}

// series 是折线图的一条线。
type series struct {
	Label  string
	Points []point
}

type point struct {
	X float64
	Y float64
}

type chartKind int

const (
	chartDailyEvents chartKind = iota
	chartDailyCost
	chartDailyLatency
)

// appendChart 生成 SVG 文件并写入 Markdown 图片引用；数据不足时不画假图。
func appendChart(b *strings.Builder, assetsDir, slug, title string, tbl *table, kind chartKind) {
	if tbl == nil || len(tbl.Rows) == 0 {
		b.WriteString("_无数据，跳过图表_\n\n")
		return
	}
	var (
		svg string
		ok  bool
	)
	switch kind {
	case chartDailyEvents:
		svg, ok = dailyEventsSVG(tbl, title)
	case chartDailyCost:
		svg, ok = singleSVG(tbl, "cost_usd", title)
	case chartDailyLatency:
		svg, ok = latencySVG(tbl, title)
	}
	if !ok {
		b.WriteString("_样本不足，跳过图表_\n\n")
		return
	}
	if err := os.MkdirAll(assetsDir, 0o700); err == nil {
		if err := os.WriteFile(filepath.Join(assetsDir, slug+".svg"), []byte(svg), 0o600); err == nil {
			fmt.Fprintf(b, "![%s](assets/%s.svg)\n\n", title, slug)
			return
		}
	}
	// 写文件失败时退回内联代码块，至少不丢图。
	b.WriteString("```svg\n" + svg + "\n```\n\n")
}

func dailyEventsSVG(tbl *table, title string) (string, bool) {
	idx := columnIndex(tbl.Columns)
	days := map[string]float64{}
	seriesMap := map[string]map[string]float64{"live": {}, "comment": {}, "dm": {}}
	for _, row := range tbl.Rows {
		if row[idx["dir"]] != "in" {
			continue
		}
		day := dayKey(row[idx["day"]])
		n, _ := strconv.ParseFloat(row[idx["n"]], 64)
		ch := row[idx["channel"]]
		if _, ok := seriesMap[ch]; !ok {
			continue
		}
		seriesMap[ch][day] += n
		days[day] = 1
	}
	keys := sortedKeys(days)
	if len(keys) < 2 {
		return "", false
	}
	order := []string{"live", "comment", "dm"}
	lines := make([]series, 0, len(order))
	for _, ch := range order {
		line := series{Label: ch}
		for i, day := range keys {
			line.Points = append(line.Points, point{X: float64(i), Y: seriesMap[ch][day]})
		}
		lines = append(lines, line)
	}
	return renderLineChart(keys, lines, title), true
}

func singleSVG(tbl *table, field, title string) (string, bool) {
	idx := columnIndex(tbl.Columns)
	var keys []string
	var values []float64
	for _, row := range tbl.Rows {
		v, err := strconv.ParseFloat(row[idx[field]], 64)
		if err != nil {
			v = 0
		}
		keys = append(keys, dayKey(row[idx["day"]]))
		values = append(values, v)
	}
	if len(keys) < 2 {
		return "", false
	}
	line := series{Label: field}
	for i, v := range values {
		line.Points = append(line.Points, point{X: float64(i), Y: v})
	}
	return renderLineChart(keys, []series{line}, title), true
}

func latencySVG(tbl *table, title string) (string, bool) {
	idx := columnIndex(tbl.Columns)
	days := map[string]float64{}
	byChannel := map[string]map[string]float64{"live": {}, "comment": {}, "dm": {}}
	for _, row := range tbl.Rows {
		ch := row[idx["channel"]]
		if _, ok := byChannel[ch]; !ok {
			continue
		}
		day := dayKey(row[idx["day"]])
		v, err := strconv.ParseFloat(row[idx["p90_ms"]], 64)
		if err != nil {
			continue
		}
		byChannel[ch][day] = v
		days[day] = 1
	}
	keys := sortedKeys(days)
	if len(keys) < 2 {
		return "", false
	}
	lines := make([]series, 0, 3)
	for _, ch := range []string{"live", "comment", "dm"} {
		line := series{Label: ch}
		for i, day := range keys {
			line.Points = append(line.Points, point{X: float64(i), Y: byChannel[ch][day] / 1000})
		}
		lines = append(lines, line)
	}
	return renderLineChart(keys, lines, title), true
}

func columnIndex(cols []string) map[string]int {
	out := make(map[string]int, len(cols))
	for i, c := range cols {
		out[c] = i
	}
	return out
}

func dayKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 10 {
		return raw[:10]
	}
	return raw
}

func sortedKeys(set map[string]float64) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var palette = []string{"#4ea1ff", "#f5a623", "#50c878"}

// renderLineChart 输出无依赖 SVG；y 轴从 0 起，避免夸大波动。
func renderLineChart(labels []string, lines []series, title string) string {
	const (
		width  = 720
		height = 260
		left   = 48
		right  = 16
		top    = 28
		bottom = 42
	)
	maxY := 0.0
	for _, line := range lines {
		for _, p := range line.Points {
			if p.Y > maxY {
				maxY = p.Y
			}
		}
	}
	if maxY <= 0 {
		maxY = 1
	}
	plotW := float64(width - left - right)
	plotH := float64(height - top - bottom)
	xAt := func(i int) float64 {
		if len(labels) <= 1 {
			return float64(left)
		}
		return float64(left) + plotW*float64(i)/float64(len(labels)-1)
	}
	yAt := func(v float64) float64 {
		return float64(top) + plotH*(1-v/maxY)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, width, height, width, height)
	b.WriteString(`<rect width="100%" height="100%" fill="#0f1115"/>`)
	fmt.Fprintf(&b, `<text x="%d" y="18" fill="#e6e6e6" font-size="13" font-family="sans-serif">%s</text>`, left, escapeXML(title))
	// y 轴刻度
	for i := 0; i <= 4; i++ {
		v := maxY * float64(i) / 4
		y := yAt(v)
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#232833"/>`, left, y, width-right, y)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" fill="#8b93a1" font-size="10" text-anchor="end" font-family="sans-serif">%s</text>`,
			left-6, y+3, trimFloat(v))
	}
	// x 轴标签：最多 8 个
	step := 1
	if len(labels) > 8 {
		step = int(math.Ceil(float64(len(labels)) / 8))
	}
	for i, label := range labels {
		if i%step != 0 {
			continue
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" fill="#8b93a1" font-size="10" text-anchor="middle" font-family="sans-serif">%s</text>`,
			xAt(i), height-16, escapeXML(label))
	}
	// 折线
	for idx, line := range lines {
		color := palette[idx%len(palette)]
		var d strings.Builder
		for i, p := range line.Points {
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&d, "%s%.1f %.1f ", cmd, xAt(i), yAt(p.Y))
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2"/>`, strings.TrimSpace(d.String()), color)
		fmt.Fprintf(&b, `<text x="%d" y="%d" fill="%s" font-size="11" font-family="sans-serif">%s</text>`,
			left+idx*90, height-2, color, escapeXML(line.Label))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func trimFloat(v float64) string {
	if v >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	if v >= 10 {
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func escapeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
