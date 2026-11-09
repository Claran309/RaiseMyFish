package main

// ReAct 思考链：工厂、触发判定、执行与模型/记账适配。

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// reactFactory 按事件 Scope 现造一个 ReAct Agent；nil 表示不启用 ReAct。
//
// 为什么是工厂而不是单个实例：工具要在**当前事件**的 Scope 内查询，
// 并发 worker 共享一个可变 Scope 会串会话（数据竞争 + 隐私越界）。
// fromID 是本条事件的发送者平台 UID，供「我是谁」做精确身份查询。
// knowledgePreloaded 表示 Prepare 已把知识写进资料段，工厂据此不再注册
// knowledge_search（避免同一轮重复召回）；零命中时传 false，工具保留。
type reactFactory func(scope memory.Scope, fromID string, knowledgePreloaded bool) (*reasoning.Agent, error)

// reactRuntime 是 Generate 阶段用到的 ReAct 装配：工厂 + 触发策略。
//
// 这里**刻意不带结果缓存**：ReAct 的最终答案依赖现场状态、提问人身份、
// 双方最近对话和时间，同一句话隔 30 秒再问，正确答案就可能变了。
// 缓存最终答案会造成「同房间跨人复用」（A 问的答案发给 B）和
// 「纠正后仍复用旧回答」（用户说「不是那个」但拿到同样的答复）。
// 需要省钱时应该缓存**只读检索结果**（记忆/知识/档案），而不是成品回复。
type reactRuntime struct {
	factory      reactFactory
	defaultReact bool
	// budget 与单步 Brain 使用同一口径；runReact 通过 brain.Prepare 统一校验与裁剪。
	budget brain.Budget
	// finalReserve 是从本轮预算里给「最终正文」留的时间：准备（读取/召回/
	// embedding/Prepare）与 ReAct 工具都在父 deadline 减预留的 prepCtx 内跑，
	// 即使 ReAct 把准备段耗光，回落单步生成仍有时间。
	finalReserve time.Duration
}

// shouldUseReAct 判断这条弹幕要不要走多步思考。
//
// default_react=true（默认）：只有短句保持单步，其余全走 ReAct，
// 不再依赖「梗/刚才」这类词表——词表漏判就查不到记忆，可用性优先。
// default_react=false：保留旧两段式，仅命中召回线索时走 ReAct。
//
// shouldUseReAct 判断这条弹幕要不要走多步思考。
//
// default_react=true（默认）：**全量走 ReAct**（含短句）——可用性优先，
// 短句也可能需要查记忆/知识（「记得吗」「摸我」），不再按长度区分。
// default_react=false：保留旧两段式，仅命中召回线索时走 ReAct。
func shouldUseReAct(text string, defaultReact bool) bool {
	if defaultReact {
		return true
	}
	d := reasoning.Decide(text)
	return d.NeedMemory || d.NeedKnowledge
}

// prepareDeadline 给「准备 + ReAct」划出预算：父 deadline 减去最终正文预留。
//
// 预留按剩余时间的一半封顶：剩余本来就不多时，不能让准备阶段 0 预算直接超时，
// 否则回落单步反而一点时间拿不到。没有 deadline 或没有预留时原样返回父 ctx。
func prepareDeadline(ctx context.Context, reserve time.Duration) (context.Context, context.CancelFunc) {
	if reserve <= 0 {
		return ctx, func() {}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return ctx, func() {}
	}
	if half := remaining / 2; reserve > half {
		reserve = half
	}
	if reserve <= 0 {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, deadline.Add(-reserve))
}

// runReact 执行一次 ReAct 并记录预算相关日志（步数/工具/降级/耗时）。
//
// 不做结果缓存：最终答案依赖现场、提问人、最近对话与时间，
// 复用成品会带来跨人串答案和「纠正后照旧」两类错误（见 reactRuntime 注释）。
//
// 上下文与单步 Brain 同源：input 是 ContextBuilder 组装的统一快照，
// 经 brain.Prepare 完成与单步完全相同的校验/预算裁剪后进入 user 段。
// 准备失败是明确停止：不再「渲染失败只带问题」继续，回落策略交给调用方。
//
// 返回值带 Result（含 Evidence）：调用方在 Text 为空回落单步时，
// 把已验证的工具观察注入普通 Brain，避免「查对了但回落又不知道」。
func runReact(ctx context.Context, runtime *reactRuntime, scope memory.Scope, ev event.Event, task brain.Task, input brain.ContextInput, log *zap.Logger) (reasoning.Result, error) {
	if runtime == nil || runtime.factory == nil {
		return reasoning.Result{}, nil
	}
	prepared, err := brain.Prepare(ctx, task, input, runtime.budget)
	if err != nil {
		return reasoning.Result{}, fmt.Errorf("准备 ReAct 生成输入失败：%w", err)
	}
	if prepared.Truncated {
		log.Warn("ReAct 上下文超出预算已裁剪",
			zap.String("action", "react_context"),
			zap.String("event_id", ev.ID),
		)
	}
	// 知识已注入资料段 → 不再注册 knowledge_search，避免同一轮重复召回；
	// 零命中（IncludedIDs 为空）时保留工具，模型可以换关键词主动查。
	knowledgePreloaded := prepared.KnowledgeInjected()
	agent, err := runtime.factory(scope, ev.FromID, knowledgePreloaded)
	if err != nil || agent == nil {
		return reasoning.Result{}, err
	}
	started := time.Now()
	res, err := agent.Run(ctx, reasoning.Input{
		System:   prepared.Messages[0].Content,
		Context:  prepared.Context,
		Question: prepared.Question,
	})
	elapsed := time.Since(started).Milliseconds()
	log.Info("ReAct 执行完成",
		zap.String("action", "react"),
		zap.String("event_id", ev.ID),
		zap.Int("steps", res.Steps),
		zap.Strings("tools_used", res.Tools),
		zap.Int("evidence", len(res.Evidence)),
		zap.Bool("knowledge_preloaded", knowledgePreloaded),
		zap.Bool("degraded", res.Degraded),
		zap.String("degrade_reason", res.Reason),
		zap.Int64("latency_ms", elapsed),
	)
	if err != nil {
		return res, err
	}
	return res, nil
}

