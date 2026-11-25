// fishload 按 at 顺序把 JSONL 事件回放到事件总线。
//
// 边界：
//   - 只调用 event.Bus.Publish，不直接写 send_log，也不触达任何平台；
//   - 生产库禁止使用：启动时拒绝非 raisemyfish_bench 数据库；
//   - 固定速率或突发模式由本地时钟控制，记录每条事件的注入时间。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	eventbus "RaiseMyFish/internal/event/bus"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/store"
)

// benchDatabase 是唯一允许写入的库名。
const benchDatabase = "raisemyfish_bench"

type inputRecord struct {
	ID             string          `json:"id"`
	Channel        event.Channel   `json:"channel"`
	Dir            event.Dir       `json:"dir"`
	Session        string          `json:"session"`
	FromID         string          `json:"from_id,omitempty"`
	FromName       string          `json:"from_name,omitempty"`
	Text           string          `json:"text"`
	Private        bool            `json:"private,omitempty"`
	ReplyTo        string          `json:"reply_to,omitempty"`
	At             time.Time       `json:"at"`
	Meta           json.RawMessage `json:"meta,omitempty"`
	CollectMode    string          `json:"collect_mode,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
}

func (r inputRecord) event() event.Event {
	return event.Event{
		ID: r.ID, Channel: r.Channel, Dir: r.Dir, Session: r.Session,
		FromID: r.FromID, FromName: r.FromName, Text: r.Text, Private: r.Private,
		ReplyTo: r.ReplyTo, At: r.At, Status: event.StatusPending, Meta: r.Meta,
		CollectMode: r.CollectMode, ConversationID: r.ConversationID,
	}
}

// publisher 是 fishload 依赖的最小总线接口；真实实现是 *eventbus.Bus。
type publisher interface {
	Publish(context.Context, event.Event) (bool, error)
}

type options struct {
	ConfigPath  string
	EnvPath     string
	File        string
	RecordPath  string
	Mode        string
	RatePerMin  int
	BurstCount  int
	BurstWindow time.Duration
	BurstGap    time.Duration
	Limit       int
	RewriteAt   bool
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	events, err := loadEvents(opts.File, opts.Limit)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return errors.New("输入没有可回放的事件")
	}
	cfg, err := config.LoadForMigrate(opts.ConfigPath, opts.EnvPath)
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
	var databaseName string
	if err := resources.DB.WithContext(probe).Raw("SELECT current_database()").Scan(&databaseName).Error; err != nil {
		return fmt.Errorf("读取数据库名失败：%w", err)
	}
	if databaseName != benchDatabase {
		return fmt.Errorf("拒绝写入 %q：fishload 只允许 %s", databaseName, benchDatabase)
	}
	repo, err := database.NewEventRepo(resources.DB)
	if err != nil {
		return err
	}
	bus, err := eventbus.New(repo, 0)
	if err != nil {
		return err
	}
	var record io.WriteCloser
	if opts.RecordPath != "" {
		record, err = createRecordFile(opts.RecordPath)
		if err != nil {
			return err
		}
		defer record.Close()
	}
	summary, err := replay(ctx, events, opts, bus, record, time.Now, wait)
	if err != nil {
		return err
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("fishload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := options{}
	fs.StringVar(&opts.ConfigPath, "config", "config/fishd.bench.toml", "压测配置")
	fs.StringVar(&opts.EnvPath, "env-file", ".env", "本地环境文件")
	fs.StringVar(&opts.File, "file", "", "输入 JSONL（按 at 排序回放）")
	fs.StringVar(&opts.RecordPath, "record", "", "注入记录 JSONL（可选）")
	fs.StringVar(&opts.Mode, "mode", "fixed", "fixed 或 burst")
	fs.IntVar(&opts.RatePerMin, "rate", 24, "固定模式每分钟事件数")
	fs.IntVar(&opts.BurstCount, "burst-count", 100, "每轮突发条数")
	fs.DurationVar(&opts.BurstWindow, "burst-window", 10*time.Second, "突发窗口")
	fs.DurationVar(&opts.BurstGap, "burst-gap", 50*time.Second, "两轮突发事件之间的间隔")
	fs.IntVar(&opts.Limit, "limit", 0, "最多回放条数（0=全部）")
	fs.BoolVar(&opts.RewriteAt, "rewrite-at", false, "发布前把事件 At 改为实际注入时间（压测延迟口径）")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("未知参数：%s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(opts.File) == "" {
		return opts, errors.New("必须指定 --file")
	}
	if opts.Mode != "fixed" && opts.Mode != "burst" {
		return opts, errors.New("--mode 只能是 fixed 或 burst")
	}
	if opts.RatePerMin < 1 || opts.RatePerMin > 100000 {
		return opts, errors.New("--rate 需在 1 至 100000 之间")
	}
	if opts.BurstCount < 1 || opts.BurstCount > 100000 || opts.BurstWindow <= 0 || opts.BurstGap < 0 {
		return opts, errors.New("突发参数非法")
	}
	if opts.Limit < 0 {
		return opts, errors.New("--limit 不能为负")
	}
	return opts, nil
}

// loadEvents 读取并校验 JSONL，稳定按 at 升序排序。
func loadEvents(path string, limit int) ([]event.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开输入：%w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	events := make([]event.Event, 0, 1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record inputRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("第 %d 行不是合法 JSON：%w", lineNo, err)
		}
		ev := record.event()
		if err := store.ValidateEvent(ev); err != nil {
			return nil, fmt.Errorf("第 %d 行事件非法：%w", lineNo, err)
		}
		events = append(events, ev)
		if limit > 0 && len(events) >= limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取输入：%w", err)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	return events, nil
}

// scheduleOffsets 生成相对回放开始的注入偏移。
func scheduleOffsets(count int, opts options) []time.Duration {
	if count <= 0 {
		return nil
	}
	offsets := make([]time.Duration, count)
	if opts.Mode == "fixed" {
		interval := time.Minute / time.Duration(opts.RatePerMin)
		for i := range offsets {
			offsets[i] = time.Duration(i) * interval
		}
		return offsets
	}
	step := opts.BurstWindow / time.Duration(opts.BurstCount)
	if step <= 0 {
		step = time.Nanosecond
	}
	cycle := opts.BurstWindow + opts.BurstGap
	for i := range offsets {
		burstIndex := i % opts.BurstCount
		cycleIndex := i / opts.BurstCount
		offsets[i] = time.Duration(cycleIndex)*cycle + time.Duration(burstIndex)*step
	}
	return offsets
}

type injectionRecord struct {
	EventID     string    `json:"event_id"`
	ScheduledAt time.Time `json:"scheduled_at"`
	InjectedAt  time.Time `json:"injected_at"`
	PublishedAt time.Time `json:"published_at"`
	Duplicate   bool      `json:"duplicate"`
	Error       string    `json:"error,omitempty"`
}

type summary struct {
	Mode         string    `json:"mode"`
	Requested    int       `json:"requested"`
	Published    int       `json:"published"`
	Duplicates   int       `json:"duplicates"`
	Failed       int       `json:"failed"`
	DurationMS   int64     `json:"duration_ms"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	EventsPerMin float64   `json:"events_per_minute"`
}

