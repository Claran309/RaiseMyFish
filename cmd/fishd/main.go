// fishd装配基础设施与可选live采集；模型消费和发送尚未接入。
package main

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
	"RaiseMyFish/internal/database"
	eventbus "RaiseMyFish/internal/event/bus"
	"RaiseMyFish/internal/infra"
	"go.uber.org/zap"
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
	fs := flag.NewFlagSet("fishd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "config/fishd.toml", "TOML配置文件")
	envPath := fs.String("env-file", ".env", "本地环境文件")
	brainRoute := fs.String("brain-route", "default", "仅check-brain使用的具名路由")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("命令参数无效")
	}
	action := fs.Args()
	migrate := len(action) == 2 && action[0] == "migrate" && action[1] == "up"
	checkBrain := len(action) == 1 && action[0] == "check-brain"
	checkAll := len(action) == 1 && action[0] == "check"
	if len(action) > 0 && !migrate && !checkBrain && !checkAll {
		return errors.New("用法：fishd [--config 路径] [--env-file 路径] [migrate up|check|check-brain]；不支持破坏性回滚")
	}
	cfg, err := config.Load(*path, *envPath)
	if err != nil {
		return err
	}
	logger, closeLog, err := infra.NewLogger(cfg.Log.Level, cfg.Log.Dir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeLog()) }()
	resources, err := infra.Setup(ctx, cfg)
	if err != nil {
		logger.Error("基础设施初始化失败", zap.Error(err))
		return err
	}
	defer func() { result = errors.Join(result, resources.Close(ctx)) }()
	timeout := time.Duration(cfg.Database.TimeoutSeconds) * time.Second
	if migrate {
		migrateCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := database.Migrate(migrateCtx, resources.DB); err != nil {
			logger.Error("迁移失败", zap.Error(err))
			return err
		}
		logger.Info("数据库迁移就绪", zap.String("action", "migrate"))
		_, err := fmt.Fprintln(stdout, "数据库迁移就绪（重复执行无副作用）")
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	err = database.Ready(checkCtx, resources.DB)
	if err == nil && !checkBrain && !checkAll {
		err = resources.Acquire(checkCtx)
	}
	cancel()
	if err != nil {
		return err
	}
	usage, err := database.NewUsageRepo(resources.DB)
	if err != nil {
		return err
	}
	routes, pack, err := buildBrains(ctx, cfg, usage, logger)
	if err != nil {
		return err
	}
	knowledgeRuntime, err := buildKnowledge(ctx, cfg.Knowledge)
	if err != nil {
		return err
	}
	liveCollector, err := buildLive(cfg, logger)
	if err != nil {
		return err
	}
	if checkAll {
		return printCheckDiagnostic(cfg, routes, knowledgeRuntime, stdout)
	}
	if checkBrain {
		return printBrainDiagnostic(*brainRoute, routes, pack, stdout)
	}
	logger.Info("Brain装配检查完成，未消费事件", zap.String("action", "brain_setup"), zap.Int("routes", len(routes)))
	logger.Info("知识装配检查完成，未接入事件消费", zap.String("action", "knowledge_setup"), zap.Bool("enabled", knowledgeRuntime.Diagnostic.Enabled), zap.Bool("assembled", knowledgeRuntime.Diagnostic.Assembled), zap.Int("entries", knowledgeRuntime.Diagnostic.Entries), zap.String("revision", knowledgeRuntime.Diagnostic.Revision))
	check := func(parent context.Context) error {
		probe, stop := context.WithTimeout(parent, timeout)
		defer stop()
		return resources.Ping(probe)
	}
	serveRuntime := func(ctx context.Context) error {
		return serve(ctx, cfg.Runtime.StateFile, time.Duration(cfg.Runtime.HeartbeatSeconds)*time.Second, logger, check)
	}
	// 消费循环：只有 [scheduler] 存在、有 Brain 路由且 live 通道开启时才装配；
	// 否则保持「只采集」的现状。
	wantConsume := consumeEnabled(cfg, routes)
	if liveCollector == nil && !wantConsume {
		return serveRuntime(ctx)
	}
	events, err := database.NewEventRepo(resources.DB)
	if err != nil {
		return err
	}
	bus, err := eventbus.New(events, 0)
	if err != nil {
		return err
	}
	consume, err := buildConsume(cfg, resources, routes, pack, bus, knowledgeRuntime.Reader, logger)
	if err != nil {
		return err
	}
	if consume != nil {
		logger.Info("消费循环装配完成",
			zap.String("action", "consume_loop_start"),
			zap.Int("concurrency", cfg.Scheduler.MaxConcurrent),
			zap.Int("claim_batch", cfg.Scheduler.ClaimBatch),
			zap.Int("channels", len(cfg.Scheduler.Channels)),
		)
	}
	servePipeline := func(ctx context.Context) error {
		return withScheduler(ctx, consume, logger, serveRuntime)
	}
	if liveCollector == nil {
		return servePipeline(ctx)
	}
	return serveWithCollector(ctx, liveCollector, bus, timeout, logger, servePipeline)
}

// serve 不启动额外后台goroutine；所有探活有超时，取消后更新状态再释放连接。
func serve(ctx context.Context, path string, interval time.Duration, logger *zap.Logger, check func(context.Context) error) (result error) {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if err := check(ctx); err != nil {
		return err
	}
	state := infra.Status{PID: os.Getpid(), State: "running", Database: "ready", Version: "phase0.6-dev", UpdatedAt: time.Now().UTC()}
	if err := infra.WriteStatus(path, state); err != nil {
		return err
	}
	logger.Info("fishd基础设施已启动", zap.String("action", "start"), zap.Int("pid", state.PID))
	defer func() {
		state.State = "stopped"
		state.UpdatedAt = time.Now().UTC()
		result = errors.Join(result, infra.WriteStatus(path, state))
		logger.Info("fishd已停止", zap.String("action", "stop"))
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := check(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				logger.Error("数据库健康检查失败，退出服务", zap.String("action", "health"))
				return err
			}
			state.UpdatedAt = time.Now().UTC()
			if err := infra.WriteStatus(path, state); err != nil {
				return err
			}
		}
	}
}
