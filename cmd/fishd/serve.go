package main

// 本文件装配 0.11 的消费循环：Claim → Scheduler → Brain → Sender。
//
// 边界：
//   - 只做装配，不设计新状态机；准入/冷却/租约语义全部沿用 internal/scheduler；
//   - 不伪造 Task / Memory / Presence：Memory 与 Shared 显式传空快照（Scope 正确、无数据）；
//   - Brain 与 Sender 以接口注入，测试用 mock 跑通链路，不调用真实模型。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	senderlive "RaiseMyFish/internal/sender/live"
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
			OwnerIDs:      p.OwnerIDs,
			SelfIDs:       p.SelfIDs,
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

// buildConsumeWorkers 组装 Generate / Verify 两个 worker。
//
// 两个 handler 都必须**结清事件**再返回：scheduler 在 complete() 里用 ownSettlement 检查
// 「租约已释放且状态落到 done/skipped/dead/retry」，未结清会直接返回 ErrUnsettled 并终止消费循环。
// 结清用的都是契约里已有的边（queued→replying→sent→done、replying→dead、retry 走 Defer），不新增状态。
//
// Generate / Deliver / Verify 三个 handler 都必须设置：scheduler.launch 按 claim 的 stage 分派，
// 任一为 nil 都会在对应阶段裸奔调用。
func buildConsumeWorkers(pack persona.Pack, byChannel map[event.Channel]brain.Brain, snd sender.Sender, bus event.Bus, retryDelay time.Duration, mem memory.Reader, know knowledge.Reader, pres *presence.Table, log *zap.Logger) (scheduler.Workers, error) {
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

	generate := func(ctx context.Context, w scheduler.Work) (scheduler.Result, error) {
		// 没有 Task 说明调度器给错了阶段；这里拒绝而不是造一个假任务
		if w.Task == nil {
			return scheduler.Result{}, errors.New("调度工作缺少任务，拒绝生成回复")
		}
		ev := w.Claim.Event
		engine, ok := byChannel[ev.Channel]
		if !ok {
			return scheduler.Result{}, fmt.Errorf("通道 %s 未配置 Brain 路由", ev.Channel)
		}

		// 1. 先进 replying：契约只有 queued→replying 这条边，不能一步跳到 sent
		if err := bus.Mark(ctx, w.Claim, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
			return scheduler.Result{}, fmt.Errorf("进入执行中失败：%w", err)
		}
		// presence 是旁路观测：空表跳过，失败也不影响主链路
		if pres != nil {
			pres.Set(presence.State{Kind: presence.KindReplying, Channel: string(ev.Channel)})
		}
		defer func() {
			if pres != nil {
				pres.Clear(presence.KindReplying, string(ev.Channel))
			}
		}()

		scope := memory.Scope{Channel: ev.Channel, Session: ev.Session, Private: ev.Private}
		// 召回失败降级为空快照 + 日志，不阻塞回复（记忆是增强，不是前置条件）
		memCtx, shared := recallMemory(ctx, mem, scope, log)
		knowCtx := recallKnowledge(ctx, know, ev, log)

		input := brain.ContextInput{
			Event:     ev,
			Persona:   pack,
			Memory:    memCtx,
			Shared:    shared,
			Knowledge: knowCtx,
		}
		task := brain.Task{ID: w.Task.ID, EventID: w.Task.EventID, Channel: w.Task.Channel, Session: w.Task.Session}

		reply, err := engine.Reply(ctx, task, input)
		if err != nil {
			// 生成失败也要结清，否则事件卡在 replying
			if markErr := bus.Mark(ctx, w.Claim, []event.Status{event.StatusReplying}, event.StatusDead); markErr != nil {
				return scheduler.Result{}, errors.Join(err, markErr)
			}
			return scheduler.Result{}, err
		}

		// Scope 必须显式带上：sender.Reply.Validate 要求与事件通道/会话一致
		res, err := snd.Send(ctx, w.Claim, sender.Reply{Text: reply.Text, Scope: scope})
		if err != nil {
			if settleErr := settleFailure(ctx, bus, w.Claim, err, []event.Status{event.StatusReplying}, retryDelay); settleErr != nil {
				return scheduler.Result{}, errors.Join(err, settleErr)
			}
			return scheduler.Result{}, err
		}

		// 投递被接受才算发送成功；OK=false 不记 sending
		if pres != nil && res.OK {
			pres.Set(presence.State{Kind: presence.KindSending, Channel: string(ev.Channel), TTL: time.Minute})
		}

		// 2. 投递完成 → sent（Mark 会把 stage 推到 verify 并保留租约）
		if err := bus.Mark(ctx, w.Claim, []event.Status{event.StatusReplying}, event.StatusSent); err != nil {
			return scheduler.Result{}, fmt.Errorf("标记投递完成失败：%w", err)
		}
		// 3. 核验通过 → done；未核验则交回 retry，由 verify 阶段接手
		if err := settleDelivered(ctx, bus, w.Claim, res.Verified, retryDelay); err != nil {
			return scheduler.Result{}, err
		}
		return scheduler.Result{Replied: res.OK}, nil
	}

	// deliver 与 verify 是同一件事：事件已经投递出去，只需要 Resume 核验并结清。
	// 两者都必须显式设置——scheduler 在 stage=deliver 时会直接调 Workers.Deliver，
	// 留 nil 就是一次裸奔调用（panic），事件也永远结不清。
	resumeAndSettle := func(ctx context.Context, w scheduler.Work) (scheduler.Result, error) {
		// 失败结清只能从 queued 出发：契约里没有 sent→dead 这条边
		// （deliver/verify 阶段领取时状态本来就是 queued）。
		from := []event.Status{event.StatusQueued}

		res, err := snd.Resume(ctx, w.Claim)
		if err != nil {
			if settleErr := settleFailure(ctx, bus, w.Claim, err, from, retryDelay); settleErr != nil {
				return scheduler.Result{}, errors.Join(err, settleErr)
			}
			return scheduler.Result{}, err
		}
		if err := settleDelivered(ctx, bus, w.Claim, res.Verified, retryDelay); err != nil {
			return scheduler.Result{}, err
		}
		return scheduler.Result{Replied: res.OK}, nil
	}

	return scheduler.Workers{
		Generate: generate,
		Deliver:  resumeAndSettle,
		Verify:   resumeAndSettle,
	}, nil
}

