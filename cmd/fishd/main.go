// fishd装配基础设施与可选live采集；模型消费和发送尚未接入。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	eventbus "RaiseMyFish/internal/event/bus"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/memory"
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
	commentCollector, err := buildComment(cfg, logger)
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
	stopMemory := startMemoryExtractor(ctx, cfg, resources.DB, logger)
	defer stopMemory()
	stopMemeLearn := startMemeLearn(ctx, cfg, resources.DB, knowledgeRuntime, logger)
	defer stopMemeLearn()
	stopHeartbeatLike := startHeartbeatLike(ctx, cfg, logger)
	defer stopHeartbeatLike()
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
	// 心跳独立于状态文件，供独立进程 fishwatchdog 判断冻结/死亡。
	// 放在初始化完成之后启动：启动阶段卡死时不刷新心跳，看门狗才有机会介入。
	stopHeartbeat := startHeartbeat(ctx, cfg.Watchdog.HeartbeatPath, logger)
	defer stopHeartbeat()
	// 消费循环：只有 [scheduler] 存在、有 Brain 路由且 live 通道开启时才装配；
	// 否则保持「只采集」的现状。
	wantConsume := consumeEnabled(cfg, routes)
	if liveCollector == nil && commentCollector == nil && !wantConsume {
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
	// 发送通道表：buildConsume 先用空壳，注册表建好后再绑定（Collect 之前完成）
	chans := &channelSet{}
	consume, snd, err := buildConsume(cfg, resources, routes, pack, bus, knowledgeRuntime.Reader, presenceTable, chans, logger)
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
	} else {
		// watch/active 都是旁路：装配失败只记日志，不阻塞采集/调度。
		stopWatch := startWatch(ctx, cfg, logger)
		defer stopWatch()
		stopProactive := startProactive(ctx, cfg, resources, routes, pack, knowledgeRuntime.Reader, presenceTable, logger)
		defer stopProactive()
	}
	// 通道经注册表获取；Collect 异步启动，stop 在返回时取消并等待采集退出。
	dmCollector, err := buildDMCollector(cfg, logger)
	if err != nil {
		return fmt.Errorf("装配私信采集失败：%w", err)
	}
	// 运维面板（只监听回环）：默认关，开启后独立 goroutine 跑
	stopWeb := startWebPanel(ctx, cfg, resources, logger)
	defer stopWeb()

	// 知识热重载：梗白学习写入 learned.md 后不必重启
	if stopReload := startKnowledgeHotReload(ctx, knowledgeRuntime.Reader, cfg.Knowledge, logger); stopReload != nil {
		defer stopReload()
	}
	registry, err := buildChannelRegistry(liveCollector, commentCollector, dmCollector, snd, timeout, logger)
	if err != nil {
		return err
	}
	// 发送路径改用通道：此后 Generate/Deliver 优先走注册表
	chans.Set(registry)
	channels := registry.All()
	for _, ch := range channels {
		stop, err := ch.Collect(ctx, withPresenceBus(bus, presenceTable))
		if err != nil {
			return fmt.Errorf("启动 %s 采集失败：%w", ch.Name(), err)
		}
		defer stop()
	}
	if len(channels) > 0 {
		logger.Info("已装配采集通道到事件总线", zap.String("action", "collector_bus_start"), zap.Int("channels", len(channels)))
	}
	return servePipeline(ctx)
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
func newMemoryWorker(cfg config.Memory, source extractworker.DoneSource, repo store.MemoryRepo, profiles extractworker.ProfileSource, extractor memory.Extractor, watermarkPath string) (*extractworker.Worker, error) {
	watermark, err := extractworker.NewFileWatermark(watermarkPath)
	if err != nil {
		return nil, err
	}
	// 间隔由 runMemoryExtract 的循环控制；worker 的 Run 不在本装配中使用。
	// Profiles 非 nil 时会在提炼循环里附带档案重建（每小时节流 + 只在直播活跃时）。
	// Extractor 非 nil 时按触发策略走 LLM，否则规则版。
	return extractworker.New(source, repo, watermark, extractworker.Options{AllowPrivate: cfg.AllowPrivate, Profiles: profiles, Extractor: extractor})
}

