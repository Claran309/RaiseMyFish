package main

// 本文件实现面板的运行控制面：采集器、看直播、主动弹幕、热重载与梗白学习的
// 运行时启停，以及送礼/记忆冻结/看门狗等状态查询。
//
// 边界：
//   - 只改运行时开关，不改调度/发送语义；
//   - 采集器实例启动后不可复活（collector.started CAS），重启一律新建实例；
//   - 所有错误文案不含凭据，状态只给白名单字段。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/watchdog"
	"RaiseMyFish/internal/web"
	"go.uber.org/zap"
)

// runtimeDeps 是控制面装配依赖；Initial* 是启动时已建好的采集器实例。
type runtimeDeps struct {
	Bus         event.Bus
	Resources   *infra.Resources
	Routes      map[string]brain.Brain
	Pack        persona.Pack
	Knowledge   knowledgeAssembly
	Presence    *presence.Table
	Timeout     time.Duration
	DMWhitelist dmWhitelistSetter
	GiftGate    *atomic.Bool
	Now         func() time.Time
	Live        collector.Collector
	Comment     collector.Collector
	DM          collector.Collector
}

// runtimeControl 实现 web.RuntimeController。
type runtimeControl struct {
	ctx   context.Context
	base  config.Config
	deps  runtimeDeps
	log   *zap.Logger
	now   func() time.Time
	quota reasoning.QuotaSource

	live      *collectorController
	comment   *collectorController
	dm        *collectorController
	watch     *watchController
	proactive *proactiveController
	hotReload *backgroundController
	memeLearn *backgroundController

	opMu sync.Mutex
	mu   sync.Mutex

	commentVideos []string
	allowedMIDs   []string
}

