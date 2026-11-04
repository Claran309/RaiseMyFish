package main

// 本文件装配 0.11 的消费循环：Claim → Scheduler → Brain → Sender。
//
// 边界：
//   - 只做装配，不设计新状态机；准入/冷却/租约语义全部沿用 internal/scheduler；
//   - 不伪造 Task / Memory / Presence：不需要召回时 Memory 与 Shared 显式传空快照（Scope 正确、无数据）；
//   - Brain 与 Sender 以接口注入，测试用 mock 跑通链路，不调用真实模型。
//
// 各阶段实现按职责拆在同目录：generate.go / deliver.go / verify.go / recall.go / react.go。

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/gift"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	sendercomment "RaiseMyFish/internal/sender/comment"
	senderlive "RaiseMyFish/internal/sender/live"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// buildSchedulerOptions 把配置转成调度器参数。零值已在 config 层补过默认。
func buildSchedulerOptions(c *config.Scheduler) scheduler.Options {
	o := scheduler.DefaultOptions()
	o.MaxConcurrent = c.MaxConcurrent
	o.MaxPending = c.MaxPending
	o.ClaimBatch = c.ClaimBatch
	o.PollInterval = time.Duration(c.PollIntervalMS) * time.Millisecond
	o.RetryDelay = time.Duration(c.RetryDelayMS) * time.Millisecond
	o.OperationTimeout = time.Duration(c.OperationTimeoutMS) * time.Millisecond
	o.WorkerTimeout = time.Duration(c.WorkerTimeoutMS) * time.Millisecond
	o.Location = time.UTC

	o.Channels = make(map[event.Channel]scheduler.ChannelPolicy, len(c.Channels))
	for ch, p := range c.Channels {
		o.Channels[ch] = scheduler.ChannelPolicy{
			// 公开/私密是通道语义，不是策略：scheduler 的 ACL 用它们决定能不能处理
			Public:        ch == event.ChLive || ch == event.ChComment,
			Private:       ch == event.ChDM,
			Brain:         p.Brain,
			RatePerMinute: p.RatePerMinute,
			Burst:         p.Burst,
			DailyQuota:    p.DailyQuota,
			Cooldown:      time.Duration(p.CooldownMS) * time.Millisecond,
			MinRunes:      p.MinRunes,
			RepeatWindow:  time.Duration(p.RepeatWindowMS) * time.Millisecond,
			// 指针只用于配置层区分「未写」；进入调度后就是确定的布尔策略。
			ReplyOnlyMention: p.ReplyOnlyMention != nil && *p.ReplyOnlyMention,
			ReplyMentionBack: p.ReplyMentionBack != nil && *p.ReplyMentionBack,
			MaxReplyRunes:    p.MaxReplyRunes,
			SelfNames:        p.SelfNames,
			OwnerIDs:         p.OwnerIDs,
			SelfIDs:          p.SelfIDs,
		}
	}
	return o
}

// resolveRoutes 把「通道 → brain 路由名」解析成「通道 → Brain 实例」。
//
// 配置里写了不存在的路由就报错，不在运行期静默丢事件。
func resolveRoutes(policies map[event.Channel]scheduler.ChannelPolicy, routes map[string]brain.Brain) (map[event.Channel]brain.Brain, error) {
	byChannel := make(map[event.Channel]brain.Brain, len(policies))
	for ch, p := range policies {
		engine, ok := routes[p.Brain]
		if !ok || engine == nil {
			return nil, fmt.Errorf("通道 %s 的 brain 路由 %q 未装配", ch, p.Brain)
		}
		byChannel[ch] = engine
	}
	return byChannel, nil
}

