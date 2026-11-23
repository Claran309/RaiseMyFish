package scheduler

import (
	"container/heap"
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/trace"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

type policyState struct {
	ChannelPolicy
	limiter *rate.Limiter
}
type cooldown struct {
	until time.Time
	timer Timer
}
type running struct {
	work    Work
	cancel  context.CancelFunc
	renewAt time.Time
	lost    bool
	receipt *event.Claim
}
type completion struct {
	job    *running
	result Result
	err    error
}

// Scheduler的可变状态仅由Run循环拥有；timer回调只发送合并唤醒。
type Scheduler struct {
	bus       event.Bus
	repo      store.SchedulerRepo
	options   Options
	workers   Workers
	clock     Clock
	log       *zap.Logger
	policies  map[event.Channel]*policyState
	pending   priorityQueue
	fairSeq   map[event.Channel]int64
	waiting   map[string]*queued
	active    map[string]*running
	sessions  map[sessionKey]string
	cooldowns map[sessionKey]cooldown
	repeats   map[repeatKey]time.Time
	wake      chan struct{}
	done      chan completion
	started   atomic.Bool
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.Func, reflect.Interface, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// New构造单次Run实例；新模块不改变已有总线/Scope契约。
func New(bus event.Bus, repo store.SchedulerRepo, o Options, workers Workers, log *zap.Logger) (*Scheduler, error) {
	if nilInterface(bus) || nilInterface(repo) || log == nil || workers.Generate == nil || workers.Verify == nil || o.MaxConcurrent < 1 || o.MaxConcurrent > 100 || o.MaxPending < o.MaxConcurrent || o.MaxPending > 10000 || o.ClaimBatch < 1 || o.ClaimBatch > 100 || o.PollInterval <= 0 || o.RetryDelay <= 0 || o.OperationTimeout <= 0 || o.WorkerTimeout <= 0 || len(o.Channels) == 0 {
		return nil, store.ErrInvalid
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	if nilInterface(o.Clock) {
		o.Clock = systemClock{}
	}
	policies := make(map[event.Channel]*policyState, len(o.Channels))
	for ch, p := range o.Channels {
		if !ch.Valid() || strings.TrimSpace(p.Brain) == "" || math.IsNaN(p.RatePerMinute) || math.IsInf(p.RatePerMinute, 0) || p.RatePerMinute <= 0 || p.Burst < 1 || p.DailyQuota < 1 || p.Cooldown < 0 || p.MinRunes < 0 || p.RepeatWindow < 0 || p.MaxConcurrent < 0 || p.MaxConcurrent > 32 || p.MergeWindow < 0 || p.MergeWindow > time.Minute {
			return nil, store.ErrInvalid
		}
		// routing_mode 只属于 live；未知取值直接拒绝，避免配错静默变成「全部回复」。
		if p.RoutingMode != "" && p.RoutingMode != RoutingMentionOnly && p.RoutingMode != RoutingFilter && p.RoutingMode != RoutingAll {
			return nil, store.ErrInvalid
		}
		if ch != event.ChLive && p.RoutingMode != "" {
			return nil, store.ErrInvalid
		}
		if p.MinRunes == 0 {
			p.MinRunes = 2
		}
		p.SelfNames = append([]string(nil), p.SelfNames...)
		p.OwnerIDs = append([]string(nil), p.OwnerIDs...)
		p.SelfIDs = append([]string(nil), p.SelfIDs...)
		p.BotIDs = append([]string(nil), p.BotIDs...)
		p.Names = append([]string(nil), p.Names...)
		p.Ads = append([]string(nil), p.Ads...)
		p.Whitelist = append([]string(nil), p.Whitelist...)
		policies[ch] = &policyState{ChannelPolicy: p, limiter: rate.NewLimiter(rate.Limit(p.RatePerMinute/60), p.Burst)}
	}
	o.Channels = nil
	return &Scheduler{bus: bus, repo: repo, options: o, workers: workers, clock: o.Clock, log: log, policies: policies, fairSeq: map[event.Channel]int64{}, waiting: map[string]*queued{}, active: map[string]*running{}, sessions: map[sessionKey]string{}, cooldowns: map[sessionKey]cooldown{}, repeats: map[repeatKey]time.Time{}, wake: make(chan struct{}, 1), done: make(chan completion, o.MaxConcurrent)}, nil
}
func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Scheduler) stopTimers() {
	for key, c := range s.cooldowns {
		c.timer.Stop()
		delete(s.cooldowns, key)
	}
}
func (s *Scheduler) nextRenew(c event.Claim) time.Time {
	now := s.clock.Now()
	remaining := c.LeaseUntil.Sub(now)
	if remaining <= 0 {
		return now
	}
	return now.Add(remaining / 3)
}
func (s *Scheduler) deferClaim(ctx context.Context, c event.Claim, until time.Time) error {
	floor := s.clock.Now().Add(s.options.RetryDelay)
	if until.Before(floor) {
		until = floor
	}
	err := s.bus.Defer(ctx, c, until)
	if errors.Is(err, event.ErrLeaseLost) {
		return nil
	}
	return err
}
func (s *Scheduler) skip(ctx context.Context, c event.Claim) error {
	err := s.bus.Mark(ctx, c, []event.Status{event.StatusQueued}, event.StatusSkipped)
	if errors.Is(err, event.ErrLeaseLost) {
		return nil
	}
	return err
}

func (s *Scheduler) task(ctx context.Context, c event.Claim) (*store.Task, error) {
	t, err := s.repo.Task(ctx, c.Event.ID)
	if err != nil {
		return nil, err
	}
	if t.Validate() != nil || t.EventID != c.Event.ID || t.Channel != c.Event.Channel || t.Session != c.Event.Session {
		return nil, store.ErrConflict
	}
	return &t, nil
}
func (s *Scheduler) add(ctx context.Context, c event.Claim) error {
	if c.Event.ID == "" || c.Event.Status != event.StatusQueued || c.Event.Dir != event.DirIn || c.Lease.Owner == "" || c.Lease.Version < 1 || (c.Stage != event.StageSchedule && c.Stage != event.StageGenerate && c.Stage != event.StageDeliver && c.Stage != event.StageVerify) {
		return store.ErrInvalid
	}
	if active := s.active[c.Event.ID]; active != nil {
		if c.Lease.Version > active.work.Claim.Lease.Version {
			if active.receipt != nil {
				return s.deferClaim(ctx, c, s.clock.Now().Add(s.options.RetryDelay))
			}
			active.lost = true
			active.cancel()
			return s.deferClaim(ctx, c, s.clock.Now().Add(s.options.RetryDelay))
		}
		return nil
	}
	if old := s.waiting[c.Event.ID]; old != nil {
		if c.Lease.Version <= old.claim.Lease.Version {
			return nil
		}
		heap.Remove(&s.pending, old.index)
		delete(s.waiting, c.Event.ID)
	}
	item := &queued{claim: c, priority: 2, renewAt: s.nextRenew(c)}
	if p := s.policies[c.Event.Channel]; p != nil {
		item.priority = priority(c.Event, p.ChannelPolicy)
	}
	if c.Stage == event.StageVerify {
		item.priority = 0
	}
	if c.Stage == event.StageGenerate || c.Stage == event.StageDeliver {
		t, err := s.task(ctx, c)
		if err != nil {
			return err
		}
		item.priority = t.Priority
	}
	// backfill（历史补采）永远排在 fresh 之后：它不该挤占实时回复的并发。
	if c.Event.CollectMode == "backfill" && item.priority < 3 {
		item.priority = 3
	}
	item.fair = s.fairSeq[c.Event.Channel]
	s.fairSeq[c.Event.Channel]++
	heap.Push(&s.pending, item)
	s.waiting[c.Event.ID] = item
	return nil
}

func (s *Scheduler) renewAll(ctx context.Context) error {
	now := s.clock.Now()
	for _, item := range append([]*queued(nil), s.pending...) {
		if now.Before(item.renewAt) {
			continue
		}
		c, err := s.bus.Renew(ctx, item.claim)
		if errors.Is(err, event.ErrLeaseLost) {
			heap.Remove(&s.pending, item.index)
			delete(s.waiting, item.claim.Event.ID)
			continue
		}
		if err != nil {
			return err
		}
		item.claim = c
		item.renewAt = s.nextRenew(c)
	}
	for _, job := range s.active {
		if job.lost || job.receipt != nil || now.Before(job.renewAt) {
			continue
		}
		c, err := s.bus.Renew(ctx, job.work.Claim)
		if errors.Is(err, event.ErrLeaseLost) {
			receipt, readErr := s.repo.Snapshot(ctx, job.work.Claim.Event.ID)
			if readErr != nil {
				return readErr
			}
			if ownSettlement(job, receipt) {
				job.receipt = &receipt
			} else {
				job.lost = true
				job.cancel()
			}
			continue
		}
		if err != nil {
			return err
		}
		job.work.Claim = c
		job.renewAt = s.nextRenew(c)
	}
	return nil
}

// cycle可被包内测试逐轮驱动；生产中只能由单Run循环调用。
func (s *Scheduler) cycle(parent context.Context) error {
	for {
		select {
		case done := <-s.done:
			if err := s.complete(parent, done); err != nil {
				return err
			}
		default:
			goto drained
		}
	}
drained:
	ctx, cancel := context.WithTimeout(parent, s.options.OperationTimeout)
	defer cancel()
	if err := s.renewAll(ctx); err != nil {
		return err
	}
	if _, err := s.bus.Recover(ctx, s.options.ClaimBatch); err != nil {
		return err
	}
	space := s.options.MaxPending - len(s.pending) - len(s.active)
	if space > 0 {
		claims, err := s.bus.ClaimPending(ctx, min(space, s.options.ClaimBatch))
		if err != nil {
			return err
		}
		for _, c := range claims {
			if err := s.add(ctx, c); err != nil {
				return err
			}
		}
	}
	now := s.clock.Now()
	for key, cd := range s.cooldowns {
		if !cd.until.After(now) {
			cd.timer.Stop()
			delete(s.cooldowns, key)
		}
	}
	for key, until := range s.repeats {
		if !until.After(now) {
			delete(s.repeats, key)
		}
	}
	for len(s.active) < s.options.MaxConcurrent && len(s.pending) > 0 {
		item := s.popDispatchable()
		if item == nil {
			break
		}
		delete(s.waiting, item.claim.Event.ID)
		if err := s.dispatch(ctx, parent, item); err != nil {
			return err
		}
	}
	return nil
}

// popDispatchable 按优先级弹出第一个未超过通道软配额的任务。
//
// 被配额挡下的任务先暂存再放回堆，顺序不变；通道并发上限只是软约束，
// 全局 MaxConcurrent 仍是硬上限。
func (s *Scheduler) popDispatchable() *queued {
	counts := make(map[event.Channel]int, len(s.policies))
	for _, job := range s.active {
		counts[job.work.Claim.Event.Channel]++
	}
	var held []*queued
	var chosen *queued
	for len(s.pending) > 0 {
		item := heap.Pop(&s.pending).(*queued)
		policy := s.policies[item.claim.Event.Channel]
		limit := 0
		if policy != nil {
			limit = policy.MaxConcurrent
		}
		if limit <= 0 || counts[item.claim.Event.Channel] < limit {
			chosen = item
			break
		}
		held = append(held, item)
	}
	for _, item := range held {
		heap.Push(&s.pending, item)
	}
	return chosen
}

func (s *Scheduler) dispatch(ctx, parent context.Context, item *queued) error {
	c, err := s.bus.Renew(ctx, item.claim)
	if errors.Is(err, event.ErrLeaseLost) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.Event.Status != event.StatusQueued {
		return store.ErrConflict
	}
	if c.Stage == event.StageSchedule {
		var span *trace.Span
		ctx, span = s.startStageSpan(ctx, c, "schedule")
		defer trace.End(span, nil)
	}
	policy := s.policies[c.Event.Channel]
	if policy == nil || !aclAllowed(c.Event, policy.ChannelPolicy) {
		if c.Stage == event.StageSchedule {
			return s.skip(ctx, c)
		}
		return s.deferClaim(ctx, c, s.clock.Now().Add(s.options.RetryDelay))
	}
	now := s.clock.Now()
	if c.Stage == event.StageSchedule {
		// 积压超时：事件从发生到准入超过 MaxEventAge（默认 60 秒）直接丢弃，
		// 按通道配置；未配置时沿用全局值（live 默认 60 秒）。
		maxAge := policy.MaxEventAge
		if maxAge <= 0 {
			maxAge = s.options.MaxEventAge
		}
		if maxAge > 0 && !c.Event.At.IsZero() && now.Sub(c.Event.At) > maxAge {
			s.recordSkipped(c, "stale")
			return s.skip(ctx, c)
		}
		// 弹幕路由在准入前决定：mention_only 只回 @，filter 走规则筛选；
		// 这是唯一合法的 queued→skipped 阶段，放在这里也保证不进 Admit、不生成 Task、不调用模型。
		if noiseReason(c.Event, policy.ChannelPolicy) != "" || routingSkip(c.Event, policy.ChannelPolicy) || s.repeats[repetition(c.Event)].After(now) {
			return s.skip(ctx, c)
		}
	}
	if _, busy := s.sessions[session(c.Event)]; busy {
		return s.deferClaim(ctx, c, now.Add(s.options.RetryDelay))
	}
	if until, err := s.repo.Earlier(ctx, c); err != nil {
		return err
	} else if until != nil {
		return s.deferClaim(ctx, c, *until)
	}
	// 同用户短窗口合并：后面还有一条待处理消息时，只回最后一条，
	// 前一条的正文会通过会话 Recent / 直播 viewerRecent 进入后一条的上下文。
	if policy.MergeWindow > 0 {
		if src, ok := s.repo.(interface {
			NewerSameUser(context.Context, event.Claim, time.Duration) (bool, error)
		}); ok {
			newer, err := src.NewerSameUser(ctx, c, policy.MergeWindow)
			if err != nil {
				return err
			}
			if newer {
				s.log.Info("同用户短窗口合并，跳过较早事件",
					zap.String("action", "merge_window"),
					zap.String("ch", string(c.Event.Channel)),
					zap.String("event_id", c.Event.ID),
				)
				return s.skip(ctx, c)
			}
		}
	}
	var task *store.Task
	expectedStage := c.Stage
	switch c.Stage {
	case event.StageSchedule:
		if cd, ok := s.cooldowns[session(c.Event)]; ok && cd.until.After(now) {
			return s.deferClaim(ctx, c, cd.until)
		}
		reservation := policy.limiter.ReserveN(now, 1)
		if !reservation.OK() {
			return store.ErrInvalid
		}
		if delay := reservation.DelayFrom(now); delay > 0 {
			reservation.CancelAt(now)
			return s.deferClaim(ctx, c, now.Add(delay))
		}
		from, until := dayWindow(now, s.options.Location)
		t := store.Task{ID: "task:" + c.Event.ID, EventID: c.Event.ID, Channel: c.Event.Channel, Session: c.Event.Session, Brain: policy.Brain, Priority: priority(c.Event, policy.ChannelPolicy), CreatedAt: now}
		admission, err := s.repo.Admit(ctx, c, t, store.ScheduleQuota{Limit: policy.DailyQuota, From: from, Until: until})
		if err != nil {
			reservation.CancelAt(now)
			return err
		}
		if admission.WaitUntil != nil {
			reservation.CancelAt(now)
			return s.deferClaim(ctx, c, *admission.WaitUntil)
		}
		task, err = s.task(ctx, c)
		if err != nil {
			return err
		}
		expectedStage = event.StageGenerate
		if policy.RepeatWindow > 0 {
			s.repeats[repetition(c.Event)] = now.Add(policy.RepeatWindow)
		}
	case event.StageGenerate, event.StageDeliver:
		if c.Stage == event.StageDeliver && s.workers.Deliver == nil {
			return store.ErrInvalid
		}
		task, err = s.task(ctx, c)
		if err != nil {
			return err
		}
	case event.StageVerify:
	default:
		return store.ErrInvalid
	}
	// 顺序/任务/准入查询也会消耗租期；所有分支在启动下游前再次以PG作围栏。
	confirmed, err := s.bus.Renew(ctx, c)
	if errors.Is(err, event.ErrLeaseLost) {
		return nil
	}
	if err != nil {
		return err
	}
	if confirmed.Stage != expectedStage || confirmed.Event.Status != event.StatusQueued || confirmed.Event.ID != c.Event.ID || confirmed.Event.Channel != c.Event.Channel || confirmed.Event.Session != c.Event.Session || confirmed.Event.Private != c.Event.Private {
		return store.ErrConflict
	}
	c = confirmed
	s.launch(parent, Work{Claim: c, Task: task})
	return nil
}

// startStageSpan 以事件 ID 派生稳定 trace；跨阶段即使不共享 ctx 也能聚合到同一 trace。
func (s *Scheduler) startStageSpan(ctx context.Context, c event.Claim, stage string) (context.Context, *trace.Span) {
	if s.options.Tracer == nil {
		return ctx, nil
	}
	attrs := []trace.Attr{trace.A("channel", string(c.Event.Channel)), trace.A("stage", stage)}
	if !c.Event.CollectedAt.IsZero() {
		attrs = append(attrs, trace.A("queue_ms", time.Since(c.Event.CollectedAt).Milliseconds()))
	}
	return s.options.Tracer.StartEvent(ctx, c.Event.ID, stage, attrs...)
}

// recordSkipped 记录 stale 等跳过原因；事件仍按 skipped 结清，不计入 dead 分布。
func (s *Scheduler) recordSkipped(c event.Claim, reason string) {
	if s.options.ErrorRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.options.ErrorRepo.Record(ctx, store.EventError{
		EventID: c.Event.ID, Stage: "schedule", ErrorKind: reason, Message: "超过通道最大事件年龄",
	}); err != nil {
		s.log.Warn("写入跳过原因失败", zap.String("action", "skip_reason"), zap.String("event_id", c.Event.ID), zap.Error(err))
	}
}
