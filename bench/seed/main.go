// bench/seed 给 staging 库灌脱敏基线数据。
//
// 安全：连接后先校验 current_database() == raisemyfish_bench；
// 默认只做幂等 upsert，--reset 才会清空 bench 表。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
)

const benchDatabase = "raisemyfish_bench"

type options struct {
	ConfigPath    string
	EnvPath       string
	Viewers       int
	Conversations int
	Events        int
	Reset         bool
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("bench-seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := options{}
	fs.StringVar(&opts.ConfigPath, "config", "config/fishd.bench.toml", "压测配置")
	fs.StringVar(&opts.EnvPath, "env-file", ".env", "本地环境文件")
	fs.IntVar(&opts.Viewers, "viewers", 1000, "观众档案数")
	fs.IntVar(&opts.Conversations, "conversations", 1000, "会话状态数")
	fs.IntVar(&opts.Events, "events", 10000, "历史事件数")
	fs.BoolVar(&opts.Reset, "reset", false, "先清空 bench 表")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if opts.Viewers < 0 || opts.Conversations < 0 || opts.Events < 0 {
		return errors.New("数量不能为负")
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
	var name string
	if err := resources.DB.WithContext(probe).Raw("SELECT current_database()").Scan(&name).Error; err != nil {
		return err
	}
	if err := checkBenchDatabase(name); err != nil {
		return err
	}
	if err := database.Migrate(probe, resources.DB); err != nil {
		return fmt.Errorf("迁移：%w", err)
	}
	if opts.Reset {
		if err := resetBench(resources); err != nil {
			return err
		}
	}
	if err := seedViewers(resources, opts.Viewers, time.Now().UTC()); err != nil {
		return err
	}
	if err := seedConversations(resources, opts.Conversations, time.Now().UTC()); err != nil {
		return err
	}
	if err := seedEvents(resources, opts.Events, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "种子完成：viewers=%d conversations=%d events=%d\n", opts.Viewers, opts.Conversations, opts.Events)
	return nil
}

func checkBenchDatabase(name string) error {
	if name != benchDatabase {
		return fmt.Errorf("拒绝灌种子：当前库 %q 不是 %s", name, benchDatabase)
	}
	return nil
}

func resetBench(resources *infra.Resources) error {
	// 只清 bench 库；调用前已校验库名。CASCADE 处理外键。
	return resources.DB.Exec(`TRUNCATE TABLE
event_errors, event_timings, send_log, tasks, events,
conversation_state, viewer_profiles, memory_records, llm_usage, dlq
CASCADE`).Error
}

func seedViewers(resources *infra.Resources, count int, now time.Time) error {
	for start := 0; start < count; start += 500 {
		end := start + 500
		if end > count {
			end = count
		}
		var b strings.Builder
		b.WriteString(`INSERT INTO viewer_profiles(uid,name,hits,replies,last_seen,recent_texts,recent_replies) VALUES `)
		args := make([]any, 0, (end-start)*7)
		for i := start; i < end; i++ {
			if i > start {
				b.WriteByte(',')
			}
			uid := fmt.Sprintf("bench-viewer-%06d", i)
			b.WriteString(`(?,?,?,?,?,?::jsonb,?::jsonb)`)
			args = append(args, uid, "压测观众"+uid[len(uid)-4:], 3+i%20, 1+i%10,
				now.Add(-time.Duration(i%1440)*time.Minute), `[]`, `[]`)
		}
		b.WriteString(` ON CONFLICT(uid) DO UPDATE SET name=excluded.name,hits=excluded.hits,replies=excluded.replies,last_seen=excluded.last_seen`)
		if err := resources.DB.Exec(b.String(), args...).Error; err != nil {
			return fmt.Errorf("灌观众：%w", err)
		}
	}
	return nil
}

func seedConversations(resources *infra.Resources, count int, now time.Time) error {
	for start := 0; start < count; start += 500 {
		end := start + 500
		if end > count {
			end = count
		}
		var b strings.Builder
		b.WriteString(`INSERT INTO conversation_state(conversation_id,channel,account_id,user_id,topic_id,summary,unanswered,turn_count,last_turn_at,expires_at)
VALUES `)
		args := make([]any, 0, (end-start)*10)
		for i := start; i < end; i++ {
			if i > start {
				b.WriteByte(',')
			}
			id := fmt.Sprintf("live:10000001:bench-%06d", i)
			b.WriteString(`(?,?,?,?,?,?,?,?,?,?)`)
			args = append(args, id, "live", "1000", fmt.Sprintf("bench-viewer-%06d", i), "10000001",
				"压测会话摘要", "", 2, now.Add(-time.Duration(i%1440)*time.Minute), now.Add(24*time.Hour))
		}
		b.WriteString(` ON CONFLICT(conversation_id) DO UPDATE SET summary=excluded.summary,turn_count=excluded.turn_count,last_turn_at=excluded.last_turn_at`)
		if err := resources.DB.Exec(b.String(), args...).Error; err != nil {
			return fmt.Errorf("灌会话：%w", err)
		}
	}
	return nil
}

func seedEvents(resources *infra.Resources, count int, now time.Time) error {
	base := now.Add(-30 * 24 * time.Hour)
	for start := 0; start < count; start += 500 {
		end := start + 500
		if end > count {
			end = count
		}
		var b strings.Builder
		b.WriteString(`INSERT INTO events(id,channel,dir,session,from_id,from_name,text,private,at,status,meta,collected_at,collect_mode)
VALUES `)
		args := make([]any, 0, (end-start)*13)
		for i := start; i < end; i++ {
			if i > start {
				b.WriteByte(',')
			}
			b.WriteString(`(?,?,?,?,?,?,?,?,?,?,?::jsonb,?,?)`)
			uid := fmt.Sprintf("bench-viewer-%06d", i%1000)
			at := base.Add(time.Duration(i) * 4 * time.Minute)
			args = append(args, fmt.Sprintf("live:10000001:seed-%06d", i), "live", "in", "10000001",
				uid, "压测观众", fmt.Sprintf("压测历史事件 %d：这是脱敏种子。", i), false,
				at, "done", `{"uid":"`+uid+`","platform":"bili"}`, at, "feed")
		}
		b.WriteString(` ON CONFLICT(id) DO NOTHING`)
		if err := resources.DB.Exec(b.String(), args...).Error; err != nil {
			return fmt.Errorf("灌事件：%w", err)
		}
	}
	return nil
}