// buildConsumeWorkers 组装 Generate / Deliver / Verify 三个 worker。
//
// 三个 handler 都必须**结清事件**再返回：scheduler 在 complete() 里用 ownSettlement 检查
// 「租约已释放且状态落到 done/skipped/dead/retry」，未结清会直接返回 ErrUnsettled 并终止消费循环。
// 结清用的都是契约里已有的边（queued→replying→sent→done、replying→dead、retry 走 Defer），不新增状态。
// 注意：真实 sender 会在 Send/Resume 的事务里原子结清输入事件并释放租约（见 contracts/event.md），
// 此时 handler 不能再拿旧 claim 写 Bus，否则只会得到「事件租约已失效」。
//
// Generate / Deliver / Verify 三个 handler 都必须设置：scheduler.launch 按 claim 的 stage 分派，
// 任一为 nil 都会在对应阶段裸奔调用。
func buildConsumeWorkers(live scheduler.ChannelPolicy, pack persona.Pack, byChannel map[event.Channel]brain.Brain, snd sender.Sender, bus event.Bus, retryDelay time.Duration, mem memory.Reader, know knowledge.Reader, viewers store.ViewerRepo, soulPath string, pres *presence.Table, state func(time.Time) string, react reactFactory, log *zap.Logger) (scheduler.Workers, error) {
	if snd == nil {
		return scheduler.Workers{}, errors.New("发送器不能为 nil")
	}
	if bus == nil {
		return scheduler.Workers{}, errors.New("事件总线不能为 nil（worker 必须结清事件）")
	}
	if retryDelay <= 0 {
		return scheduler.Workers{}, errors.New("重试间隔必须为正")
	}
	if len(byChannel) == 0 {
		return scheduler.Workers{}, errors.New("没有可用的 Brain 路由")
	}
	if log == nil {
		return scheduler.Workers{}, errors.New("日志器不能为 nil")
	}

	generate := generateHandler(generateDeps{
		live:       live,
		pack:       pack,
		brains:     byChannel,
		sender:     snd,
		bus:        bus,
		retryDelay: retryDelay,
		memory:     mem,
		knowledge:  know,
		soulPath:   soulPath,
		viewers:    viewers,
		presence:   pres,
		state:      state,
		react:      react,
		log:        log,
	})
	// deliver 与 verify 是同一件事：事件已经投递出去，只需要 Resume 核验并结清。
	resume := resumeDeps{sender: snd, bus: bus, retryDelay: retryDelay}

	return scheduler.Workers{
		Generate: withErrorLog(log, "generate_failed", generate),
		Deliver:  withErrorLog(log, "deliver_failed", deliverHandler(resume)),
		Verify:   withErrorLog(log, "verify_failed", verifyHandler(resume)),
	}, nil
}

// senderWiring 是 buildSender 需要的纯配置投影，单独抽出来便于单测断言「传了什么参数」。
type senderWiring struct {
	Node    senderlive.NodeOptions
	Service senderlive.Options
}

// resolveSenderWiring 把配置投影成发送器参数。
//
//   - 脚本/超时/self_id 来自 `[sender]`（发送脚本与采集脚本不是同一个文件）；
//   - Cookie 仍来自采集侧：同一个账号的凭据只解析一处，避免两处配置不一致；
//   - self_id 留空时回落到采集侧同一账号的 UID。
func resolveSenderWiring(cfg config.Config, minInterval time.Duration) (senderWiring, error) {
	if cfg.Sender == nil {
		return senderWiring{}, errors.New("未配置 [sender] 发送通道，无法装配发送器")
	}

	node := senderlive.DefaultNodeOptions()
	node.NodePath = cfg.Sender.NodePath
	node.ScriptPath = cfg.Sender.ScriptPath
	node.Cookie = cfg.Collector.Live.Cookie
	node.CookieFile = cfg.Collector.Live.CookieFile
	if cfg.Sender.HTTPTimeoutMS > 0 {
		node.Timeout = time.Duration(cfg.Sender.HTTPTimeoutMS) * time.Millisecond
	}

	selfID := strings.TrimSpace(cfg.Sender.SelfID)
	if selfID == "" {
		selfID = strings.TrimSpace(cfg.Collector.Live.SelfID)
	}

	service := senderlive.DefaultOptions()
	service.Enabled = cfg.Channels.Live.Enabled
	service.SelfID = selfID
	service.MinInterval = minInterval
	return senderWiring{Node: node, Service: service}, nil
}

// buildLiveSender 用 `[sender]` 配置装配 live 发送器。
func buildLiveSender(cfg config.Config, resources *infra.Resources, log *zap.Logger) (sender.Sender, error) {
	policy := sender.DefaultPolicy()
	if cfg.Scheduler != nil {
		if live, ok := cfg.Scheduler.Channels[event.ChLive]; ok && live.CooldownMS > 0 {
			policy.MinInterval = time.Duration(live.CooldownMS) * time.Millisecond
		}
	}
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("发送策略无效：%w", err)
	}

	wiring, err := resolveSenderWiring(cfg, policy.MinInterval)
	if err != nil {
		return nil, err
	}
	repo, err := database.NewSendRepo(resources.DB, policy)
	if err != nil {
		return nil, err
	}
	transport, err := senderlive.NewNodeTransport(wiring.Node)
	if err != nil {
		return nil, fmt.Errorf("装配发送通道失败：%w", err)
	}
	return senderlive.New(repo, transport, wiring.Service, log)
}

