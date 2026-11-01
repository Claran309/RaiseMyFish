// Package live通过持久化投递协议发送弹幕，不执行模型调用。
package live

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"context"
	"errors"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"reflect"
	"sync/atomic"
	"time"
)

type Session interface {
	Post(context.Context, sender.Permit) (sender.Verdict, error)
	Close() error
}
type Transport interface {
	Open(context.Context, sender.Attempt) (Session, error)
	Verify(context.Context, sender.Attempt) (sender.Verdict, error)
}
type Options struct {
	Enabled        bool
	SelfID         string
	MinInterval    time.Duration
	CleanupTimeout time.Duration
}

func DefaultOptions() Options {
	return Options{MinInterval: 10 * time.Second, CleanupTimeout: 5 * time.Second}
}

type Service struct {
	repo      store.DeliveryRepo
	transport Transport
	options   Options
	log       *zap.Logger
	limiter   *rate.Limiter
	paused    atomic.Bool
}

func nilValue(v any) bool {
	if v == nil {
		return true
	}
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		return value.IsNil()
	}
	return false
}
func New(repo store.DeliveryRepo, transport Transport, o Options, log *zap.Logger) (*Service, error) {
	if nilValue(repo) || nilValue(transport) || !sender.ValidActor(o.SelfID) || o.MinInterval <= 0 || o.CleanupTimeout <= 0 || log == nil {
		return nil, &sender.Error{Kind: sender.KindInput}
	}
	return &Service{repo: repo, transport: transport, options: o, log: log, limiter: rate.NewLimiter(rate.Every(o.MinInterval), 1)}, nil
}
func (s *Service) Name() string { return "bili-live" }
func (s *Service) enabled(ctx context.Context) error {
	if ctx == nil {
		return &sender.Error{Kind: sender.KindInput}
	}
	if !s.options.Enabled {
		return &sender.Error{Kind: sender.KindSkipped}
	}
	if s.paused.Load() {
		return &sender.Error{Kind: sender.KindAuth}
	}
	return ctx.Err()
}
func (s *Service) Send(ctx context.Context, c event.Claim, p sender.Reply) (sender.Result, error) {
	if err := s.enabled(ctx); err != nil {
		return sender.Result{}, err
	}
	if err := p.Validate(c); err != nil {
		return sender.Result{}, err
	}
	a, err := s.repo.Prepare(ctx, c, p, s.options.SelfID)
	if err != nil {
		return sender.Result{}, err
	}
	return s.process(ctx, c, a)
}
func (s *Service) Resume(ctx context.Context, c event.Claim) (sender.Result, error) {
	if err := s.enabled(ctx); err != nil {
		return sender.Result{}, err
	}
	if c.Event.Channel != event.ChLive || c.Event.Dir != event.DirIn || c.Event.Private || !sender.ValidActor(c.Event.Session) || c.Lease.Owner == "" || c.Lease.Version < 1 {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput}
	}
	a, err := s.repo.Load(ctx, c, s.options.SelfID)
	if err != nil {
		return sender.Result{}, err
	}
	return s.process(ctx, c, a)
}
func (s *Service) cleanup(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), s.options.CleanupTimeout)
}
func (s *Service) wait(ctx context.Context, c event.Claim, a sender.Attempt, d sender.Deferral) (sender.Result, error) {
	save, cancel := s.cleanup(ctx)
	defer cancel()
	return s.repo.Defer(save, c, a.ID, d)
}
func (s *Service) settle(ctx context.Context, c event.Claim, a sender.Attempt, v sender.Verdict) (sender.Result, error) {
	if v.ErrorKind == "auth" || v.ErrorKind == "credentials" {
		s.paused.Store(true)
	}
	save, cancel := s.cleanup(ctx)
	defer cancel()
	return s.repo.Settle(save, c, a.ID, v)
}
func keepCause(err, cause error) error {
	if cause == nil {
		return err
	}
	var typed *sender.Error
	if errors.As(err, &typed) {
		copy := *typed
		copy.Cause = errors.Join(typed.Cause, cause)
		return &copy
	}
	return &sender.Error{Kind: sender.KindUncertain, Cause: errors.Join(err, cause)}
}
func (s *Service) process(ctx context.Context, c event.Claim, a sender.Attempt) (sender.Result, error) {
	if a.ActorID != s.options.SelfID || a.EventID != c.Event.ID || a.RoomID != c.Event.Session {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput}
	}
	switch a.State {
	case sender.StateSkipped:
		return a.Result(), &sender.Error{Kind: sender.KindSkipped, Settled: true}
	case sender.StateDead, sender.StateManual:
		return a.Result(), &sender.Error{Kind: sender.KindDead, Settled: true}
	case sender.StateAccepted:
		if a.Verified {
			return a.Result(), nil
		}
	}
	if a.State != sender.StateReady {
		v, err := s.transport.Verify(ctx, a)
		transportErr := err
		v.Origin = sender.OriginVerify
		if err != nil {
			v = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "network", Evidence: "none", Origin: sender.OriginVerify}
			var typed *sender.Error
			if errors.As(err, &typed) && typed.Kind == sender.KindAuth {
				v.ErrorKind = "auth"
			}
		}
		// 只读核验不能证明原请求未发；即使子进程误报也不允许重投。
		if !v.Verified {
			v.Outcome = sender.OutcomeUnknown
		}
		if a.MsgID != "" {
			if v.Verified && (v.MsgID != a.MsgID || v.Evidence != "history_id") {
				v = sender.Verdict{Outcome: sender.OutcomeUnknown, MsgID: a.MsgID, ErrorKind: "unconfirmed", Evidence: "none", Origin: sender.OriginVerify}
			} else if !v.Verified {
				v.MsgID = a.MsgID
			}
		}
		if err := v.Validate(); err != nil {
			v = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "protocol", Evidence: "none", Origin: sender.OriginVerify}
		}
		result, settleErr := s.settle(ctx, c, a, v)
		return result, keepCause(settleErr, transportErr)
	}
	now := time.Now()
	reservation := s.limiter.ReserveN(now, 1)
	if !reservation.OK() {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput}
	}
	if delay := reservation.DelayFrom(now); delay > 0 {
		reservation.CancelAt(now)
		return s.wait(ctx, c, a, sender.Deferral{Kind: sender.KindRetry, Code: "rate_wait", RetryAt: now.Add(delay)})
	}
	session, err := s.transport.Open(ctx, a)
	if err != nil {
		reservation.CancelAt(now)
		kind := sender.KindRetry
		code := "preflight"
		var typed *sender.Error
		if errors.As(err, &typed) && typed.Kind == sender.KindAuth {
			kind = sender.KindAuth
			code = "auth"
			s.paused.Store(true)
		}
		count := ctx.Err() == nil
		if !count {
			code = "cancelled"
		}
		result, waitErr := s.wait(ctx, c, a, sender.Deferral{Kind: kind, Code: code, CountFailure: count})
		return result, keepCause(waitErr, err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			s.log.Warn("发送子进程收尾异常", zap.String("action", "sender_close"))
		}
	}()
	permit, err := s.repo.Arm(ctx, c, a.ID)
	if err != nil {
		reservation.CancelAt(now)
		var typed *sender.Error
		if errors.As(err, &typed) && typed.Kind == sender.KindAuth {
			s.paused.Store(true)
		}
		return sender.Result{}, err
	}
	v, err := session.Post(ctx, permit)
	transportErr := err
	v.Origin = sender.OriginPost
	if err != nil {
		v = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "network", Evidence: "none", Origin: sender.OriginPost}
	}
	if err := v.Validate(); err != nil {
		v = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "protocol", Evidence: "none", Origin: sender.OriginPost}
	}
	if v.Outcome == sender.OutcomeNotPosted {
		reservation.CancelAt(now)
	}
	result, settleErr := s.settle(ctx, c, a, v)
	return result, keepCause(settleErr, transportErr)
}