// newRuntimeControl 装配控制面；bus 是采集事件落库的唯一出口。
func newRuntimeControl(ctx context.Context, cfg config.Config, deps runtimeDeps, log *zap.Logger) (*runtimeControl, error) {
	if ctx == nil || deps.Bus == nil || log == nil {
		return nil, errors.New("运行时控制面缺少必要依赖")
	}
	if deps.Timeout <= 0 {
		deps.Timeout = 5 * time.Second
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	if deps.GiftGate == nil {
		deps.GiftGate = &atomic.Bool{}
	}
	deps.GiftGate.Store(cfg.Gift.Enabled)

	c := &runtimeControl{
		ctx:           ctx,
		base:          cfg,
		deps:          deps,
		log:           log,
		now:           now,
		quota:         buildGiftQuota(cfg, log),
		commentVideos: append([]string{}, cfg.Collector.Comment.Videos...),
		allowedMIDs:   initialAllowedMIDs(cfg),
	}

	c.live = newCollectorController("live", ctx, deps.Bus, deps.Timeout, log, now, deps.Live,
		func() (collector.Collector, error) { return newLiveCollector(cfg, log) })
	c.comment = newCollectorController("comment", ctx, deps.Bus, deps.Timeout, log, now, deps.Comment,
		func() (collector.Collector, error) {
			c.mu.Lock()
			videos := append([]string{}, c.commentVideos...)
			c.mu.Unlock()
			return newCommentCollector(cfg, videos, log)
		})
	c.dm = newCollectorController("dm", ctx, deps.Bus, deps.Timeout, log, now, deps.DM,
		func() (collector.Collector, error) { return newDMCollector(cfg, log) })
	c.watch = &watchController{ctx: ctx, base: cfg, log: log, now: now, available: watchAvailable(cfg)}
	c.proactive = &proactiveController{
		ctx:       ctx,
		base:      cfg,
		deps:      deps,
		log:       log,
		now:       now,
		available: proactiveAvailable(cfg, deps),
		interval:  cfg.Proactive.IntervalSeconds,
	}
	c.hotReload = newBackgroundController("knowledge_reload", ctx, log, hotReloadAvailable(cfg, deps),
		func(parent context.Context) (func(), error) {
			if deps.Knowledge.Base == nil || strings.TrimSpace(cfg.Knowledge.Dir) == "" {
				return nil, errors.New("知识热重载未装配")
			}
			stop := startKnowledgeHotReload(parent, deps.Knowledge.Base, cfg.Knowledge, log)
			log.Info("知识热重载已启动", zap.String("action", "knowledge_reload"))
			return stop, nil
		})
	c.memeLearn = newBackgroundController("meme_learn", ctx, log, memeLearnAvailable(cfg, deps),
		func(parent context.Context) (func(), error) {
			if deps.Resources == nil || deps.Resources.DB == nil {
				return nil, errors.New("梗白学习缺少数据库资源")
			}
			learner, model, err := newMemeLearner(cfg, deps.Resources.DB, deps.Knowledge, log)
			if err != nil {
				return nil, err
			}
			log.Info("梗白学习已启动",
				zap.String("action", memeLearnAction),
				zap.String("model", model),
				zap.Int("hot_limit", cfg.Knowledge.MemeLearnHotLimit),
				zap.Int("max_per_day", cfg.Knowledge.MemeLearnMaxPerDay),
			)
			return startMemeLearnLoop(parent, learner, log), nil
		})
	return c, nil
}

// StartInitial 按静态配置启动初始能力；核心采集失败上抛，其余只降级。
func (c *runtimeControl) StartInitial() error {
	if c.base.Channels.Live.Enabled {
		if err := c.SetLiveCollect(true); err != nil {
			return fmt.Errorf("启动直播采集失败：%w", err)
		}
	}
	if c.base.Channels.Comment.Enabled && c.base.Collector.Comment.Enabled {
		if err := c.SetComment(true, c.base.Collector.Comment.Videos); err != nil {
			return fmt.Errorf("启动评论采集失败：%w", err)
		}
	}
	if c.base.Collector.DM.Enabled {
		if err := c.SetDM(true, nil); err != nil {
			c.log.Warn("私信采集未启动，降级运行", zap.String("action", "dm_setup"), zap.Error(err))
		}
	}
	if c.base.Watch.Enabled {
		if !c.base.Channels.Live.Enabled {
			c.log.Warn("看直播未启动：直播采集未启用", zap.String("action", "watch"))
		} else if err := c.SetWatch(true, c.base.Collector.Live.RoomID); err != nil {
			c.log.Error("看直播装配失败，降级运行", zap.String("action", "watch"), zap.Error(err))
		}
	}
	if c.base.Proactive.Enabled {
		if err := c.SetProactive(true, c.base.Proactive.IntervalSeconds); err != nil {
			c.log.Error("主动弹幕装配失败，降级运行", zap.String("action", "proactive"), zap.Error(err))
		}
	}
	if c.base.Knowledge.HotReload && c.hotReload.available {
		if err := c.SetHotReload(true); err != nil {
			c.log.Warn("知识热重载未启动", zap.String("action", "knowledge_reload"), zap.Error(err))
		}
	}
	if c.base.Knowledge.MemeLearn {
		switch {
		case !c.base.LLM.Enabled || len(c.base.LLM.Routing) == 0:
			c.log.Warn("梗白学习已配置但 LLM 未启用，跳过", zap.String("action", memeLearnAction))
		case c.deps.Knowledge.Reader == nil:
			c.log.Warn("梗白学习已配置但知识Reader未装配，跳过", zap.String("action", memeLearnAction))
		default:
			if err := c.SetMemeLearn(true); err != nil {
				c.log.Error("梗白学习装配失败，降级运行", zap.String("action", memeLearnAction), zap.Error(err))
			}
		}
	}
	return nil
}

// Close 停止所有后台循环；可重复调用。
// 不持有 opMu：各控制器自带的 lifeMu 已保证启停串行，持锁再调用会自锁。
func (c *runtimeControl) Close() {
	_ = c.watch.set(false, "")
	_ = c.proactive.set(false, 0)
	_ = c.hotReload.set(false)
	_ = c.memeLearn.set(false)
	_ = c.comment.set(false)
	_ = c.dm.set(false)
	_ = c.live.set(false)
}

// Status 汇总面板所需的全部运行时状态。
func (c *runtimeControl) Status() web.RuntimeStatus {
	c.mu.Lock()
	videos := append([]string{}, c.commentVideos...)
	allowed := append([]string{}, c.allowedMIDs...)
	c.mu.Unlock()

	comment := web.CommentState{CollectorState: c.comment.status(), Videos: videos}
	dm := c.dm.status()
	dmState := web.DMState{
		CollectorState:     dm,
		AllowedMIDs:        allowed,
		WhitelistAvailable: c.deps.DMWhitelist != nil,
	}
	return web.RuntimeStatus{
		Live:        c.live.status(),
		Comment:     comment,
		DM:          dmState,
		Watch:       c.watch.status(),
		Proactive:   c.proactive.status(),
		Gift:        c.giftStatus(),
		Memory:      c.memoryStatus(),
		HotReload:   c.hotReload.status(),
		MemeLearn:   c.memeLearn.status(),
		Watchdog:    c.watchdogStatus(),
		GeneratedAt: c.now().UTC(),
	}
}

// SetLiveCollect 启停直播采集。
func (c *runtimeControl) SetLiveCollect(enabled bool) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.live.set(enabled)
}