// buildSender 按已启用通道装配发送路由；Generate/Deliver 仍只看到一个 sender.Sender。
//
// live 走既有 Node 传输与投递账本；comment 走纯 Go HTTP（1条/分钟硬限频）。
// 两者都没有启用时明确报错，避免消费循环「能生成却不能发」。
func buildSender(cfg config.Config, resources *infra.Resources, log *zap.Logger) (sender.Sender, error) {
	if cfg.Sender == nil {
		return nil, errors.New("未配置 [sender] 发送通道，无法装配发送器")
	}
	routes := make(map[event.Channel]sender.Sender, 2)
	if cfg.Channels.Live.Enabled {
		liveSender, err := buildLiveSender(cfg, resources, log)
		if err != nil {
			return nil, err
		}
		routes[event.ChLive] = liveSender
	}
	if cfg.Sender.Comment != nil && cfg.Sender.Comment.Enabled {
		o := sendercomment.DefaultOptions()
		o.Cookie = cfg.Collector.Live.Cookie
		o.CookieFile = cfg.Collector.Live.CookieFile
		if cfg.Sender.HTTPTimeoutMS > 0 {
			o.HTTPTimeout = time.Duration(cfg.Sender.HTTPTimeoutMS) * time.Millisecond
		}
		o.MinInterval = time.Minute
		commentSender, err := sendercomment.New(o, log)
		if err != nil {
			return nil, fmt.Errorf("装配评论发送通道失败：%w", err)
		}
		routes[event.ChComment] = commentSender
	}
	if cfg.Collector.Comment.Enabled && routes[event.ChComment] == nil {
		log.Warn("评论采集已启用但未配置评论发送，评论事件将无法回复", zap.String("action", "comment_sender_missing"))
	}
	// 私信：只在 [sender.dm] 开启且凭据可用时注册路由
	if dmSender, err := buildDMSender(cfg, log); err != nil {
		return nil, fmt.Errorf("装配私信发送通道失败：%w", err)
	} else if dmSender != nil {
		routes[event.ChDM] = dmSender
	}
	if cfg.Collector.DM.Enabled && routes[event.ChDM] == nil {
		log.Warn("私信采集已启用但未装配私信发送，私信事件将无法回复", zap.String("action", "dm_sender_missing"))
	}
	return newSendRouter(routes)
}

// defaultOwnerMID 是主人的 B站 UID（派发单给定）：永不取关、私信默认允许。
const defaultOwnerMID = "313591854"

// ownerMID 优先用调度配置里的 owner_ids，缺省回落常量。
func ownerMID(cfg config.Config) string {
	if cfg.Scheduler != nil {
		if live, ok := cfg.Scheduler.Channels[event.ChLive]; ok && len(live.OwnerIDs) > 0 {
			if mid := strings.TrimSpace(live.OwnerIDs[0]); mid != "" {
				return mid
			}
		}
	}
	return defaultOwnerMID
}

// selfMID 是本鱼自己的 UID（用于「永不取关自己」）。
func selfMID(cfg config.Config) string {
	if cfg.Sender != nil {
		if mid := strings.TrimSpace(cfg.Sender.SelfID); mid != "" {
			return mid
		}
	}
	return strings.TrimSpace(cfg.Collector.Live.SelfID)
}

// buildGiftQuota 装配送礼额度账本（ReAct 的 quota 工具 + gift 副作用共用）。
// 装配失败只降级为 nil：额度未知比直接崩掉好。
func buildGiftQuota(cfg config.Config, log *zap.Logger) reasoning.QuotaSource {
	dir := filepath.Dir(cfg.Runtime.StateFile)
	quota, err := gift.NewFileQuota(filepath.Join(dir, "gift-ledger.json"))
	if err != nil {
		log.Warn("送礼额度账本装配失败，quota 工具降级", zap.String("action", "quota_setup"), zap.Error(err))
		return nil
	}
	return quota
}

// sideEffectDeps 把配置投影成副作用工具的护栏参数。
//
// 注意：平台执行器（送礼/关注/点赞/私信/动态）本批尚未接入，置 nil 时工具会明确回「未配置」，
// 但护栏（开关/白名单/额度/限频/不取关主人）全部生效并有单测覆盖。
func sideEffectDeps(cfg config.Config, quota reasoning.QuotaSource, exec toolExecutors, room string, log *zap.Logger) reasoning.SideEffectDeps {
	return reasoning.SideEffectDeps{
		Enabled:          cfg.Reasoning.AllowSideEffectTools,
		Gift:             exec.Gift,
		Social:           exec.Social,
		QuotaView:        quota,
		GiftEnabled:      cfg.Gift.Enabled,
		GiftReal:         cfg.Gift.Real,
		GiftID:           cfg.Gift.GiftID,
		MaxNum:           cfg.Gift.MaxNum,
		DailyLimit:       cfg.Gift.DailyLimit,
		AllowedRooms:     cfg.Gift.AllowedRooms,
		AllowPostDynamic: false, // 发动态默认关，需要单独开
		OwnerMID:         ownerMID(cfg),
		SelfMID:          selfMID(cfg),
		Room:             room,
		Log:              log,
	}
}

