package main

// ReAct 思考链：工厂、触发判定、执行与模型/记账适配。

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// reactFactory 按事件 Scope 现造一个 ReAct Agent；nil 表示不启用 ReAct。
//
// 为什么是工厂而不是单个实例：工具要在**当前事件**的 Scope 内查询，
// 并发 worker 共享一个可变 Scope 会串会话（数据竞争 + 隐私越界）。
type reactFactory func(scope memory.Scope) (*reasoning.Agent, error)

// reactRuntime 是 Generate 阶段用到的 ReAct 装配：工厂 + 触发策略 + 结果缓存。
type reactRuntime struct {
	factory      reactFactory
	defaultReact bool
	cache        *reactCache
}

const (
	// 短句门槛：少于 6 个字符（rune）单步，如「哈哈」「好」「在吗」。
	minReactRunes = 6
	// 同问题结果复用窗口，控制重复弹幕的模型成本。
	reactCacheTTL = 5 * time.Minute
	// 缓存条目上限；弹幕量大时不至于无界增长。
	reactCacheMaxEntries = 1024
)

// shouldUseReAct 判断这条弹幕要不要走多步思考。
//
// default_react=true（默认）：只有短句保持单步，其余全走 ReAct，
// 不再依赖「梗/刚才」这类词表——词表漏判就查不到记忆，可用性优先。
// default_react=false：保留旧两段式，仅命中召回线索时走 ReAct。
//
// 短句判断直接用 rune 长度，不复用 reasoning.Decide：
// Decide 的职责是「是否需要召回」，不是「句子长短」；拿它当长度判断会改语义，也会漏掉短句。
func shouldUseReAct(text string, defaultReact bool) bool {
	if defaultReact {
		return utf8.RuneCountInString(strings.TrimSpace(text)) >= minReactRunes
	}
	d := reasoning.Decide(text)
	return d.NeedMemory || d.NeedKnowledge
}

// reactCacheEntry 保存一次 ReAct 回复及其过期时间。
type reactCacheEntry struct {
	text      string
	expiresAt time.Time
}

// reactCache 按「查询文本 + 会话 Scope」复用 ReAct 结果。
//
// 带 Scope 是隐私底线：私信里的回答不能因为问题相同就复用给公开弹幕。
type reactCache struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	entries map[[32]byte]reactCacheEntry
}

// newReactCache 造一个结果缓存；now 可注入（测试时钟）。
func newReactCache(now func() time.Time) *reactCache {
	if now == nil {
		now = time.Now
	}
	return &reactCache{now: now, ttl: reactCacheTTL, entries: make(map[[32]byte]reactCacheEntry)}
}

// reactCacheKey 把 Scope 与查询文本压成 SHA-256。
func reactCacheKey(scope memory.Scope, text string) [32]byte {
	h := sha256.New()
	h.Write([]byte(scope.Channel))
	h.Write([]byte{0})
	h.Write([]byte(scope.Session))
	h.Write([]byte{0})
	if scope.Private {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	h.Write([]byte(strings.TrimSpace(text)))
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

// Get 返回未过期的缓存答案；过期条目顺手清掉。
func (c *reactCache) Get(scope memory.Scope, text string) (string, bool) {
	if c == nil {
		return "", false
	}
	key := reactCacheKey(scope, text)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if !c.now().Before(entry.expiresAt) {
		delete(c.entries, key)
		return "", false
	}
	return entry.text, true
}

// Put 只缓存非空答案；超过容量先清过期条目，仍满则放弃写入。
func (c *reactCache) Put(scope memory.Scope, text, reply string) {
	if c == nil || strings.TrimSpace(reply) == "" {
		return
	}
	now := c.now()
	key := reactCacheKey(scope, text)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= reactCacheMaxEntries {
		for k, entry := range c.entries {
			if !now.Before(entry.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= reactCacheMaxEntries {
			return
		}
	}
	c.entries[key] = reactCacheEntry{text: reply, expiresAt: now.Add(c.ttl)}
}

// runReact 执行一次 ReAct 并记录预算相关日志（步数/工具/降级/耗时）。
// 命中缓存时不再造 Agent，也不再消耗模型调用。
func runReact(ctx context.Context, runtime *reactRuntime, scope memory.Scope, pack persona.Pack, ev event.Event, log *zap.Logger) (string, error) {
	if runtime == nil || runtime.factory == nil {
		return "", nil
	}
	if text, ok := runtime.cache.Get(scope, ev.Text); ok {
		log.Info("ReAct 结果缓存命中",
			zap.String("action", "react_cache"),
			zap.String("event_id", ev.ID),
		)
		return text, nil
	}
	agent, err := runtime.factory(scope)
	if err != nil || agent == nil {
		return "", err
	}
	started := time.Now()
	res, err := agent.Run(ctx, reasoning.Input{System: pack.SystemPrompt(), Question: ev.Text})
	elapsed := time.Since(started).Milliseconds()
	log.Info("ReAct 执行完成",
		zap.String("action", "react"),
		zap.String("event_id", ev.ID),
		zap.Int("steps", res.Steps),
		zap.Strings("tools_used", res.Tools),
		zap.Bool("degraded", res.Degraded),
		zap.String("degrade_reason", res.Reason),
		zap.Int64("latency_ms", elapsed),
	)
	if err != nil {
		return "", err
	}
	if text := strings.TrimSpace(res.Text); text != "" {
		runtime.cache.Put(scope, ev.Text, text)
	}
	return res.Text, nil
}

// buildReasonerFactory 装配 ReAct：模型走主路由，工具走记忆/知识/状态/历史。
//
// viewerSearch 为 nil 时不注册 viewer_search；其余工具照旧。
// giftGate 是可选的动态送礼开关（面板热更新），nil 时用配置值。
// 返回 nil 表示不启用（配置关闭、没模型、或一个工具都装不起来）——此时 serve 仍走单步。
func buildReasonerFactory(cfg config.Config, mem memory.Reader, know knowledge.Reader, pres *presence.Table, events *database.EventRepo, viewerSearch reasoning.ViewerProfileSearcher, quota reasoning.QuotaSource, exec toolExecutors, usage store.UsageRepo, giftGate func() bool, log *zap.Logger) (reactFactory, error) {
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

	return func(scope memory.Scope) (*reasoning.Agent, error) {
		recall, err := reasoning.BuildTools(reasoning.ToolDeps{
			Memory:    mem,
			Knowledge: know,
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
			Now:          time.Now,
		})
		if err != nil {
			return nil, err
		}
		boxes := []*reasoning.Toolbox{recall, readonly}
		// 副作用 5 个：默认关；关了就不注册，模型连选项都看不见
		if cfg.Reasoning.AllowSideEffectTools {
			side, err := reasoning.BuildSideEffectTools(sideEffectDeps(cfg, quota, exec, scope.Session, giftGate, log))
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
	if u.InputTokens == nil || u.OutputTokens == nil || p.blueprint.inUSD == nil || p.blueprint.outUSD == nil {
		return nil
	}
	v := math.Round((float64(*u.InputTokens)**p.blueprint.inUSD + float64(*u.OutputTokens)**p.blueprint.outUSD)) / 1e6
	return &v
}

// validTokens 把负数/越界 token 当作未知（nil），不让脏数据进账本。
func validTokens(n *int) *int {
	if n == nil || *n < 0 || int64(*n) > 2147483647 {
		return nil
	}
	return n
}