// SetComment 启停评论采集；videos 为空表示沿用当前列表。
func (c *runtimeControl) SetComment(enabled bool, videos []string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	previous := append([]string{}, c.commentVideos...)
	c.mu.Unlock()

	target := previous
	if len(videos) > 0 {
		normalized, err := normalizeVideos(videos)
		if err != nil {
			return err
		}
		target = normalized
	}
	if enabled && len(target) == 0 {
		return errors.New("评论监听需要至少一个视频 oid")
	}
	changed := !equalStrings(previous, target)
	if changed {
		c.mu.Lock()
		c.commentVideos = target
		c.mu.Unlock()
		// 采集器不能复活，改视频列表必须先停再换新实例。
		if c.comment.isRunning() {
			if err := c.comment.set(false); err != nil {
				c.restoreCommentVideos(previous)
				return err
			}
		}
	}
	if err := c.comment.set(enabled); err != nil {
		if changed {
			c.restoreCommentVideos(previous)
		}
		return err
	}
	if changed {
		c.log.Info("评论监听视频列表已更新",
			zap.String("action", "web_comment_videos"),
			zap.Int("videos", len(target)),
			zap.Bool("enabled", enabled),
		)
	}
	return nil
}

func (c *runtimeControl) restoreCommentVideos(videos []string) {
	c.mu.Lock()
	c.commentVideos = videos
	c.mu.Unlock()
}

// SetDM 启停私信采集；allowed 为 nil 表示不改白名单。
func (c *runtimeControl) SetDM(enabled bool, allowed []string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	if allowed != nil {
		if c.deps.DMWhitelist == nil {
			return errors.New("私信发送未装配，无法修改白名单")
		}
		normalized, err := normalizeMIDs(allowed)
		if err != nil {
			return err
		}
		merged := appendUnique(normalized, ownerMID(c.base))
		if err := c.deps.DMWhitelist.SetAllowedMIDs(merged); err != nil {
			return err
		}
		c.mu.Lock()
		c.allowedMIDs = merged
		c.mu.Unlock()
		c.log.Info("私信白名单已更新",
			zap.String("action", "web_dm_whitelist"),
			zap.Int("allowed", len(merged)),
		)
	}
	return c.dm.set(enabled)
}

// SetWatch 启停看直播；roomID 为空时沿用上次房间。
func (c *runtimeControl) SetWatch(enabled bool, roomID string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.watch.set(enabled, roomID)
}

// SetProactive 启停主动弹幕；intervalSeconds 为 0 表示沿用当前间隔。
func (c *runtimeControl) SetProactive(enabled bool, intervalSeconds int) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.proactive.set(enabled, intervalSeconds)
}