// recallMemory 召回记忆与共享事实。
//
//   - mem 为 nil（未装配）→ 返回只带 Scope 的空快照，不报错；
//   - 召回失败 → 降级为只带 Scope 的空快照 + 记日志，**不阻塞回复**；
//   - 隐私：Scope 由事件派生，Reader 内部按 Scope 做隔离，这里不做任何越界拼装。
func recallMemory(ctx context.Context, mem memory.Reader, scope memory.Scope, log *zap.Logger) (memory.Context, memory.SharedState) {
	empty := memory.Context{Scope: scope}
	emptyShared := memory.SharedState{Scope: scope}
	if mem == nil {
		return empty, emptyShared
	}
	recalled, err := mem.Recall(ctx, scope)
	if err != nil {
		log.Warn("记忆召回失败，降级为空快照",
			zap.String("action", "memory_recall"),
			zap.String("channel", string(scope.Channel)),
			zap.Error(err))
		return empty, emptyShared
	}
	shared, err := mem.Shared(ctx, scope)
	if err != nil {
		log.Warn("共享事实召回失败，降级为空",
			zap.String("action", "shared_recall"),
			zap.String("channel", string(scope.Channel)),
			zap.Error(err))
		return recalled, emptyShared
	}
	return recalled, shared
}

// recallKnowledge 召回知识库条目。
//
//   - know 为 nil（[knowledge] enabled=false 或未装配）→ 返回 nil，Brain 按无知识处理；
//   - 召回失败 → 降级为 nil + 记日志，不阻塞回复。
func recallKnowledge(ctx context.Context, know knowledge.Reader, ev event.Event, log *zap.Logger) *knowledge.Context {
	if know == nil {
		return nil
	}
	query := knowledge.Query{
		Scope: knowledge.Scope{Channel: ev.Channel, Session: ev.Session, Private: ev.Private},
		Text:  ev.Text,
	}
	got, err := know.Recall(ctx, query)
	if err != nil {
		log.Warn("知识召回失败，降级为无知识",
			zap.String("action", "knowledge_recall"),
			zap.String("channel", string(ev.Channel)),
			zap.Error(err))
		return nil
	}
	return &got
}

