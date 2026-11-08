package main

// 本文件是 0.19 的主动弹幕接线：直播采集运行期间按固定间隔旁路说一句。
//
// 与 0.17 的区别：不再走独立的「识图→直接出弹幕」简化管线，
// 而是把画面描述注入 ContextInput.Visual，复用直播回复的 Brain 管线。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/proactive"
	"RaiseMyFish/internal/reasoning"
	senderlive "RaiseMyFish/internal/sender/live"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
)

// proactiveRunner 是定时循环依赖的最小能力，proactive.Runner 直接满足。
type proactiveRunner interface {
	RunOnce(context.Context) (proactive.Result, error)
}

// buildProactiveRunner 只装配依赖，构造期不联网、不执行 ffmpeg。
// giftGate 为 nil 时回落到配置里的送礼开关；面板热更新会传入动态门控。
func buildProactiveRunner(cfg config.Config, resources *infra.Resources, routes map[string]brain.Brain, pack persona.Pack, know knowledge.Reader, pres *presence.Table, rooms *liveRoomState, giftGate func() bool, logger *zap.Logger) (*proactive.Runner, error) {
	if resources == nil || resources.DB == nil {
		return nil, errors.New("主动弹幕缺少数据库资源")
	}
	engine := pickProactiveBrain(routes)
	if engine == nil {
		return nil, errors.New("主动弹幕需要可用的 Brain 路由")
	}
	_, provider, model, err := cfg.LLM.Resolve(cfg.Watch.LLMRoute)
	if err != nil {
		return nil, err
	}
	recorder, err := watch.NewStreamRecorder(watch.StreamRecorderOptions{
		RoomID:     cfg.Collector.Live.RoomID,
		Cookie:     cfg.Watch.Cookie,
		Quality:    cfg.Watch.StreamQuality,
		FFmpegPath: cfg.Watch.FFmpegPath,
		TempDir:    cfg.Watch.TempDir,
	})
	if err != nil {
		return nil, err
	}
	// 视觉客户端沿用 watch 的实现，只把提示词换成固定的「详细识图」。
	// 用量与 Watch 同一账本口径（O08）：装配失败只降级为不计账。
	usageRepo, err := database.NewUsageRepo(resources.DB)
	if err != nil {
		logger.Warn("主动弹幕视觉记账装配失败，改为不计账", zap.String("action", "vision_usage"), zap.Error(err))
		usageRepo = nil
	}
	describer, err := watch.NewOpenAIDescriber(provider.BaseURL, provider.APIKey, model, nil, 0,
		visionSink(usageRepo, model, provider, logger))
	if err != nil {
		return nil, err
	}
	vision := proactive.VisionFromFrames(describer)

	// 记忆是可选增强：装配失败只降级为空快照，不影响主动弹幕本身
	var memReader memory.Reader
	if memRepo, err := database.NewMemoryRepo(resources.DB); err == nil {
		if reader, err := memory.NewReader(memRepo, memRepo, memory.ReaderOptions{}); err == nil {
			memReader = reader
		}
	}
	// 观众档案与被动路径同一开关；装配失败只降级为不注入
	var viewerRepo store.ViewerRepo
	if cfg.Viewer.Enabled {
		vr, err := database.NewViewerRepo(resources.DB)
		if err != nil {
			logger.Warn("主动弹幕观众档案装配失败，本轮不注入档案",
				zap.String("action", "viewer_setup"), zap.Error(err))
		} else {
			viewerRepo = vr
		}
	}
	// 统一上下文快照：被动 Brain/ReAct 与主动弹幕共用同一套资料装配。
	builder := newContextBuilder(pack, memReader, know, viewerRepo, newSoulCache(cfg.Memory.SoulPath), brainStateSource(rooms.Current, cfg.Watch), logger)

	wiring, err := resolveSenderWiring(cfg, senderlive.DefaultOptions().MinInterval)
	if err != nil {
		return nil, err
	}
	transport, err := senderlive.NewNodeTransport(wiring.Node)
	if err != nil {
		return nil, err
	}
	events, err := database.NewEventRepo(resources.DB)
	if err != nil {
		return nil, err
	}
	// ReAct 多步：与弹幕回复共用同一套装配（工具按事件 Scope 现造）。
	// use_react=false 时明确传 nil，永远单步。
	var react proactive.ReactFactory
	if cfg.Proactive.UseReact {
		usageRepo, err := database.NewUsageRepo(resources.DB)
		if err != nil {
			return nil, fmt.Errorf("装配主动弹幕用量账本失败：%w", err)
		}
		factory, err := buildReasonerFactory(cfg, memReader, know, pres, events, nil, buildGiftQuota(cfg, logger), buildToolExecutors(cfg, usageRepo, logger), usageRepo, giftGate, logger)
		if err != nil {
			return nil, err
		}
		// 主动弹幕没有「提问者」，身份查询用空 UID（「我是谁」会被明确拒绝）。
		react = proactive.ReactFactory(func(scope memory.Scope) (*reasoning.Agent, error) {
			return factory(scope, "")
		})
	}
	options := senderlive.DefaultProactiveOptions()
	options.RoomID = cfg.Collector.Live.RoomID
	options.SelfID = wiring.Service.SelfID
	psender, err := senderlive.NewProactiveSender(transport, events, options, logger)
	if err != nil {
		return nil, err
	}
	return proactive.New(recorder, vision, engine, pack, memReader, react, psender, proactive.Options{
		SendGap:      time.Duration(cfg.Proactive.SendGapSeconds) * time.Second,
		RoomID:       cfg.Collector.Live.RoomID,
		ReactTrigger: cfg.Proactive.ReactTrigger,
		Builder:      builder,
		Logger:       logger,
	})
}

