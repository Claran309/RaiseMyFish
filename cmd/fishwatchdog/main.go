package main

// fishwatchdog 是独立进程：读 fishd 心跳，冻结/死亡时按配置重启。
//
// 用法：
//
//	fishwatchdog -config config/fishd.toml
//
// 必须与 fishd 分开跑（同一进程里自己看自己没意义）。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/watchdog"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "fishwatchdog 退出：%v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("fishwatchdog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "config/fishd.toml", "TOML 配置文件")
	envPath := fs.String("env-file", ".env", "本地环境文件")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 看门狗只关心 [watchdog]；不连库、不采集/发送，平台凭据缺失不能挡住监控。
	cfg, err := config.LoadForWatchdog(*path, *envPath)
	if err != nil {
		return fmt.Errorf("加载配置失败：%w", err)
	}
	log, err := newLogger(stderr)
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()

	w := cfg.Watchdog
	if !w.Enabled {
		fmt.Fprintln(stdout, "看门狗未启用（[watchdog].enabled=false），直接退出")
		return nil
	}
	if w.RestartCommand == "" {
		return errors.New("看门狗已启用但未配置 restart_command")
	}

	dog, err := watchdog.New(watchdog.Options{
		HeartbeatPath:      w.HeartbeatPath,
		HistoryPath:        w.HistoryPath,
		Interval:           time.Duration(w.IntervalSeconds) * time.Second,
		StaleAfter:         time.Duration(w.StaleSeconds) * time.Second,
		UnhealthyThreshold: w.UnhealthyThreshold,
		Cooldown:           time.Duration(w.CooldownSeconds) * time.Second,
		RestartCommand:     w.RestartCommand,
		Log:                log,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "看门狗启动：心跳 %s，间隔 %ds，阈值 %d，冷却 %ds\n",
		w.HeartbeatPath, w.IntervalSeconds, w.UnhealthyThreshold, w.CooldownSeconds)
	return dog.Run(ctx)
}

// newLogger 输出到 stderr（看门狗一般由 systemd 托管）。
func newLogger(w io.Writer) (*zap.Logger, error) {
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(w), zap.InfoLevel)
	return zap.New(core), nil
}