// settleDelivered 把「已投递」的事件收尾：核验通过就 done，否则交回 retry 让 verify 阶段接手。
func settleDelivered(ctx context.Context, bus event.Bus, c event.Claim, verified bool, retryDelay time.Duration) error {
	from := []event.Status{event.StatusQueued, event.StatusSent}
	if verified {
		if err := bus.Mark(ctx, c, from, event.StatusDone); err != nil {
			return fmt.Errorf("标记完成失败：%w", err)
		}
		return nil
	}
	// Defer 会置 retry 并释放租约，是「未结清 → 可再领取」的正规路径
	if err := bus.Defer(ctx, c, time.Now().UTC().Add(retryDelay)); err != nil {
		return fmt.Errorf("交回重试失败：%w", err)
	}
	return nil
}

// settleFailure 按发送错误类别结清事件。
//
// 只有 KindRetry（可重试的临时故障）交回 retry；其余（dead/auth/input/skipped/uncertain）
// 一律置 dead——尤其是 uncertain（可能已经发出去了），重投会重复发弹幕，宁可停下来让人看。
func settleFailure(ctx context.Context, bus event.Bus, c event.Claim, cause error, from []event.Status, retryDelay time.Duration) error {
	var sendErr *sender.Error
	if errors.As(cause, &sendErr) && sendErr.Kind == sender.KindRetry {
		if err := bus.Defer(ctx, c, time.Now().UTC().Add(retryDelay)); err != nil {
			return fmt.Errorf("交回重试失败：%w", err)
		}
		return nil
	}
	if err := bus.Mark(ctx, c, from, event.StatusDead); err != nil {
		return fmt.Errorf("标记失败事件失败：%w", err)
	}
	return nil
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

// buildSender 用 `[sender]` 配置装配 live 发送器。
func buildSender(cfg config.Config, resources *infra.Resources, log *zap.Logger) (sender.Sender, error) {
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

// buildConsume 装配消费循环；未启用（无 [scheduler] / 无 Brain 路由 / 通道关闭）时返回 nil。
func buildConsume(cfg config.Config, resources *infra.Resources, routes map[string]brain.Brain, pack persona.Pack, bus event.Bus, know knowledge.Reader, pres *presence.Table, log *zap.Logger) (*scheduler.Scheduler, error) {
	if !consumeEnabled(cfg, routes) {
		return nil, nil
	}

	// 记忆 Reader 装配失败是启动级错误：召回是消费循环的核心输入，不能静默降级成「永远失忆」。
	memRepo, err := database.NewMemoryRepo(resources.DB)
	if err != nil {
		return nil, fmt.Errorf("装配记忆仓储失败：%w", err)
	}
	memReader, err := memory.NewReader(memRepo, memRepo, memory.ReaderOptions{})
	if err != nil {
		return nil, fmt.Errorf("装配记忆 Reader 失败：%w", err)
	}

	options := buildSchedulerOptions(cfg.Scheduler)
	byChannel, err := resolveRoutes(options.Channels, routes)
	if err != nil {
		return nil, err
	}
	snd, err := buildSender(cfg, resources, log)
	if err != nil {
		return nil, err
	}
	workers, err := buildConsumeWorkers(pack, byChannel, snd, bus, options.RetryDelay, memReader, know, pres, log)
	if err != nil {
		return nil, err
	}
	repo, err := database.NewSchedulerRepo(resources.DB)
	if err != nil {
		return nil, err
	}
	return scheduler.New(bus, repo, options, workers, log)
}

// consumeEnabled 判断是否接消费循环：有调度配置、有 Brain 路由、且 live 通道开启。
func consumeEnabled(cfg config.Config, routes map[string]brain.Brain) bool {
	return cfg.Scheduler != nil && len(routes) > 0 && cfg.Channels.Live.Enabled
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