// buildConsume 装配消费循环；未启用（无 [scheduler] / 无 Brain 路由 / 通道关闭）时返回 nil。
// 返回的发送器与消费循环共用同一实例，供通道注册表复用（同一限流与暂停状态）。
func buildConsume(cfg config.Config, resources *infra.Resources, routes map[string]brain.Brain, pack persona.Pack, bus event.Bus, know knowledge.Reader, pres *presence.Table, log *zap.Logger) (*scheduler.Scheduler, sender.Sender, error) {
	if !consumeEnabled(cfg, routes) {
		return nil, nil, nil
	}

	// 记忆 Reader 装配失败是启动级错误：召回是消费循环的核心输入，不能静默降级成「永远失忆」。
	memRepo, err := database.NewMemoryRepo(resources.DB)
	if err != nil {
		return nil, nil, fmt.Errorf("装配记忆仓储失败：%w", err)
	}
	memReader, err := memory.NewReader(memRepo, memRepo, memory.ReaderOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("装配记忆 Reader 失败：%w", err)
	}

	options := buildSchedulerOptions(cfg.Scheduler)
	byChannel, err := resolveRoutes(options.Channels, routes)
	if err != nil {
		return nil, nil, err
	}
	snd, err := buildSender(cfg, resources, log)
	if err != nil {
		return nil, nil, err
	}
	// history 工具需要读最近事件；EventRepo 只读复用，不新增写路径。
	eventRepo, err := database.NewEventRepo(resources.DB)
	if err != nil {
		return nil, nil, err
	}
	// ReAct 的 token 也要进 llm_usage，否则日报费用偏低
	usageRepo, err := database.NewUsageRepo(resources.DB)
	if err != nil {
		return nil, nil, fmt.Errorf("装配用量账本失败：%w", err)
	}
	// 观众档案：没启用就不建仓储（Generate 里 viewers==nil 自然不注入）
	var viewerRepo store.ViewerRepo
	if cfg.Viewer.Enabled {
		if repo, err := database.NewViewerRepo(resources.DB); err == nil {
			viewerRepo = repo
		}
	}
	quota := buildGiftQuota(cfg, log)
	execs := buildToolExecutors(cfg, log)
	react, err := buildReasonerFactory(cfg, memReader, know, pres, eventRepo, quota, execs, usageRepo, log)
	if err != nil {
		return nil, nil, err
	}
	workers, err := buildConsumeWorkers(options.Channels[event.ChLive], pack, byChannel, snd, bus, options.RetryDelay, memReader, know, viewerRepo, cfg.Memory.SoulPath, pres, brainStateSource(pres, cfg.Watch), react, log)
	if err != nil {
		return nil, nil, err
	}
	repo, err := database.NewSchedulerRepo(resources.DB)
	if err != nil {
		return nil, nil, err
	}
	sched, err := scheduler.New(bus, repo, options, workers, log)
	return sched, snd, err
}

// consumeEnabled 判断是否接消费循环：有调度配置、有 Brain 路由、且至少一个通道开启。
func consumeEnabled(cfg config.Config, routes map[string]brain.Brain) bool {
	return cfg.Scheduler != nil && len(routes) > 0 && (cfg.Channels.Live.Enabled || cfg.Channels.Comment.Enabled)
}

// withScheduler 让调度循环与服务主循环并行运行；任一返回就整体收敛。
func withScheduler(ctx context.Context, sched *scheduler.Scheduler, log *zap.Logger, serve func(context.Context) error) error {
	if sched == nil {
		return serve(ctx)
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan error, 2)
	go func() { results <- sched.Run(child) }()
	go func() { results <- serve(child) }()

	first := <-results
	cancel()
	second := <-results

	// 外部取消属于正常停止
	if ctx.Err() != nil {
		return nil
	}
	if first != nil {
		log.Error("消费循环异常退出", zap.String("action", "consume_loop_error"), zap.Error(first))
		return first
	}
	return second
}
