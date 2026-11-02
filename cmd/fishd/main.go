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
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// memoryWatermarkPath 是提炼游标的持久化位置；state/ 不随日志轮转清理。
const memoryWatermarkPath = "state/memory-watermark.json"

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
	// 记忆提炼是增强能力：初始化失败只记日志并降级，不阻塞采集/消费主链路。
	stopMemory := startMemoryExtractor(ctx, cfg.Memory, resources.DB, logger)
	defer stopMemory()
	logger.Info("Brain装配检查完成，未消费事件", zap.String("action", "brain_setup"), zap.Int("routes", len(routes)))
	logger.Info("知识装配检查完成，未接入事件消费", zap.String("action", "knowledge_setup"), zap.Bool("enabled", knowledgeRuntime.Diagnostic.Enabled), zap.Bool("assembled", knowledgeRuntime.Diagnostic.Assembled), zap.Int("entries", knowledgeRuntime.Diagnostic.Entries), zap.String("revision", knowledgeRuntime.Diagnostic.Revision))
	check := func(parent context.Context) error {
		probe, stop := context.WithTimeout(parent, timeout)
		defer stop()
		return resources.Ping(probe)
	}
	// 活动流是 P0 内存态：只用于展示主体此刻在做什么，不落库；
	// 心跳时把摘要写进状态文件，供 fishctl status 跨进程读取。
	presenceTable := presence.NewTable()
	serveRuntime := func(ctx context.Context) error {
		return serve(ctx, cfg.Runtime.StateFile, time.Duration(cfg.Runtime.HeartbeatSeconds)*time.Second, logger, check, presenceTable.Summary)
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
	consume, err := buildConsume(cfg, resources, routes, pack, bus, knowledgeRuntime.Reader, presenceTable, logger)
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
		if cfg.Watch.Enabled {
			logger.Warn("看直播未启动：直播采集未启用", zap.String("action", "watch"))
		}
		return servePipeline(ctx)
	}
	// watch/active 都是旁路：装配失败只记日志，不阻塞采集/调度。
	stopWatch := startWatch(ctx, cfg, logger)
	defer stopWatch()
	stopProactive := startProactive(ctx, cfg, resources, routes, pack, logger)
	defer stopProactive()
	return serveWithCollector(ctx, liveCollector, withPresence(bus, presenceTable), timeout, logger, servePipeline)
}

// serve 不启动额外后台goroutine；所有探活有超时，取消后更新状态再释放连接。
// presence 可选：传入时每次心跳把摘要写进状态文件，供 fishctl status 跨进程读取。
func serve(ctx context.Context, path string, interval time.Duration, logger *zap.Logger, check func(context.Context) error, presence ...func(time.Time) string) (result error) {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if err := check(ctx); err != nil {
		return err
	}
	summary := func(now time.Time) string {
		if len(presence) == 0 || presence[0] == nil {
			return ""
		}
		return presence[0](now)
	}
	now := time.Now()
	state := infra.Status{PID: os.Getpid(), State: "running", Database: "ready", Version: "phase0.6-dev", Presence: summary(now), UpdatedAt: now.UTC()}
	if err := infra.WriteStatus(path, state); err != nil {
		return err
	}
	logger.Info("fishd基础设施已启动", zap.String("action", "start"), zap.Int("pid", state.PID))
	defer func() {
		state.State = "stopped"
		state.Presence = ""
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
			now := time.Now()
			state.UpdatedAt = now.UTC()
			state.Presence = summary(now)
			if err := infra.WriteStatus(path, state); err != nil {
				return err
			}
		}
	}
}

// memoryExtractWorker 是提炼循环依赖的最小能力，便于装配测试注入。
type memoryExtractWorker interface {
	RunOnce(context.Context) (extractworker.Stats, error)
}

// newMemoryWorker 只用接口依赖装配 worker，测试可注入 mock 来源与仓库。
func newMemoryWorker(cfg config.Memory, source extractworker.DoneSource, repo store.MemoryRepo, watermarkPath string) (*extractworker.Worker, error) {
	watermark, err := extractworker.NewFileWatermark(watermarkPath)
	if err != nil {
		return nil, err
	}
	// 间隔由 runMemoryExtract 的循环控制；worker 的 Run 不在本装配中使用。
	return extractworker.New(source, repo, watermark, extractworker.Options{AllowPrivate: cfg.AllowPrivate})
}

// runMemoryExtract 立即扫一轮，之后按 interval 循环；单轮失败只记日志，等下轮重试。
func runMemoryExtract(ctx context.Context, worker memoryExtractWorker, interval time.Duration, logger *zap.Logger) {
	runOnce := func() {
		stats, err := worker.RunOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("记忆提炼失败，等待下一轮", zap.String("action", "memory_extract"), zap.Error(err))
			}
			return
		}
		logger.Info("记忆提炼完成",
			zap.String("action", "memory_extract"),
			zap.Int("scanned", stats.Scanned),
			zap.Int("extracted", stats.Extracted),
			zap.Int("inserted", stats.Inserted),
			zap.Int("duplicated", stats.Duplicated),
			zap.Int("skipped", stats.Skipped),
		)
	}
	runOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// startMemoryExtractor 装配并启动提炼 worker；返回的 stop 会取消并等待循环退出。
// 未启用或初始化失败时返回空 stop，调用方主链路不受影响。
func startMemoryExtractor(ctx context.Context, cfg config.Memory, db *gorm.DB, logger *zap.Logger) func() {
	noop := func() {}
	if !cfg.Enabled {
		return noop
	}
	source, err := database.NewDoneSourceRepo(db)
	if err != nil {
		logger.Error("记忆提炼装配失败，降级运行", zap.String("action", "memory_extract"), zap.Error(err))
		return noop
	}
	repo, err := database.NewMemoryRepo(db)
	if err != nil {
		logger.Error("记忆提炼装配失败，降级运行", zap.String("action", "memory_extract"), zap.Error(err))
		return noop
	}
	worker, err := newMemoryWorker(cfg, source, repo, memoryWatermarkPath)
	if err != nil {
		logger.Error("记忆提炼初始化失败，降级运行", zap.String("action", "memory_extract"), zap.Error(err))
		return noop
	}

	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	logger.Info("记忆提炼已启动",
		zap.String("action", "memory_extract"),
		zap.Int("interval_seconds", cfg.IntervalSeconds),
		zap.Bool("allow_private", cfg.AllowPrivate),
	)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMemoryExtract(runCtx, worker, interval, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}