// SetGift 热更新送礼开关（ReAct 副作用工具下一次调用即生效）。
func (c *runtimeControl) SetGift(enabled bool) error {
	c.deps.GiftGate.Store(enabled)
	c.log.Info("送礼开关已更新", zap.String("action", "web_gift_toggle"), zap.Bool("enabled", enabled))
	return nil
}

// SetMemoryFrozen 通过状态文件冻结/解冻记忆提炼。
func (c *runtimeControl) SetMemoryFrozen(frozen bool) error {
	var err error
	if frozen {
		err = memory.Freeze(memoryFreezePath)
	} else {
		err = memory.Unfreeze(memoryFreezePath)
	}
	if err != nil {
		return err
	}
	c.log.Info("记忆冻结状态已更新", zap.String("action", "web_memory_freeze"), zap.Bool("frozen", frozen))
	return nil
}

// SetHotReload 启停知识目录热重载。
func (c *runtimeControl) SetHotReload(enabled bool) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.hotReload.set(enabled)
}

// SetMemeLearn 启停梗白学习。
func (c *runtimeControl) SetMemeLearn(enabled bool) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.memeLearn.set(enabled)
}

// Watchdog 返回看门狗配置、心跳与重启历史。
func (c *runtimeControl) Watchdog() web.WatchdogStatus {
	return c.watchdogStatus()
}

// giftStatus 读取送礼开关与当日额度；账本故障只降级为错误文案。
func (c *runtimeControl) giftStatus() web.GiftState {
	state := web.GiftState{
		Enabled:    c.deps.GiftGate.Load(),
		Real:       c.base.Gift.Real,
		GiftID:     c.base.Gift.GiftID,
		DailyLimit: c.base.Gift.DailyLimit,
		MaxNum:     c.base.Gift.MaxNum,
		Day:        c.now().Format("2006-01-02"),
	}
	if c.quota == nil {
		state.Error = "送礼额度账本未装配"
		return state
	}
	used, err := c.quota.Used(state.Day)
	if err != nil {
		state.Error = "读取送礼额度失败"
		return state
	}
	state.Used = used
	return state
}

func (c *runtimeControl) memoryStatus() web.MemoryState {
	frozen, err := memory.IsFrozen(memoryFreezePath)
	if err != nil {
		return web.MemoryState{Error: "读取冻结状态失败"}
	}
	return web.MemoryState{Frozen: frozen}
}

func (c *runtimeControl) watchdogStatus() web.WatchdogStatus {
	cfg := c.base.Watchdog
	state := web.WatchdogStatus{
		Enabled:                  cfg.Enabled,
		StaleSeconds:             cfg.StaleSeconds,
		UnhealthyThreshold:       cfg.UnhealthyThreshold,
		CooldownSeconds:          cfg.CooldownSeconds,
		RestartCommandConfigured: strings.TrimSpace(cfg.RestartCommand) != "",
		Restarts:                 []watchdog.RestartRecord{},
	}
	heartbeat, err := readHeartbeat(cfg.HeartbeatPath)
	if err != nil {
		state.Error = "读取心跳文件失败"
	}
	if heartbeat != nil {
		state.Heartbeat.Exists = true
		state.Heartbeat.At = formatTime(heartbeat.At)
		state.Heartbeat.PID = heartbeat.PID
		age := c.now().Sub(heartbeat.At)
		if age < 0 {
			age = 0
		}
		state.Heartbeat.AgeSeconds = int64(age.Seconds())
		if cfg.StaleSeconds > 0 {
			state.Heartbeat.Stale = state.Heartbeat.AgeSeconds > int64(cfg.StaleSeconds)
		}
	}
	records, err := watchdog.ReadHistory(cfg.HistoryPath, 20)
	if err != nil {
		state.Error = "读取重启历史失败"
	}
	if len(records) > 0 {
		state.Restarts = records
	}
	return state
}

// ---------------------------------------------------------------------------
// 采集器控制