// buildReasonerFactory 装配 ReAct：模型走主路由，工具走记忆/知识/状态/历史。
//
// viewerSearch 为 nil 时不注册 viewer_search；其余工具照旧。
// giftGate 是可选的动态送礼开关（面板热更新），nil 时用配置值。
// sideLimiter 由进程装配一次，跨事件共享限频；每个事件仍各自建工具箱（Scope/Room 不共享）。
// 返回 nil 表示不启用（配置关闭、没模型、或一个工具都装不起来）——此时 serve 仍走单步。
func buildReasonerFactory(cfg config.Config, mem memory.Reader, know knowledge.Reader, pres *presence.Table, events *database.EventRepo, viewerSearch reasoning.ViewerProfileSearcher, quota reasoning.QuotaSource, exec toolExecutors, usage store.UsageRepo, giftGate func() bool, sideLimiter *reasoning.SideEffectLimiter, log *zap.Logger) (reactFactory, error) {
	if !cfg.Reasoning.Enabled {
		log.Info("ReAct 未启用（配置关闭）", zap.String("action", "react_disabled"))
		return nil, nil
	}
	blueprint, err := buildReasoningLLM(cfg, usage, log)
	if err != nil {
		return nil, err
	}
	if blueprint == nil {
		log.Info("ReAct 未启用（没有可用模型路由）", zap.String("action", "react_disabled"))
		return nil, nil
	}
	opts := reasoning.Options{
		MaxSteps: cfg.Reasoning.MaxSteps,
		Timeout:  time.Duration(cfg.Reasoning.TimeoutMS) * time.Millisecond,
	}
	var presenceFn func(time.Time) string
	if pres != nil {
		presenceFn = pres.Summary
	}

	return func(scope memory.Scope, fromID string, knowledgePreloaded bool) (*reasoning.Agent, error) {
		// 预召回命中时把知识工具摘掉：资料已在 Context 里，再查一遍是重复召回。
		// 零命中时保留工具（know 原样传入），让模型能换关键词主动查。
		toolKnowledge := know
		if knowledgePreloaded {
			toolKnowledge = nil
		}
		recall, err := reasoning.BuildTools(reasoning.ToolDeps{
			Memory:    mem,
			Knowledge: toolKnowledge,
			Presence:  presenceFn,
			History:   events,
			Scope:     scope,
		})
		if err != nil {
			return nil, err
		}
		// 只读 7 个：观众史/回复史/直播状态/额度/时间/画面/搜索；档案语义检索按依赖额外注册
		readonly, err := reasoning.BuildReadOnlyTools(reasoning.ReadOnlyDeps{
			Viewer:       events,
			ViewerSearch: viewerSearch,
			Replies:      events,
			Live:         exec.Live,
			Quota:        quota,
			QuotaLimit:   cfg.Gift.DailyLimit,
			Frame:        exec.Frame,
			FrameRead:    exec.FrameRead,
			Search:       exec.Search,
			Room:         scope.Session,
			SpeakerUID:   fromID,
			Now:          time.Now,
		})
		if err != nil {
			return nil, err
		}
		boxes := []*reasoning.Toolbox{recall, readonly}
		// 副作用 5 个：默认关；关了就不注册，模型连选项都看不见
		if cfg.Reasoning.AllowSideEffectTools {
			side, err := reasoning.BuildSideEffectTools(sideEffectDeps(cfg, quota, exec, scope.Session, giftGate, sideLimiter, log))
			if err != nil {
				return nil, err
			}
			boxes = append(boxes, side)
		}
		box, err := reasoning.MergeToolboxes(boxes...)
		if err != nil {
			return nil, err
		}
		// 每次事件派生一个适配器：账本行里的通道就是这条弹幕的通道
		llm := &providerLLM{blueprint: blueprint, channel: scope.Channel, log: log}
		return reasoning.New(llm, box, opts, log)
	}, nil
}

