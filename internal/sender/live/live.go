// Package live通过持久化投递协议发送弹幕，不执行模型调用。
package live

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
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
	// VerifyAttempts 是单次核验流程内最多查几次 history（只读，不产生投递）。
	VerifyAttempts int
	// VerifyInterval 是两次核验之间的等待；B站弹幕进 history 有延迟，
	// 背靠背连查等于只查一次。为 0 表示不等待（仅测试用）。
	VerifyInterval time.Duration
}

func DefaultOptions() Options {
	return Options{MinInterval: 10 * time.Second, CleanupTimeout: 5 * time.Second, VerifyAttempts: 3, VerifyInterval: 1500 * time.Millisecond}
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
	if nilValue(repo) || nilValue(transport) || !sender.ValidActor(o.SelfID) || o.MinInterval <= 0 || o.CleanupTimeout <= 0 || o.VerifyAttempts < 1 || o.VerifyInterval < 0 || log == nil {
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
	a.ReplyMID, a.ReplyName = replyTarget(c.Event, a.Text, s.options.SelfID)
	if a.ReplyMID != "" {
		a.Text = stripMentionPrefix(a.Text, a.ReplyName)
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
	a.ReplyMID, a.ReplyName = replyTarget(c.Event, a.Text, s.options.SelfID)
	if a.ReplyMID != "" {
		a.Text = stripMentionPrefix(a.Text, a.ReplyName)
	}
	return s.process(ctx, c, a)
}

type replyTargetMeta struct {
	ReplyToID   string `json:"reply_to_id"`
	ReplyToName string `json:"reply_to_name"`
}

func usableReplyName(name string) string {
	name = strings.TrimSpace(name)
	if !validReplyName(name) {
		return ""
	}
	return name
}

// mentionsName 判断正文是否以「@昵称」开头；后接空白或结尾才算，避免 @小鱼 命中 @小鱼干。
func mentionsName(text, name string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	for _, marker := range []string{"@", "＠"} {
		rest, ok := strings.CutPrefix(trimmed, marker)
		if !ok || !strings.HasPrefix(rest, name) {
			continue
		}
		tail := rest[len(name):]
		if tail == "" {
			return true
		}
		if r, _ := utf8.DecodeRuneInString(tail); unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// stripMentionPrefix 去掉正文开头的「@昵称」前缀。
//
// 结构化 @（reply_mid）由平台渲染高亮，正文再带 @昵称会出现双重 @；
// 因此发送前在有结构化目标时剥离文本前缀。剥空时保留原文，避免发出空弹幕。
func stripMentionPrefix(text, name string) string {
	name = usableReplyName(name)
	if name == "" || !mentionsName(text, name) {
		return text
	}
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	for _, marker := range []string{"@", "＠"} {
		if rest, ok := strings.CutPrefix(trimmed, marker); ok {
			rest = strings.TrimSpace(rest[len(name):])
			if rest != "" {
				return rest
			}
			return text
		}
	}
	return text
}

// replyTarget 从事件Meta与已带@前缀的正文反推结构化@目标。
//
// B站语义：别人回复/@鱼时 reply_to_id 是鱼自己，此时要 @ 的是发弹幕的人；
// 只有回复对象确实是其他观众时，才使用 reply_to_id/reply_to_name。
func replyTarget(e event.Event, text, selfID string) (string, string) {
	if e.Channel != event.ChLive || len(e.Meta) == 0 {
		return "", ""
	}
	var meta replyTargetMeta
	if json.Unmarshal(e.Meta, &meta) != nil {
		return "", ""
	}
	fromName, replyName := usableReplyName(e.FromName), usableReplyName(meta.ReplyToName)
	if fromName != "" && fromName != replyName && mentionsName(text, fromName) && sender.ValidActor(e.FromID) && e.FromID != selfID {
		return e.FromID, fromName
	}
	if replyName != "" && mentionsName(text, replyName) && sender.ValidActor(meta.ReplyToID) && meta.ReplyToID != selfID {
		return meta.ReplyToID, replyName
	}
	return "", ""
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

// verifyLoop 在只读核验上做有界重试。
//
// 为什么需要（2026-11-02 冒烟根因）：3 条 unknown_manual 全是 `verify_count=3` 用尽、
// `evidence=none`、`msg_id` 为空——核验次数确实用完了，但三次**背靠背**查完（整个
// attempt 从 armed 到落库只花了约 1 秒）。而 gethistory 只返回房间最近一屏，
// 弹幕从发出到进 history 有 1~3 秒延迟，连查三次等于只查一次。
// 这里改成「有间隔地查几次」，把有效核验窗口从 ~0 秒拉到 ~3 秒。
//
// 安全性：只调 transport.Verify（只读 history），全程不会重新 POST。
func (s *Service) verifyLoop(ctx context.Context, a sender.Attempt) (sender.Verdict, error) {
	attempts := s.options.VerifyAttempts
	if attempts < 1 {
		attempts = 1
	}
	var last sender.Verdict
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 && s.options.VerifyInterval > 0 {
			timer := time.NewTimer(s.options.VerifyInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return last, ctx.Err()
			case <-timer.C:
			}
		}
		v, err := s.transport.Verify(ctx, a)
		last, lastErr = v, err
		if err == nil && v.Verified {
			return v, nil
		}
		if ctx.Err() != nil {
			return last, ctx.Err()
		}
	}
	return last, lastErr
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
		v, err := s.verifyLoop(ctx, a)
		transportErr := err
		v.Origin = sender.OriginVerify
		switch {
		case err != nil && ctx.Err() != nil:
			// 调用方预算耗尽，区别于「查了但没有」：方便把 manual 的原因分开看
			v = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "timeout", Evidence: "none", Origin: sender.OriginVerify}
		case err != nil:
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
