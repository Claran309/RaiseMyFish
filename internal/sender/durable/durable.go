// Package durable 给没有平台幂等键的通道加一层持久化投递计划。
//
// 纪律与 live 一致：先落不可变正文，再 arm，最后记录 attempt；arm 之后崩溃
// 一律按未知处理，恢复只核验不重发。评论/私信没有可回读的核验接口，因此未知
// 会直接交回上层按不确定结束，而不是伪造 exactly-once。
package durable

import (
	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"context"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"
)

const defaultSaveTimeout = 5 * time.Second

// ActorSource 提供当前账号与按 MID 取旧账号的快照。
//
// Resume 只按计划原本的 MID 取账号，绝不回落到当前活跃账号。
type ActorSource interface {
	CurrentActor(ctx context.Context) (bili.Actor, error)
	ActorByMID(ctx context.Context, mid string) (bili.Actor, error)
}

// Admission 是账号切换的准入闸门；一次 Send/Resume 全程持有。
type Admission interface {
	BeginOperation(ctx context.Context) (func(), error)
}

// Options 是包装通道的参数；Validate 必须返回实际会被平台发送的正文。
type Options struct {
	Channel     event.Channel
	AccountID   string
	Validate    func(event.Claim, sender.Reply) (sender.Reply, error)
	MaxAttempts int
	SaveTimeout time.Duration
	// Actors/Admission 为空时退回固定 AccountID（兼容旧装配与单测）。
	Actors    ActorSource
	Admission Admission
}

// Sender 在真实通道前加投递计划与 attempt 记录。
type Sender struct {
	inner       sender.Sender
	repo        store.PlannedRepo
	channel     event.Channel
	account     string
	validate    func(event.Claim, sender.Reply) (sender.Reply, error)
	maxAttempts int
	saveTimeout time.Duration
	actors      ActorSource
	admission   Admission
	log         *zap.Logger
}

var _ sender.Sender = (*Sender)(nil)

func New(repo store.PlannedRepo, inner sender.Sender, o Options, log *zap.Logger) (*Sender, error) {
	if repo == nil || inner == nil || log == nil {
		return nil, &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递计划依赖不能为 nil")}
	}
	if o.Channel != event.ChComment && o.Channel != event.ChDM {
		return nil, &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递计划只支持评论/私信通道")}
	}
	if !sender.ValidActor(strings.TrimSpace(o.AccountID)) {
		return nil, &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递计划缺少合法发送账号")}
	}
	if o.Validate == nil {
		return nil, &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递计划缺少回复校验")}
	}
	if o.MaxAttempts < 1 || o.MaxAttempts > 3 {
		return nil, &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递计划重试次数必须在1至3之间")}
	}
	if o.SaveTimeout <= 0 {
		o.SaveTimeout = defaultSaveTimeout
	}
	return &Sender{
		inner: inner, repo: repo, channel: o.Channel, account: strings.TrimSpace(o.AccountID),
		validate: o.Validate, maxAttempts: o.MaxAttempts, saveTimeout: o.SaveTimeout,
		actors: o.Actors, admission: o.Admission, log: log,
	}, nil
}

func (s *Sender) Name() string { return s.inner.Name() + "-planned" }

