// fishctl 只读检查基础设施状态及跟随日志，不承担业务决策。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (result error) {
	fs := flag.NewFlagSet("fishctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "config/fishd.toml", "TOML配置文件")
	envPath := fs.String("env-file", ".env", "本地环境文件")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("命令参数无效")
	}
	action := fs.Args()
	if len(action) >= 2 && action[0] == "persona" && action[1] == "rescore" {
		return runPersonaRescore(ctx, action[2:], stdout, stderr)
	}
	if len(action) >= 2 && action[0] == "persona" && action[1] == "eval" {
		return runPersonaEval(ctx, *path, *envPath, action[2:], stdout, stderr)
	}
	if len(action) != 1 || (action[0] != "status" && action[0] != "tail") {
		return errors.New("用法：fishctl [--config 路径] [--env-file 路径] status|tail|persona eval")
	}
	cfg, err := config.Load(*path, *envPath)
	if err != nil {
		return err
	}
	if action[0] == "tail" {
		return tail(ctx, cfg.Log.Dir, stdout, time.Now, 200*time.Millisecond)
	}
	probe, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.TimeoutSeconds)*time.Second)
	defer cancel()
	resources, err := infra.Setup(probe, cfg)
	ready := err == nil
	probeErr := err
	if resources != nil {
		defer func() { result = errors.Join(result, resources.Close(ctx)) }()
		probeErr = database.Ready(probe, resources.DB)
		ready = probeErr == nil
	}
	// 统计来源尚未装配（真实 repository 实现属 0.10d）。显式置 nil 而不是留空，
	// 是为了让「看起来接好了其实没接」一眼可见：collectStats(nil) 返回零值，
	// status 的输出与 0.1 完全一致，不新增字段。
	var statsSource StatsSource
	stats, statsErr := collectStats(probe, statsSource, time.Now().UTC(), DefaultStatsWindow)
	return errors.Join(
		printStatus(cfg.Runtime.StateFile, time.Duration(cfg.Runtime.StaleSeconds)*time.Second, ready, stdout, stats),
		statsErr,
		probeErr,
	)
}

// printStatus 输出机器可读的健康摘要。
//
// stats 为可选参数：传了且非空时追加 `stats` 字段；不传时输出与 0.1 逐字节一致，
// 避免影响既有的状态解析方。
func printStatus(path string, ttl time.Duration, databaseReady bool, out io.Writer, stats ...StatusStats) error {
	state, readErr := infra.ReadStatus(path)
	alive := readErr == nil && state.Alive(time.Now().UTC(), ttl) && databaseReady
	var extra *StatusStats
	if len(stats) > 0 && !stats[0].empty() {
		snapshot := stats[0]
		extra = &snapshot
	}
	summary := struct {
		ServiceAlive  bool         `json:"service_alive"`
		DatabaseReady bool         `json:"database_ready"`
		Service       infra.Status `json:"service"`
		Channels      string       `json:"channels"`
		Stats         *StatusStats `json:"stats,omitempty"`
	}{alive, databaseReady, state, "not_implemented", extra}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("输出状态摘要：%w", err)
	}
	if !alive {
		return errors.New("fishd未运行、心跳过期或数据库未就绪")
	}
	return nil
}

// tail 从当日日志开头读取并跟随追加；跨日换文件，缺文件时等待，取消立即返回。
func tail(ctx context.Context, dir string, out io.Writer, now func() time.Time, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var day string
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		today := now().Format("2006-01-02")
		if today != day {
			day = today
			offset = 0
		}
		f, err := os.Open(filepath.Join(dir, "fishd-"+day+".log"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("打开当日日志：%w", err)
		}
		if err == nil {
			info, statErr := f.Stat()
			if statErr != nil {
				return errors.Join(statErr, f.Close())
			}
			if info.Size() < offset {
				offset = 0
			}
			_, seekErr := f.Seek(offset, io.SeekStart)
			if seekErr != nil {
				return errors.Join(seekErr, f.Close())
			}
			n, copyErr := io.Copy(out, io.NewSectionReader(f, offset, info.Size()-offset))
			closeErr := f.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return fmt.Errorf("跟随日志：%w", err)
			}
			offset += n
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