type waitFunc func(context.Context, time.Duration) error

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func replay(ctx context.Context, events []event.Event, opts options, pub publisher, record io.Writer, now func() time.Time, sleep waitFunc) (summary, error) {
	offsets := scheduleOffsets(len(events), opts)
	start := now()
	result := summary{Mode: opts.Mode, Requested: len(events), StartedAt: start}
	var recorder *bufio.Writer
	if record != nil {
		recorder = bufio.NewWriter(record)
		defer recorder.Flush()
	}
	var lastOffset time.Duration
	for i, ev := range events {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// offsets 是相对起点的绝对偏移；睡眠必须用相邻差值，不能重复累计。
		if d := offsets[i] - lastOffset; d > 0 {
			if err := sleep(ctx, d); err != nil {
				return result, err
			}
		}
		lastOffset = offsets[i]
		injected := now()
		if opts.RewriteAt {
			ev.At = injected
		}
		ok, err := pub.Publish(ctx, ev)
		published := now()
		item := injectionRecord{
			EventID: ev.ID, ScheduledAt: start.Add(offsets[i]), InjectedAt: injected,
			PublishedAt: published, Duplicate: !ok && err == nil,
		}
		switch {
		case err != nil:
			result.Failed++
			item.Error = err.Error()
		case ok:
			result.Published++
		default:
			result.Duplicates++
		}
		if recorder != nil {
			data, marshalErr := json.Marshal(item)
			if marshalErr != nil {
				return result, marshalErr
			}
			if _, writeErr := recorder.WriteString(string(data) + "\n"); writeErr != nil {
				return result, writeErr
			}
			// 压测进程可能被强制终止；逐条落盘，保证注入时间完整可对账。
			if flushErr := recorder.Flush(); flushErr != nil {
				return result, flushErr
			}
		}
	}
	result.FinishedAt = now()
	result.DurationMS = result.FinishedAt.Sub(start).Milliseconds()
	if result.DurationMS > 0 {
		result.EventsPerMin = float64(result.Requested) / (float64(result.DurationMS) / 60000)
	}
	return result, nil
}

func createRecordFile(path string) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建注入记录目录：%w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("创建注入记录：%w", err)
	}
	return f, nil
}

func filepathDir(path string) string {
	index := strings.LastIndexAny(path, `/\`)
	if index < 0 {
		return "."
	}
	return path[:index]
}