// Send 先绑定账号快照，再按「落计划 → arm → POST → 记录结果」执行。
//
// 账号快照在准入窗口内取一次；若该事件已经有旧计划，则沿用计划原账号，
// 不允许同一份计划改由新活跃账号发送。私信首次建计划只认事件持久化的
// 接收账号（Meta.recv_actor / 复合 Session），归属缺失就冻结，不做猜测。
func (s *Sender) Send(ctx context.Context, c event.Claim, reply sender.Reply) (sender.Result, error) {
	if ctx == nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("上下文不能为 nil")}
	}
	if err := s.checkClaim(c); err != nil {
		return sender.Result{}, err
	}
	release, err := s.begin(ctx)
	if err != nil {
		return sender.Result{}, err
	}
	defer release()
	normalized, err := s.validate(c, reply)
	if err != nil {
		var typed *sender.Error
		if errors.As(err, &typed) {
			return sender.Result{}, err
		}
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: err}
	}
	text := strings.TrimSpace(normalized.Text)
	if text == "" {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("回复正文不能为空")}
	}
	planAccount := s.account
	var current *bili.Actor
	if s.actors != nil {
		if s.channel == event.ChDM {
			// 私信必须沿用接收这条消息的账号：当前活跃账号可能已经切走，
			// 拿它建计划会把 A 收到的私信变成 B 的回复。
			account, accountErr := s.dmPlanAccount(ctx, c)
			if accountErr != nil {
				return sender.Result{}, accountErr
			}
			planAccount = account
		} else {
			actor, actorErr := s.actors.CurrentActor(ctx)
			if actorErr != nil {
				s.log.Warn("读取当前发送账号失败，未发送",
					zap.String("action", "planned_actor_unavailable"),
					zap.String("event_id", c.Event.ID),
					zap.String("ch", string(s.channel)),
					zap.Error(actorErr),
				)
				return sender.Result{}, actorSendError(actorErr)
			}
			current = &actor
			if mid := strings.TrimSpace(actor.MID); mid != "" {
				planAccount = mid
			}
			existing, planErr := s.repo.PlanAccount(ctx, c.Event.ID)
			switch {
			case planErr == nil:
				if mid := strings.TrimSpace(existing); mid != "" {
					planAccount = mid
				}
			case errors.Is(planErr, store.ErrNotFound):
			default:
				s.log.Warn("读取投递计划账号失败，未发送",
					zap.String("action", "planned_account_lookup_failed"),
					zap.String("event_id", c.Event.ID),
					zap.String("ch", string(s.channel)),
					zap.Error(planErr),
				)
				return sender.Result{}, &sender.Error{Kind: sender.KindRetry, Cause: planErr}
			}
		}
	}
	if !sender.ValidActor(planAccount) {
		return sender.Result{}, &sender.Error{Kind: sender.KindAuth, Cause: errors.New("投递计划缺少可归属的发送账号")}
	}
	attempt, err := s.repo.PreparePlan(ctx, c, s.keyFor(c, planAccount), text)
	if err != nil {
		s.log.Warn("投递计划保存失败，未发送",
			zap.String("action", "planned_prepare_failed"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.Error(err),
		)
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrInvalid) {
			return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: err}
		}
		// 计划未落库就没有产生过平台请求，交回重试是安全的。
		return sender.Result{}, &sender.Error{Kind: sender.KindRetry, Cause: err}
	}
	return s.deliver(ctx, c, planAccount, attempt, true, s.actorFor(planAccount, current))
}

// Resume 只处理已有计划；armed/unknown 永不重新 POST。
//
// plan key 必须使用计划落库时的账号：当前活跃账号可能已经切走，
// 用新号重建 key 会直接变成「旧任务用新号重发」。
func (s *Sender) Resume(ctx context.Context, c event.Claim) (sender.Result, error) {
	if ctx == nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("上下文不能为 nil")}
	}
	if err := s.checkClaim(c); err != nil {
		return sender.Result{}, err
	}
	release, err := s.begin(ctx)
	if err != nil {
		return sender.Result{}, err
	}
	defer release()
	planAccount := s.account
	if s.actors != nil {
		account, lookupErr := s.repo.PlanAccount(ctx, c.Event.ID)
		if lookupErr != nil {
			if errors.Is(lookupErr, store.ErrNotFound) {
				return sender.Result{}, &sender.Error{Kind: sender.KindDead, Cause: errors.New("投递计划不存在，拒绝重发")}
			}
			s.log.Warn("读取投递计划账号失败",
				zap.String("action", "planned_account_lookup_failed"),
				zap.String("event_id", c.Event.ID),
				zap.String("ch", string(s.channel)),
				zap.Error(lookupErr),
			)
			return sender.Result{}, &sender.Error{Kind: sender.KindUncertain, Cause: lookupErr}
		}
		planAccount = strings.TrimSpace(account)
	}
	if !sender.ValidActor(planAccount) {
		return sender.Result{}, &sender.Error{Kind: sender.KindDead, Cause: errors.New("投递计划缺少可归属账号，拒绝重发")}
	}
	attempt, err := s.repo.LoadPlan(ctx, c, s.keyFor(c, planAccount))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return sender.Result{}, &sender.Error{Kind: sender.KindDead, Cause: errors.New("投递计划不存在，拒绝重发")}
		}
		s.log.Warn("读取投递计划失败",
			zap.String("action", "planned_load_failed"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.Error(err),
		)
		return sender.Result{}, &sender.Error{Kind: sender.KindUncertain, Cause: err}
	}
	return s.deliver(ctx, c, planAccount, attempt, c.Stage == event.StageDeliver, s.actorFor(planAccount, nil))
}

