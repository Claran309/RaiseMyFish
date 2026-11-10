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
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	eventbus "RaiseMyFish/internal/event/bus"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/logview"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// memoryWatermarkPath 是提炼游标的持久化位置；state/ 不随日志轮转清理。
const memoryWatermarkPath = "state/memory-watermark.json"

// memoryFreezePath 是运行时冻结标记，与 fishctl memory freeze/unfreeze 共用。
const memoryFreezePath = memory.DefaultFreezePath

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
	// migrate 只操作数据库，不该被通道开关/B站/LLM 凭据拦住：
	// 用 LoadForMigrate 只校验数据库与日志，保证首次部署能先建表。
	var err error
	var cfg config.Config
	if migrate {
		cfg, err = config.LoadForMigrate(*path, *envPath)
	} else {
		// 先只做结构校验：B站凭据可能只存在于 DB 活跃账号，要等数据库就绪后才能核验。
		cfg, err = config.LoadForDaemon(*path, *envPath)
	}
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
	// 账号解析链：DB 活跃账号优先，环境变量兜底；随后静态消费方回填、Go 客户端动态解析。
	accountService, err := buildAccountService(cfg, resources.DB, logger)
	if err != nil {
		return fmt.Errorf("装配B站账号服务失败：%w", err)
	}
	applyActiveAccount(ctx, &cfg, accountService, logger)
	// 账号链完成后才核验B站凭据：DB 活跃账号可以补上环境变量里没有的 cookie/self_id。
	if err := cfg.ValidateCredentials(); err != nil {
		logger.Error("B站凭据校验失败", zap.String("action", "account_credential"), zap.Error(err))
		return fmt.Errorf("校验基础设施配置：%w", err)
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
	// 语义召回是增强能力：装配失败只降级为关键词，不影响启动。
	if err := attachKnowledgeSemantic(cfg, resources.DB, &knowledgeRuntime, logger); err != nil {
		logger.Warn("知识语义召回装配失败，回落纯关键词", zap.String("action", "knowledge_semantic"), zap.Error(err))
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
	stopEmbeddingRefresh := startKnowledgeEmbeddingRefresh(ctx, cfg, knowledgeRuntime, logger)
	defer stopEmbeddingRefresh()
	// 记忆/档案向量：同一套增量语义，节奏独立（默认 5 分钟）。
	stopMemoryEmbedding := startMemoryEmbeddingRefresh(ctx, cfg, resources.DB, logger)
	defer stopMemoryEmbedding()
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
	// 事件总线始终装配：面板可以在运行时开启采集，不能因为静态配置全关就跳过。
	events, err := database.NewEventRepo(resources.DB)
	if err != nil {
		return err
	}
	bus, err := eventbus.New(events, 0)
	if err != nil {
		return err
	}

	// 送礼开关由控制面热更新，ReAct 副作用每次调用都读最新值。
	giftGate := &atomic.Bool{}
	giftGate.Store(cfg.Gift.Enabled)
	// 副作用限频是进程级的：消费循环与主动弹幕的每个工具箱共用这一份，
	// 否则每个事件新建工具箱会各自清零，follow/like/dm 可被跨事件绕过。
	sideLimiter := reasoning.NewSideEffectLimiter()

	// 发送通道表：buildConsume 先用空壳，注册表建好后再绑定（Send/Resume 之前完成）
	chans := &channelSet{}
	// live 采集房间的权威状态：状态注入与 watching 重置都读它。
	liveRooms := &liveRoomState{}
	consume, snd, dmWhitelist, err := buildConsume(cfg, resources, accountService, routes, pack, bus, knowledgeRuntime.Reader, presenceTable, liveRooms, chans, giftGate.Load, sideLimiter, logger)
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
	// 通道注册表只服务发送/核验；采集统一由运行时控制面驱动。
	dmCollector, err := buildDMCollector(cfg, accountService, logger)
	if err != nil {
		return fmt.Errorf("装配私信采集失败：%w", err)
	}
	registry, err := buildChannelRegistry(liveCollector, commentCollector, dmCollector, snd, timeout, logger)
	if err != nil {
		return err
	}
	chans.Set(registry)

	deps := runtimeDeps{
		// 采集发布前记录 presence（在看直播），与旧 channel.Collect 路径一致。
		Bus:         withPresenceBus(bus, presenceTable),
		Resources:   resources,
		Routes:      routes,
		Pack:        pack,
		Knowledge:   knowledgeRuntime,
		Presence:    presenceTable,
		Rooms:       liveRooms,
		Timeout:     timeout,
		Accounts:    accountService,
		DMWhitelist: dmWhitelist,
		GiftGate:    giftGate,
		Now:         time.Now,

		SideEffectLimiter: sideLimiter,
	}
	// 避免 typed-nil 指针装进接口后变成「非 nil 采集器」。
	if liveCollector != nil {
		deps.Live = liveCollector
	}
	if commentCollector != nil {
		deps.Comment = commentCollector
	}
	if dmCollector != nil {
		deps.DM = dmCollector
	}
	control, err := newRuntimeControl(ctx, cfg, deps, logger)
	if err != nil {
		return err
	}
	defer control.Close()

	// 运维面板（只监听回环）：默认关，开启后独立 goroutine 跑。
	stopWeb := startWebPanel(ctx, cfg, resources, knowledgeRuntime, accountService, control, logview.NewSource(cfg.Log.Dir, nil), logger)
	defer stopWeb()

	// 主循环先就绪（running 状态落盘）再启动采集：外部不会先看到事件、后看到服务状态。
	runCtx, stopPipeline := context.WithCancel(ctx)
	defer stopPipeline()
	pipeline := make(chan error, 1)
	go func() { pipeline <- servePipeline(runCtx) }()
	if err := waitRunning(runCtx, cfg.Runtime.StateFile, pipeline); err != nil {
		if errors.Is(err, context.Canceled) {
			return <-pipeline
		}
		return err
	}
	if err := control.StartInitial(); err != nil {
		stopPipeline()
		<-pipeline
		return err
	}
	return <-pipeline
}

// waitRunning 等主循环把 running 状态写进状态文件；主循环提前退出或取消会立即返回。
func waitRunning(ctx context.Context, path string, pipeline <-chan error) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if st, err := infra.ReadStatus(path); err == nil && st.State == "running" && st.PID == os.Getpid() {
			return nil
		}
		select {
		case err := <-pipeline:
			if err == nil {
				return errors.New("服务在就绪前退出")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// serve 不启动额外后台goroutine；所有探活有超时，取消后更新状态再释放连接。
// presence 可选：传入时每次心跳把摘要写进状态文件，供 fishctl status 跨进程读取。
func serve(ctx context.Context, path string, interval time.Duration, logger *zap.Logger, check func(context.Context) error, presence ...func(time.Time) string) (result error) {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if err := check(ctx); err != nil {
		// 启动探针期间被取消属于正常停止，不能误报数据库故障。
		if ctx.Err() != nil {
			return nil
		}
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

// memoryWorkerDeps 是提炼 worker 的装配依赖。
//
// 用结构体而不是长参数列表：接线后依赖项已超过 10 个，
// 位置参数很容易在新增依赖时传错（且编译器不会报错，因为类型都是接口）。
type memoryWorkerDeps struct {
	Config config.Memory
	// Source 是旧水位路径的事件来源；Jobs 非 nil 时仅作兼容保留。
	Source extractworker.DoneSource
	// Jobs 非 nil 时走任务队列（M05）：入队/租约/完成，不再依赖 done 事件与事件时间水位。
	Jobs extractworker.JobSource
	// Recent 提供提炼上下文；必须是带时间上界的实现（M07），否则历史回放会读到未来。
	Recent extractworker.RecentEventsSource
	Repo   store.MemoryRepo
	// Profiles 非 nil 时附带观众档案重建（每小时节流 + 只在直播活跃时）。
	Profiles extractworker.ProfileSource
	// Soul 非 nil 且 SoulEnabled 时重建灵魂记忆（M12）。
	Soul extractworker.SoulSource
	// Extractor 非 nil 时按触发策略走 LLM，否则规则版。
	Extractor     memory.Extractor
	WatermarkPath string
	FreezePath    string
}

// newMemoryWorker 只用接口依赖装配 worker，测试可注入 mock 来源与仓库。
func newMemoryWorker(deps memoryWorkerDeps) (*extractworker.Worker, error) {
	watermark, err := extractworker.NewFileWatermark(deps.WatermarkPath)
	if err != nil {
		return nil, err
	}
	opts := extractworker.Options{
		AllowPrivate: deps.Config.AllowPrivate,
		Frozen:       deps.Config.Frozen,
		FreezePath:   deps.FreezePath,
		Profiles:     deps.Profiles,
		Extractor:    deps.Extractor,
		Jobs:         deps.Jobs,
		JobOwner:     extractJobOwner(),
		// 提炼上下文：只读「严格早于当前事件」的历史，避免回放时混入未来发言（M07）。
		Recent:    deps.Recent,
		OwnerUID:  deps.Config.OwnerUID,
		OwnerName: deps.Config.OwnerName,
	}
	// 灵魂记忆（M12）：只有显式开启时才接来源；路径由配置派生/显式给出，
	// 主体/未来生效/私密筛选在仓库与重建层各自兜底。
	if deps.Config.SoulEnabled {
		opts.Soul = deps.Soul
		opts.SoulOpts = extractworker.SoulOptions{
			Path:       deps.Config.SoulPath,
			MaxEntries: deps.Config.SoulMaxEntries,
		}
	}
	// 间隔由 runMemoryExtract 的循环控制；worker 的 Run 不在本装配中使用。
	return extractworker.New(deps.Source, deps.Repo, watermark, opts)
}

// extractJobOwner 是提炼任务的租约标识：主机名+进程号，便于排查「谁卡住了任务」。
func extractJobOwner() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "fishd"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

// buildMemoryExtractor 构造记忆提炼器：LLM 版（按触发策略）或 nil（规则版）。
// 任何装配失败都回落规则版——提炼是增强能力，不能因模型不可用而中断。
func buildMemoryExtractor(cfg config.Config, db *gorm.DB, logger *zap.Logger) memory.Extractor {
	if !cfg.Memory.LLMExtract {
		return nil
	}
	if !cfg.LLM.Enabled || len(cfg.LLM.Routing) == 0 {
		logger.Warn("记忆 LLM 提炼已配置但 LLM 未启用，回落规则版", zap.String("action", "memory_extract"))
		return nil
	}
	key := backgroundRoute(cfg)
	// 账本是增强能力：装配失败时照旧提炼，只是这次调用不进成本统计。
	var usage store.UsageRepo
	if repo, uerr := database.NewUsageRepo(db); uerr == nil {
		usage = repo
	} else {
		logger.Warn("记忆提炼账本装配失败，本次调用不记成本", zap.String("action", "memory_extract"), zap.Error(uerr))
	}
	adapter, model, err := buildBackgroundLLM(cfg, key, cfg.LLM.MaxReplyTokens, llmmeta.PurposeMemoryExtract, usage, logger)
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
//
// purpose 会写进每条账本行；usage 为 nil 时仍返回可用客户端（只是不记账）。
func buildBackgroundLLM(cfg config.Config, key string, maxTokens int, purpose llmmeta.Purpose, usage store.UsageRepo, logger *zap.Logger) (*memoryProviderLLM, string, error) {
	name, prov, model, err := cfg.LLM.Resolve(key)
	if err != nil {
		return nil, "", err
	}
	client, err := provider.NewOpenAI(name, prov.BaseURL, prov.APIKey, &http.Client{}, time.Duration(cfg.LLM.AttemptTimeoutMS)*time.Millisecond)
	if err != nil {
		return nil, "", err
	}
	adapter := &memoryProviderLLM{client: client, model: model, maxTokens: maxTokens, purpose: purpose, usage: usage, logger: logger}
	// 报价可选：没配就记 nil 费用，而不是编一个 0。
	if price, ok := prov.Prices[model]; ok {
		adapter.inUSD = price.InputUSDPerMillion
		adapter.outUSD = price.OutputUSDPerMillion
	}
	return adapter, model, nil
}

// memoryProviderLLM 把 provider 适配成后台增强任务共用的最小模型接口
// （memory.LLM 与 knowledge.MemeLLM）。
type memoryProviderLLM struct {
	client    provider.Provider
	model     string
	maxTokens int
	purpose   llmmeta.Purpose
	inUSD     *float64
	outUSD    *float64
	usage     store.UsageRepo
	logger    *zap.Logger
}

func (m *memoryProviderLLM) Complete(ctx context.Context, system, user string) (string, error) {
	at := time.Now()
	resp, err := m.client.Chat(ctx, provider.Request{
		Model:       m.model,
		Messages:    []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		MaxTokens:   m.maxTokens,
		Temperature: 0.2,
	})
	// 失败调用也要记（OK=false），否则「模型一直挂」在账本里完全看不见。
	m.record(ctx, at, resp, err)
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// record 写一行 llm_usage；失败只记日志，绝不上抛（后台增强不能因账本故障中断）。
func (m *memoryProviderLLM) record(ctx context.Context, at time.Time, resp provider.Response, callErr error) {
	if m.usage == nil {
		return
	}
	row := store.Usage{
		At:                at.UTC(),
		Purpose:           string(m.purpose),
		Model:             m.model,
		CorrelationID:     llmmeta.SafeCorrelationID(llmmeta.From(ctx).CorrelationID),
		InputTokens:       validTokens(resp.Usage.InputTokens),
		OutputTokens:      validTokens(resp.Usage.OutputTokens),
		CachedInputTokens: validTokens(resp.Usage.CachedInputTokens),
		CostUSD:           store.Cost(resp.Usage.InputTokens, resp.Usage.OutputTokens, m.inUSD, m.outUSD),
		Milliseconds:      int(time.Since(at).Milliseconds()),
		OK:                callErr == nil,
	}
	accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := m.usage.RecordUsage(accountCtx, row); err != nil && m.logger != nil {
		m.logger.Warn("后台模型调用记账失败",
			zap.String("action", "llm_usage"),
			zap.String("purpose", string(m.purpose)),
			zap.String("model", m.model),
			zap.Bool("ok", callErr == nil),
			zap.Int("cost_ms", row.Milliseconds),
			zap.Error(err),
		)
	}
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
		if stats.Frozen {
			logger.Info("记忆已冻结，本轮只读不写",
				zap.String("action", "memory_frozen"),
				zap.String("reason", stats.FrozenReason),
			)
			return
		}
		logger.Info("记忆提炼完成",
			zap.String("action", "memory_extract"),
			zap.Int("scanned", stats.Scanned),
			zap.Int("extracted", stats.Extracted),
			zap.Int("inserted", stats.Inserted),
			zap.Int("duplicated", stats.Duplicated),
			zap.Int("skipped", stats.Skipped),
			zap.Int("lease_lost", stats.LeaseLost),
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
	extractor := buildMemoryExtractor(cfg, db, logger)
	// 任务队列（M05）：装配失败时回落旧的水位扫描，不让提炼整体停掉。
	jobs, jerr := database.NewExtractionJobsRepo(db)
	if jerr != nil {
		logger.Warn("提炼任务队列装配失败，回落到水位扫描", zap.String("action", "memory_extract"), zap.Error(jerr))
		jobs = nil
	}
	// 提炼上下文（M06/M07）：EventRepo 同时提供「严格早于当前事件」的上界查询。
	recent, rerr := database.NewEventRepo(db)
	if rerr != nil {
		logger.Warn("提炼上下文装配失败，将不带上下文提炼", zap.String("action", "memory_extract"), zap.Error(rerr))
		recent = nil
	}
	worker, err := newMemoryWorker(memoryWorkerDeps{
		Config:        cfg.Memory,
		Source:        source,
		Jobs:          jobs,
		Recent:        recent,
		Repo:          repo,
		Profiles:      profiles,
		Soul:          repo,
		Extractor:     extractor,
		WatermarkPath: memoryWatermarkPath,
		FreezePath:    memoryFreezePath,
	})
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
		zap.Bool("frozen", cfg.Memory.Frozen),
		zap.Bool("soul_enabled", cfg.Memory.SoulEnabled),
		zap.Int("soul_max_entries", cfg.Memory.SoulMaxEntries),
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