// buildMemoryExtractor 构造记忆提炼器：LLM 版（按触发策略）或 nil（规则版）。
// 任何装配失败都回落规则版——提炼是增强能力，不能因模型不可用而中断。
func buildMemoryExtractor(cfg config.Config, logger *zap.Logger) memory.Extractor {
	if !cfg.Memory.LLMExtract {
		return nil
	}
	if !cfg.LLM.Enabled || len(cfg.LLM.Routing) == 0 {
		logger.Warn("记忆 LLM 提炼已配置但 LLM 未启用，回落规则版", zap.String("action", "memory_extract"))
		return nil
	}
	key := backgroundRoute(cfg)
	adapter, model, err := buildBackgroundLLM(cfg, key, cfg.LLM.MaxReplyTokens)
	if err != nil {
		logger.Warn("记忆 LLM 提炼装配失败，回落规则版", zap.String("action", "memory_extract"), zap.Error(err))
		return nil
	}
	extractor, err := memory.NewLLMExtractor(adapter, memory.LLMExtractOptions{
		MaxPerHour: cfg.Memory.LLMExtractMaxPerHour,
	})
	if err != nil {
		logger.Warn("记忆 LLM 提炼器装配失败，回落规则版", zap.String("action", "memory_extract"), zap.Error(err))
		return nil
	}
	logger.Info("记忆 LLM 提炼已启用", zap.String("action", "memory_extract"), zap.String("model", model))
	return extractor
}

// backgroundRoute 选择后台增强任务的具名路由：优先 default，否则按名称排序取第一个。
func backgroundRoute(cfg config.Config) string {
	if _, ok := cfg.LLM.Routing["default"]; ok {
		return "default"
	}
	keys := make([]string, 0, len(cfg.LLM.Routing))
	for key := range cfg.LLM.Routing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// buildBackgroundLLM 按具名路由装配后台增强任务共用的模型客户端；不记录密钥。
func buildBackgroundLLM(cfg config.Config, key string, maxTokens int) (*memoryProviderLLM, string, error) {
	name, prov, model, err := cfg.LLM.Resolve(key)
	if err != nil {
		return nil, "", err
	}
	client, err := provider.NewOpenAI(name, prov.BaseURL, prov.APIKey, &http.Client{}, time.Duration(cfg.LLM.AttemptTimeoutMS)*time.Millisecond)
	if err != nil {
		return nil, "", err
	}
	return &memoryProviderLLM{client: client, model: model, maxTokens: maxTokens}, model, nil
}

// memoryProviderLLM 把 provider 适配成后台增强任务共用的最小模型接口
// （memory.LLM 与 knowledge.MemeLLM）。
type memoryProviderLLM struct {
	client    provider.Provider
	model     string
	maxTokens int
}

func (m *memoryProviderLLM) Complete(ctx context.Context, system, user string) (string, error) {
	resp, err := m.client.Chat(ctx, provider.Request{
		Model:       m.model,
		Messages:    []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		MaxTokens:   m.maxTokens,
		Temperature: 0.2,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
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
func startMemoryExtractor(ctx context.Context, cfg config.Config, db *gorm.DB, logger *zap.Logger) func() {
	noop := func() {}
	if !cfg.Memory.Enabled {
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
	// 观察档案重建依赖：失败降级为不建档案（提炼本身不受影响）。
	profiles, err := database.NewViewerRepo(db)
	if err != nil {
		logger.Warn("观众档案装配失败，跳过档案重建", zap.String("action", "memory_extract"), zap.Error(err))
		profiles = nil
	}
	extractor := buildMemoryExtractor(cfg, logger)
	worker, err := newMemoryWorker(cfg.Memory, source, repo, profiles, extractor, memoryWatermarkPath)
	if err != nil {
		logger.Error("记忆提炼初始化失败，降级运行", zap.String("action", "memory_extract"), zap.Error(err))
		return noop
	}

	interval := time.Duration(cfg.Memory.IntervalSeconds) * time.Second
	logger.Info("记忆提炼已启动",
		zap.String("action", "memory_extract"),
		zap.Int("interval_seconds", cfg.Memory.IntervalSeconds),
		zap.Bool("allow_private", cfg.Memory.AllowPrivate),
		zap.Bool("llm_extract", cfg.Memory.LLMExtract),
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