// dmPlanAccount 选择私信计划的发送账号。
//
// 已有计划优先（旧任务按原 actor 处理）；首次建计划只认事件持久化的接收
// 账号。事件缺少归属时明确冻结，绝不回落到当前活跃账号。
func (s *Sender) dmPlanAccount(ctx context.Context, c event.Claim) (string, error) {
	existing, planErr := s.repo.PlanAccount(ctx, c.Event.ID)
	switch {
	case planErr == nil:
		mid := strings.TrimSpace(existing)
		if mid == "" {
			return "", &sender.Error{Kind: sender.KindDead, Cause: errors.New("投递计划缺少可归属的发送账号")}
		}
		return mid, nil
	case errors.Is(planErr, store.ErrNotFound):
	default:
		s.log.Warn("读取投递计划账号失败，未发送",
			zap.String("action", "planned_account_lookup_failed"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.Error(planErr),
		)
		return "", &sender.Error{Kind: sender.KindRetry, Cause: planErr}
	}
	recv, ok := event.DMRecvActor(c.Event)
	if !ok {
		// 旧事件没有接收账号，不能猜成当前活跃账号；冻结等待人工处理。
		s.log.Warn("私信事件缺少接收账号归属，拒绝改用当前活跃账号",
			zap.String("action", "planned_recv_actor_missing"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
		)
		return "", &sender.Error{Kind: sender.KindDead, Cause: errors.New("私信事件缺少接收账号归属，拒绝改用当前活跃账号")}
	}
	return recv, nil
}

// begin 取得本次发送的账号切换准入，直到计划记录结束才释放。
func (s *Sender) begin(ctx context.Context) (func(), error) {
	if s.admission == nil {
		return func() {}, nil
	}
	release, err := s.admission.BeginOperation(ctx)
	if err != nil {
		return nil, &sender.Error{Kind: sender.KindRetry, Cause: err}
	}
	if release == nil {
		release = func() {}
	}
	return release, nil
}

// actorFor 返回本次投递的账号快照来源；只在真正需要 POST 时解析。
// prefer 是 Send 开头捕获的当前快照，只有计划账号与它一致时才复用。
func (s *Sender) actorFor(planAccount string, prefer *bili.Actor) func(context.Context) (bili.Actor, error) {
	if s.actors == nil {
		return nil
	}
	return func(ctx context.Context) (bili.Actor, error) {
		if prefer != nil && strings.TrimSpace(prefer.MID) == planAccount && prefer.Valid() {
			return *prefer, nil
		}
		actor, err := s.actors.ActorByMID(ctx, planAccount)
		if err != nil {
			return bili.Actor{}, err
		}
		if !actor.Identified() || strings.TrimSpace(actor.MID) != planAccount {
			return bili.Actor{}, bili.ErrNoCredential
		}
		return actor, nil
	}
}

// actorSendError 把账号解析失败映射成发送错误类别。
func actorSendError(err error) error {
	if errors.Is(err, bili.ErrNoCredential) {
		return &sender.Error{Kind: sender.KindAuth, Cause: err}
	}
	return &sender.Error{Kind: sender.KindRetry, Cause: err}
}

func (s *Sender) checkClaim(c event.Claim) error {
	e := c.Event
	if c.Lease.Owner == "" || c.Lease.Version < 1 || e.ID == "" || e.Channel != s.channel ||
		e.Dir != event.DirIn || e.Private != (s.channel == event.ChDM) {
		return &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递计划事件不匹配")}
	}
	// 私信 Session 是 "{接收账号}:{对方UID}"，旧数据只有对方 UID，两种都要认。
	if s.channel == event.ChDM {
		if _, ok := event.DMPeer(e); !ok {
			return &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递计划事件不匹配")}
		}
		return nil
	}
	if !sender.ValidActor(e.Session) {
		return &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递计划事件不匹配")}
	}
	return nil
}

func (s *Sender) keyFor(c event.Claim, account string) store.PlanKey {
	return store.PlanKey{Channel: s.channel, AccountID: strings.TrimSpace(account), Destination: c.Event.Session}
}

func (s *Sender) deliver(ctx context.Context, c event.Claim, planAccount string, a sender.Attempt, allowPost bool, postActor func(context.Context) (bili.Actor, error)) (sender.Result, error) {
	switch a.State {
	case sender.StateAccepted:
		// 平台已接受就是这条通道能拿到的最强证据；重复恢复只会再次返回同一事实。
		return sender.Result{OK: true, MsgID: a.MsgID, Verified: true}, nil
	case sender.StateArmed, sender.StateUnknown:
		s.log.Warn("投递结果未知，恢复只核验不重发",
			zap.String("action", "planned_unknown_no_repost"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.String("attempt_id", a.ID),
			zap.String("state", a.State),
		)
		return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("投递结果未知，恢复不重发")}
	case sender.StateDead, sender.StateManual:
		return a.Result(), &sender.Error{Kind: sender.KindDead, Cause: errors.New("投递尝试已终止")}
	case sender.StateSkipped:
		return a.Result(), &sender.Error{Kind: sender.KindSkipped, Cause: errors.New("投递尝试已跳过")}
	case sender.StateNotPosted:
		return a.Result(), &sender.Error{Kind: sender.KindDead, Cause: errors.New("可信未投递的尝试已用完")}
	case sender.StateRejected:
		return a.Result(), terminalError(a.ErrorKind)
	case sender.StateReady:
	default:
		return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("投递尝试状态未知")}
	}
	if !allowPost {
		return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("非投递阶段禁止重发")}
	}
	// 账号快照必须先于 arm 解析：拿不到旧账号凭据时仍保持 ready，
	// 既不会产生平台请求，也不会把尝试卡在 armed 的未知状态。
	var bound *bili.Actor
	if postActor != nil {
		actor, actorErr := postActor(ctx)
		if actorErr != nil || strings.TrimSpace(actor.MID) != planAccount {
			s.log.Warn("旧账号凭据不可用，拒绝改用其他账号发送",
				zap.String("action", "planned_actor_unavailable"),
				zap.String("event_id", c.Event.ID),
				zap.String("ch", string(s.channel)),
				zap.String("attempt_id", a.ID),
				zap.String("plan_account", planAccount),
				zap.Error(actorErr),
			)
			if actorErr == nil {
				actorErr = bili.ErrNoCredential
			}
			return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain, Cause: actorErr}
		}
		bound = &actor
	}
	armed, err := s.repo.ArmPlan(ctx, c, s.keyFor(c, planAccount), a.ID)
	if err != nil {
		s.log.Warn("投递许可未获得，拒绝发送",
			zap.String("action", "planned_arm_failed"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.String("attempt_id", a.ID),
			zap.Error(err),
		)
		return sender.Result{}, &sender.Error{Kind: sender.KindUncertain, Cause: err}
	}
	if ctx.Err() != nil {
		return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain, Cause: ctx.Err()}
	}
	// 只使用计划里的原正文：恢复时不再让模型重新生成。
	reply := sender.Reply{
		Text:  armed.Text,
		Scope: memory.Scope{Channel: c.Event.Channel, Session: c.Event.Session, Private: c.Event.Private},
	}
	if bound != nil {
		ctx = bili.WithActor(ctx, *bound)
	}
	result, sendErr := s.inner.Send(ctx, c, reply)
	outcome := s.classify(sendErr, result)
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.saveTimeout)
	_, recordErr := s.repo.RecordPlan(saveCtx, c, s.keyFor(c, planAccount), armed.ID, outcome)
	cancel()
	if recordErr != nil {
		s.log.Error("投递结果未落盘",
			zap.String("action", "planned_record_failed"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.String("attempt_id", armed.ID),
			zap.Error(recordErr),
		)
		// POST 可能已经成功，但本地无法确认；交给恢复按未知处理。
		return sender.Result{}, &sender.Error{Kind: sender.KindUncertain, Cause: recordErr}
	}
	switch outcome.State {
	case sender.StateAccepted:
		return sender.Result{OK: true, MsgID: outcome.MsgID, Verified: true}, nil
	case sender.StateNotPosted:
		s.log.Warn("投递被明确拒绝，可安全重试",
			zap.String("action", "planned_not_posted"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.String("attempt_id", armed.ID),
			zap.String("error_kind", outcome.ErrorKind),
		)
		if sendErr != nil {
			return sender.Result{Error: outcome.ErrorKind}, sendErr
		}
		return sender.Result{Error: outcome.ErrorKind}, &sender.Error{Kind: sender.KindRetry, Cause: errors.New("平台明确未接收")}
	case sender.StateRejected:
		s.log.Warn("投递被平台拒绝",
			zap.String("action", "planned_rejected"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.String("attempt_id", armed.ID),
			zap.String("error_kind", outcome.ErrorKind),
		)
		return sender.Result{Error: outcome.ErrorKind}, terminalError(outcome.ErrorKind)
	default:
		s.log.Warn("投递结果未知，停止自动处理",
			zap.String("action", "planned_unknown"),
			zap.String("event_id", c.Event.ID),
			zap.String("ch", string(s.channel)),
			zap.String("attempt_id", armed.ID),
			zap.String("error_kind", outcome.ErrorKind),
		)
		return sender.Result{Error: outcome.ErrorKind}, &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("投递结果未知")}
	}
}

func (s *Sender) classify(err error, result sender.Result) store.PlanOutcome {
	if result.OK {
		return store.PlanOutcome{State: sender.StateAccepted, OK: true, Verified: true, MsgID: strings.TrimSpace(result.MsgID)}
	}
	if err == nil {
		return store.PlanOutcome{State: sender.StateUnknown, ErrorKind: "protocol"}
	}
	kind := errorKind(err)
	code := planCode(kind)
	switch kind {
	case sender.KindRetry:
		if !s.trustedNotPosted(err) {
			// 无法断言平台没收到：宁可停止自动处理，也不能重发。
			return store.PlanOutcome{State: sender.StateUnknown, ErrorKind: code}
		}
		return store.PlanOutcome{State: sender.StateNotPosted, ErrorKind: code}
	case sender.KindInput, sender.KindAuth, sender.KindDead, sender.KindSkipped:
		return store.PlanOutcome{State: sender.StateRejected, ErrorKind: code}
	default:
		return store.PlanOutcome{State: sender.StateUnknown, ErrorKind: code}
	}
}

// trustedNotPosted 判断一次失败能否断言「平台没有收到」。
//
// 评论的重试错误只来自请求前的退避或平台明确限流；私信还可能把传输层
// 超时/网络失败归为 retry，此时请求可能已到达平台，必须按未知处理。
// 只有带明确下次可发时间的平台限流/退避才可信。
func (s *Sender) trustedNotPosted(err error) bool {
	if s.channel == event.ChComment {
		return true
	}
	var retryable interface{ RetryAfter() (time.Time, bool) }
	if !errors.As(err, &retryable) {
		return false
	}
	at, ok := retryable.RetryAfter()
	return ok && !at.IsZero()
}

func errorKind(err error) sender.Kind {
	var typed *sender.Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	var kinded interface{ ErrKind() string }
	if errors.As(err, &kinded) {
		switch kind := sender.Kind(kinded.ErrKind()); kind {
		case sender.KindInput, sender.KindRetry, sender.KindDead, sender.KindSkipped, sender.KindAuth, sender.KindUncertain:
			return kind
		}
	}
	return sender.KindUncertain
}

// planCode 把错误类别映射成可落库的稳定代码（send_attempts 的白名单口径）。
func planCode(kind sender.Kind) string {
	switch kind {
	case sender.KindInput:
		return "invalid"
	case sender.KindAuth:
		return "auth"
	case sender.KindRetry:
		return "rate_limit"
	case sender.KindDead, sender.KindSkipped:
		return "unavailable"
	default:
		return "unknown_manual"
	}
}

func terminalError(code string) error {
	switch code {
	case "auth":
		return &sender.Error{Kind: sender.KindAuth, Cause: errors.New("投递权限被拒绝")}
	case "invalid", "content":
		return &sender.Error{Kind: sender.KindInput, Cause: errors.New("投递内容无效")}
	case "unavailable":
		return &sender.Error{Kind: sender.KindDead, Cause: errors.New("投递目标不可用")}
	default:
		return &sender.Error{Kind: sender.KindDead, Cause: errors.New("投递尝试已终止")}
	}
}