type collectorController struct {
	name    string
	ctx     context.Context
	bus     event.Bus
	timeout time.Duration
	log     *zap.Logger
	now     func() time.Time
	factory func() (collector.Collector, error)

	lifeMu sync.Mutex
	mu     sync.Mutex

	initial   collector.Collector
	inst      collector.Collector
	enabled   bool
	running   bool
	startedAt time.Time
	lastError string
	cancel    context.CancelFunc
	done      chan struct{}
}

func newCollectorController(name string, ctx context.Context, bus event.Bus, timeout time.Duration, log *zap.Logger, now func() time.Time, initial collector.Collector, factory func() (collector.Collector, error)) *collectorController {
	return &collectorController{
		name: name, ctx: ctx, bus: bus, timeout: timeout, log: log, now: now,
		initial: initial, factory: factory,
	}
}

func (c *collectorController) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// set 启停采集器；重复调用幂等。
func (c *collectorController) set(enabled bool) error {
	c.lifeMu.Lock()
	defer c.lifeMu.Unlock()
	if enabled {
		if c.isRunning() {
			c.mu.Lock()
			c.enabled = true
			c.mu.Unlock()
			return nil
		}
		c.mu.Lock()
		inst := c.initial
		c.initial = nil
		c.mu.Unlock()
		if inst == nil {
			if c.factory == nil {
				return fmt.Errorf("%s 采集未装配", c.name)
			}
			built, err := c.factory()
			if err != nil {
				c.fail(err)
				return err
			}
			inst = built
		}
		c.start(inst)
		return nil
	}
	c.mu.Lock()
	c.enabled = false
	cancel := c.cancel
	done := c.done
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}

func (c *collectorController) start(inst collector.Collector) {
	runCtx, cancel := context.WithCancel(c.ctx)
	done := make(chan struct{})
	c.mu.Lock()
	c.inst = inst
	c.enabled = true
	c.running = true
	c.startedAt = c.now()
	c.lastError = ""
	c.cancel = cancel
	c.done = done
	c.mu.Unlock()
	go c.run(runCtx, inst, done)
	c.log.Info("采集器已启动",
		zap.String("action", "collector_start"),
		zap.String("channel", c.name),
	)
}

// run 转发采集事件；意外退出只记状态，不拖垮整个进程。
func (c *collectorController) run(ctx context.Context, inst collector.Collector, done chan struct{}) {
	defer close(done)
	err := collector.RunToBus(ctx, inst, c.bus, c.timeout, c.log)
	stopped := ctx.Err() != nil
	c.mu.Lock()
	c.running = false
	c.inst = nil
	c.cancel = nil
	if c.done == done {
		c.done = nil
	}
	if err != nil && !stopped {
		c.lastError = err.Error()
	}
	c.mu.Unlock()
	if err != nil && !stopped {
		c.log.Warn("采集器已暂停，需处理后重启",
			zap.String("action", "collector_paused"),
			zap.String("channel", c.name),
			zap.Error(err),
		)
	}
}

func (c *collectorController) fail(err error) {
	c.mu.Lock()
	c.enabled = false
	c.lastError = err.Error()
	c.mu.Unlock()
}

func (c *collectorController) status() web.CollectorState {
	c.mu.Lock()
	state := web.CollectorState{
		Available: true,
		Enabled:   c.enabled,
		Running:   c.running,
		LastError: c.lastError,
	}
	inst := c.inst
	started := c.startedAt
	c.mu.Unlock()
	if state.Running && !started.IsZero() {
		state.StartedAt = formatTime(started)
	}
	if inst != nil {
		health := inst.Health()
		if !health.LastEventAt.IsZero() {
			state.LastEventAt = formatTime(health.LastEventAt)
		}
		state.Reconnects = health.Reconnects
		if state.LastError == "" && state.Running && health.LastError != "" {
			state.LastError = health.LastError
		}
	}
	return state
}

// ---------------------------------------------------------------------------
// 看直播控制