// pickProactiveBrain 优先用 default 路由，没有就按名字排序取第一个。
func pickProactiveBrain(routes map[string]brain.Brain) brain.Brain {
	if engine, ok := routes["default"]; ok && engine != nil {
		return engine
	}
	keys := make([]string, 0, len(routes))
	for key, engine := range routes {
		if engine != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return routes[keys[0]]
}

// runProactive 按 ticks 逐次触发；单轮失败只记日志，不退出循环。
func runProactive(ctx context.Context, runner proactiveRunner, ticks <-chan time.Time, now func() time.Time, logger *zap.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			started := now()
			result, err := runner.RunOnce(ctx)
			elapsed := now().Sub(started)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				logger.Error("主动弹幕失败，等待下一轮",
					zap.String("action", "proactive"),
					zap.Int64("latency_ms", elapsed.Milliseconds()),
					zap.Error(err),
				)
				continue
			}
			// 跳过与成功由 Runner 自己记录；这里只留一行轮询结果便于观测。
			logger.Debug("主动弹幕本轮完成",
				zap.String("action", "proactive"),
				zap.Bool("sent", result.Sent),
				zap.String("reason", result.Reason),
				zap.Int64("latency_ms", elapsed.Milliseconds()),
			)
		}
	}
}

// startProactive 仅在 [proactive] enabled=true 时装配并启动；初始化失败只降级。
func startProactive(ctx context.Context, cfg config.Config, resources *infra.Resources, routes map[string]brain.Brain, pack persona.Pack, know knowledge.Reader, pres *presence.Table, logger *zap.Logger) func() {
	noop := func() {}
	if !cfg.Proactive.Enabled {
		return noop
	}
	runner, err := buildProactiveRunner(cfg, resources, routes, pack, know, pres, nil, nil, logger)
	if err != nil {
		logger.Error("主动弹幕装配失败，降级运行", zap.String("action", "proactive"), zap.Error(err))
		return noop
	}
	interval := time.Duration(cfg.Proactive.IntervalSeconds) * time.Second
	stop := startProactiveLoop(ctx, runner, interval, logger)
	logger.Info("主动弹幕已启动",
		zap.String("action", "proactive"),
		zap.Int("interval_seconds", cfg.Proactive.IntervalSeconds),
		zap.Int("send_gap_seconds", cfg.Proactive.SendGapSeconds),
		zap.Bool("use_react", cfg.Proactive.UseReact),
		zap.String("react_trigger", cfg.Proactive.ReactTrigger),
	)
	return stop
}

// startProactiveLoop 起定时循环；interval 非正时按 20 秒兜底。
func startProactiveLoop(ctx context.Context, runner proactiveRunner, interval time.Duration, logger *zap.Logger) func() {
	if interval <= 0 {
		interval = 20 * time.Second
	}
	ticker := time.NewTicker(interval)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		runProactive(runCtx, runner, ticker.C, time.Now, logger)
	}()
	return func() {
		cancel()
		<-done
	}
}