// buildReasoningLLM 用主模型路由装配 ReAct 的模型蓝图；没启用 LLM 时返回 nil。
//
// 刻意不复用 buildBrains：Brain 走的是完整 prompt 组装，
// 而 ReAct 需要的是「给定 system/user 原样补全」。
// 但记账口径与 Brain 保持一致：每次 Chat 都写一行 llm_usage（含失败调用）。
func buildReasoningLLM(cfg config.Config, usage store.UsageRepo, log *zap.Logger) (*reasoningModel, error) {
	if !cfg.LLM.Enabled || len(cfg.LLM.Routing) == 0 {
		return nil, nil
	}
	key := "default"
	if _, ok := cfg.LLM.Routing[key]; !ok {
		keys := make([]string, 0, len(cfg.LLM.Routing))
		for k := range cfg.LLM.Routing {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		key = keys[0]
	}
	name, prov, model, err := cfg.LLM.Resolve(key)
	if err != nil {
		return nil, fmt.Errorf("解析 ReAct 模型路由失败：%w", err)
	}
	client, err := provider.NewOpenAI(name, prov.BaseURL, prov.APIKey, &http.Client{}, time.Duration(cfg.LLM.AttemptTimeoutMS)*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("装配 ReAct 模型客户端失败：%w", err)
	}
	blueprint := &reasoningModel{
		client:      client,
		model:       model,
		maxTokens:   cfg.LLM.MaxReplyTokens,
		temperature: 0.3,
		usage:       usage,
	}
	// 报价可选：没配就记 nil 费用，而不是编一个 0
	if price, ok := prov.Prices[model]; ok {
		blueprint.inUSD = price.InputUSDPerMillion
		blueprint.outUSD = price.OutputUSDPerMillion
	}
	log.Info("ReAct 模型已装配",
		zap.String("action", "react_setup"),
		zap.String("route", key),
		zap.String("model", model),
		zap.Int("max_steps", cfg.Reasoning.MaxSteps),
		zap.Int("timeout_ms", cfg.Reasoning.TimeoutMS),
		zap.Bool("usage_recording", usage != nil),
	)
	return blueprint, nil
}

// reasoningModel 是 ReAct 的模型蓝图：客户端 + 模型名 + 可选单价 + 账本。
//
// 每次事件再派生出 providerLLM（带上该事件的通道），避免共享可变状态。
type reasoningModel struct {
	client      provider.Provider
	model       string
	maxTokens   int
	temperature float64
	inUSD       *float64
	outUSD      *float64
	usage       store.UsageRepo
}

// providerLLM 把 provider.Provider 适配成 reasoning.LLM，并在每次调用后记账。
type providerLLM struct {
	blueprint *reasoningModel
	channel   event.Channel
	log       *zap.Logger
}

func (p *providerLLM) Complete(ctx context.Context, system, user string) (string, error) {
	at := time.Now()
	resp, err := p.blueprint.client.Chat(ctx, provider.Request{
		Model:       p.blueprint.model,
		Messages:    []provider.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		MaxTokens:   p.blueprint.maxTokens,
		Temperature: p.blueprint.temperature,
	})
	// 失败调用也要记（OK=false），否则「模型一直挂」在账本里完全看不见
	p.record(ctx, at, resp, err)
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// record 写一行 llm_usage。
//
// 记账失败只记日志、绝不上抛：ReAct 是增强能力，不能因为账本写不进去就让弹幕收不到回复。
func (p *providerLLM) record(ctx context.Context, at time.Time, resp provider.Response, callErr error) {
	if p.blueprint.usage == nil {
		return
	}
	row := store.Usage{
		At:           at.UTC(),
		Channel:      p.channel,
		Model:        p.blueprint.model,
		InputTokens:  validTokens(resp.Usage.InputTokens),
		OutputTokens: validTokens(resp.Usage.OutputTokens),
		CostUSD:      p.cost(resp.Usage),
		Milliseconds: int(time.Since(at).Milliseconds()),
		OK:           callErr == nil,
	}
	// 父 ctx 可能已经超时（ReAct 预算耗尽），记账用脱离取消的 ctx，但只给 2 秒
	accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := p.blueprint.usage.RecordUsage(accountCtx, row); err != nil {
		p.log.Warn("ReAct 记账失败",
			zap.String("action", "react_usage"),
			zap.String("result", "failed"),
			zap.String("ch", string(p.channel)),
			zap.String("model", row.Model),
			zap.Int("cost_ms", row.Milliseconds),
			zap.Error(err),
		)
	}
}

// cost 与 Brain 同一口径：缺 token 或缺报价就是 nil，不编造 0。
func (p *providerLLM) cost(u provider.Usage) *float64 {
	return store.Cost(u.InputTokens, u.OutputTokens, p.blueprint.inUSD, p.blueprint.outUSD)
}

// validTokens 把负数/越界 token 当作未知（nil），不让脏数据进账本。
func validTokens(n *int) *int {
	if n == nil || *n < 0 || int64(*n) > 2147483647 {
		return nil
	}
	return n
}