type watchController struct {
	ctx       context.Context
	base      config.Config
	log       *zap.Logger
	now       func() time.Time
	available bool

	lifeMu sync.Mutex
	mu     sync.Mutex

	enabled   bool
	running   bool
	room      string
	startedAt time.Time
	lastError string
	stop      func()
}

func (w *watchController) set(enabled bool, roomID string) error {
	w.lifeMu.Lock()
	defer w.lifeMu.Unlock()
	if !enabled {
		w.mu.Lock()
		w.enabled = false
		stop := w.stop
		w.stop = nil
		w.running = false
		w.mu.Unlock()
		if stop != nil {
			stop()
		}
		return nil
	}

	room, err := w.resolveRoom(roomID)
	if err != nil {
		w.setError(err)
		return err
	}
	local := w.base
	local.Collector.Live.RoomID = room
	pipeline, err := buildWatchPipeline(local, w.log)
	if err != nil {
		w.setError(err)
		return err
	}

	w.mu.Lock()
	running := w.running
	stop := w.stop
	current := w.room
	w.mu.Unlock()
	if running && current == room {
		w.mu.Lock()
		w.enabled = true
		w.lastError = ""
		w.mu.Unlock()
		return nil
	}
	if stop != nil {
		stop()
	}
	interval := time.Duration(local.Watch.IntervalSeconds) * time.Second
	stop = startWatchLoop(w.ctx, pipeline, interval, w.log)
	w.mu.Lock()
	w.enabled = true
	w.running = true
	w.room = room
	w.startedAt = w.now()
	w.lastError = ""
	w.stop = stop
	w.mu.Unlock()
	w.log.Info("看直播已启动",
		zap.String("action", "watch"),
		zap.String("room", room),
		zap.Int("interval_seconds", local.Watch.IntervalSeconds),
	)
	return nil
}

// resolveRoom 优先用请求里的房间，其次沿用上次，最后回落采集配置。
func (w *watchController) resolveRoom(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		w.mu.Lock()
		raw = w.room
		w.mu.Unlock()
	}
	if strings.TrimSpace(raw) == "" {
		raw = w.base.Collector.Live.RoomID
	}
	return parseRoomID(raw)
}

func (w *watchController) setError(err error) {
	w.mu.Lock()
	w.enabled = false
	w.lastError = err.Error()
	w.mu.Unlock()
}

func (w *watchController) status() web.WatchState {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := web.WatchState{
		Available: w.available,
		Enabled:   w.enabled,
		Running:   w.running,
		RoomID:    w.room,
		LastError: w.lastError,
	}
	if w.running && !w.startedAt.IsZero() {
		state.StartedAt = formatTime(w.startedAt)
	}
	return state
}

// ---------------------------------------------------------------------------
// 主动弹幕控制

type proactiveController struct {
	ctx       context.Context
	base      config.Config
	deps      runtimeDeps
	log       *zap.Logger
	now       func() time.Time
	available bool

	lifeMu sync.Mutex
	mu     sync.Mutex

	runner    proactiveRunner
	enabled   bool
	running   bool
	interval  int
	startedAt time.Time
	lastError string
	stop      func()
}

func (p *proactiveController) set(enabled bool, intervalSeconds int) error {
	p.lifeMu.Lock()
	defer p.lifeMu.Unlock()

	p.mu.Lock()
	current := p.interval
	p.mu.Unlock()
	if intervalSeconds <= 0 {
		intervalSeconds = current
	} else if intervalSeconds < 5 || intervalSeconds > 3600 {
		return errors.New("主动弹幕间隔必须在5至3600秒之间")
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 20
	}

	if !enabled {
		p.mu.Lock()
		p.enabled = false
		stop := p.stop
		p.stop = nil
		p.running = false
		p.mu.Unlock()
		if stop != nil {
			stop()
		}
		return nil
	}

	p.mu.Lock()
	runner := p.runner
	p.mu.Unlock()
	if runner == nil {
		built, err := buildProactiveRunner(p.base, p.deps.Resources, p.deps.Routes, p.deps.Pack, p.deps.Knowledge.Reader, p.deps.Presence, p.deps.GiftGate.Load, p.log)
		if err != nil {
			p.setError(err)
			return err
		}
		runner = built
		p.mu.Lock()
		p.runner = runner
		p.mu.Unlock()
	}

	p.mu.Lock()
	running := p.running
	stop := p.stop
	sameInterval := p.interval == intervalSeconds
	p.mu.Unlock()
	if running && sameInterval {
		p.mu.Lock()
		p.enabled = true
		p.lastError = ""
		p.mu.Unlock()
		return nil
	}
	if stop != nil {
		stop()
	}
	loop := startProactiveLoop(p.ctx, runner, time.Duration(intervalSeconds)*time.Second, p.log)
	p.mu.Lock()
	p.enabled = true
	p.running = true
	p.interval = intervalSeconds
	p.startedAt = p.now()
	p.lastError = ""
	p.stop = loop
	p.mu.Unlock()
	p.log.Info("主动弹幕已启动",
		zap.String("action", "proactive"),
		zap.Int("interval_seconds", intervalSeconds),
	)
	return nil
}

func (p *proactiveController) setError(err error) {
	p.mu.Lock()
	p.enabled = false
	p.lastError = err.Error()
	p.mu.Unlock()
}

func (p *proactiveController) status() web.ProactiveState {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := web.ProactiveState{
		FeatureState: web.FeatureState{
			Available: p.available,
			Enabled:   p.enabled,
			Running:   p.running,
			LastError: p.lastError,
		},
		IntervalSeconds: p.interval,
	}
	return state
}

// ---------------------------------------------------------------------------
// 通用后台循环控制（热重载 / 梗白学习）

type backgroundController struct {
	name      string
	ctx       context.Context
	log       *zap.Logger
	available bool
	start     func(context.Context) (func(), error)

	lifeMu sync.Mutex
	mu     sync.Mutex

	enabled   bool
	running   bool
	lastError string
	stop      func()
}

func newBackgroundController(name string, ctx context.Context, log *zap.Logger, available bool, start func(context.Context) (func(), error)) *backgroundController {
	return &backgroundController{name: name, ctx: ctx, log: log, available: available, start: start}
}

func (b *backgroundController) set(enabled bool) error {
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	if enabled {
		b.mu.Lock()
		running := b.running
		b.mu.Unlock()
		if running {
			b.mu.Lock()
			b.enabled = true
			b.mu.Unlock()
			return nil
		}
		stop, err := b.start(b.ctx)
		if err != nil {
			b.mu.Lock()
			b.enabled = false
			b.lastError = err.Error()
			b.mu.Unlock()
			return err
		}
		b.mu.Lock()
		b.stop = stop
		b.running = true
		b.enabled = true
		b.lastError = ""
		b.mu.Unlock()
		return nil
	}
	b.mu.Lock()
	b.enabled = false
	stop := b.stop
	b.stop = nil
	b.running = false
	b.mu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

func (b *backgroundController) status() web.FeatureState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return web.FeatureState{
		Available: b.available,
		Enabled:   b.enabled,
		Running:   b.running,
		LastError: b.lastError,
	}
}

// ---------------------------------------------------------------------------
// 工具函数

var (
	roomPattern  = regexp.MustCompile(`^[0-9]{1,20}$`)
	videoPattern = regexp.MustCompile(`^[0-9]{1,128}$`)
	midPattern   = regexp.MustCompile(`^[0-9]{1,32}$`)
)

// parseRoomID 接受纯数字 room_id 或 live.bilibili.com 直播间链接。
func parseRoomID(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("缺少直播间 room_id")
	}
	if roomPattern.MatchString(value) {
		room := strings.TrimLeft(value, "0")
		if room == "" {
			return "", errors.New("room_id 不能为 0")
		}
		return room, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("直播间必须是纯数字 room_id 或 live.bilibili.com 链接")
	}
	if parsed.Host == "" {
		parsed, err = url.Parse("https://" + value)
		if err != nil {
			return "", errors.New("直播间必须是纯数字 room_id 或 live.bilibili.com 链接")
		}
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "live.bilibili.com" && !strings.HasSuffix(host, ".live.bilibili.com") {
		return "", errors.New("只支持 live.bilibili.com 直播间链接")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		if !roomPattern.MatchString(segments[i]) {
			continue
		}
		room := strings.TrimLeft(segments[i], "0")
		if room == "" {
			return "", errors.New("room_id 不能为 0")
		}
		return room, nil
	}
	return "", errors.New("链接里找不到直播间 room_id")
}

// normalizeVideos 与配置层同一口径：纯数字、去前导零、去重。
func normalizeVideos(raw []string) ([]string, error) {
	videos := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		value := strings.TrimSpace(item)
		if !videoPattern.MatchString(value) {
			return nil, errors.New("视频 oid 必须是纯数字")
		}
		canonical := strings.TrimLeft(value, "0")
		if canonical == "" {
			return nil, errors.New("视频 oid 不能为 0")
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		videos = append(videos, canonical)
	}
	if len(videos) == 0 {
		return nil, errors.New("视频 oid 列表不能为空")
	}
	return videos, nil
}

// normalizeMIDs 校验白名单 mid；重复项去重，空列表拒绝。
func normalizeMIDs(raw []string) ([]string, error) {
	mids := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		mid := strings.TrimSpace(item)
		if mid == "" {
			continue
		}
		if !midPattern.MatchString(mid) {
			return nil, errors.New("私信白名单 mid 必须是纯数字")
		}
		if _, duplicate := seen[mid]; duplicate {
			continue
		}
		seen[mid] = struct{}{}
		mids = append(mids, mid)
	}
	if len(mids) == 0 {
		return nil, errors.New("私信白名单不能为空")
	}
	return mids, nil
}

// appendUnique 追加不重复的 mid（主人始终保留在白名单里）。
func appendUnique(mids []string, extra string) []string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return mids
	}
	for _, mid := range mids {
		if mid == extra {
			return mids
		}
	}
	return append(mids, extra)
}

func initialAllowedMIDs(cfg config.Config) []string {
	var configured []string
	if cfg.Sender != nil {
		configured = cfg.Sender.DM.AllowedMIDs
	}
	allowed := append([]string{}, configured...)
	return appendUnique(allowed, ownerMID(cfg))
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func watchAvailable(cfg config.Config) bool {
	if _, _, _, err := cfg.LLM.Resolve(cfg.Watch.LLMRoute); err != nil {
		return false
	}
	return cfg.Watch.IntervalSeconds > 0
}

func proactiveAvailable(cfg config.Config, deps runtimeDeps) bool {
	if deps.Resources == nil || deps.Resources.DB == nil {
		return false
	}
	if pickProactiveBrain(deps.Routes) == nil {
		return false
	}
	_, _, _, err := cfg.LLM.Resolve(cfg.Watch.LLMRoute)
	return err == nil
}

func hotReloadAvailable(cfg config.Config, deps runtimeDeps) bool {
	return deps.Knowledge.Base != nil && strings.TrimSpace(cfg.Knowledge.Dir) != ""
}

func memeLearnAvailable(cfg config.Config, deps runtimeDeps) bool {
	if deps.Resources == nil || deps.Resources.DB == nil || deps.Knowledge.Reader == nil {
		return false
	}
	return cfg.LLM.Enabled && len(cfg.LLM.Routing) > 0
}

func readHeartbeat(path string) (*watchdog.Heartbeat, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取心跳文件：%w", err)
	}
	var heartbeat watchdog.Heartbeat
	if err := json.Unmarshal(data, &heartbeat); err != nil {
		return nil, fmt.Errorf("解析心跳文件：%w", err)
	}
	return &heartbeat, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
