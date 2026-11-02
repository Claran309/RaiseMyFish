package live

// 本文件是主动弹幕的发送适配：复用 NodeTransport 的一次性投递协议，
// 但不经过 send_plans/send_attempts（主动弹幕没有输入事件与租约），
// 只把真实发送结果写成 DirOut 事件，保持「发过什么、结果如何」可审计。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// OutEventRecorder 只依赖事件写入能力，避免把仓储整包注入发送器。
type OutEventRecorder interface {
	Insert(context.Context, event.Event) (bool, error)
}

// ProactiveOptions 是主动弹幕发送器的静态参数；凭据仍由 Transport 持有。
type ProactiveOptions struct {
	RoomID         string
	SelfID         string
	PermitTTL      time.Duration
	CleanupTimeout time.Duration
}

func DefaultProactiveOptions() ProactiveOptions {
	return ProactiveOptions{PermitTTL: 5 * time.Second, CleanupTimeout: 5 * time.Second}
}

// ProactiveSender 每调用一次只投递一条；不重试、不排队，由上层限流决定节奏。
type ProactiveSender struct {
	transport Transport
	recorder  OutEventRecorder
	options   ProactiveOptions
	log       *zap.Logger
	now       func() time.Time
	newToken  func() (string, error)
	newNonce  func() (string, error)
}

// NewProactiveSender 只校验依赖与静态参数，不启动进程、不访问平台。
func NewProactiveSender(transport Transport, recorder OutEventRecorder, o ProactiveOptions, log *zap.Logger) (*ProactiveSender, error) {
	if nilValue(transport) || nilValue(recorder) || !sender.ValidActor(o.SelfID) || !sender.ValidActor(o.RoomID) ||
		o.PermitTTL <= 0 || o.PermitTTL > 10*time.Second || o.CleanupTimeout <= 0 || log == nil {
		return nil, &sender.Error{Kind: sender.KindInput}
	}
	return &ProactiveSender{
		transport: transport,
		recorder:  recorder,
		options:   o,
		log:       log,
		now:       time.Now,
		newToken:  randomToken,
		newNonce:  randomNonce,
	}, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "pro-" + hex.EncodeToString(buf), nil
}

// randomNonce 必须满足平台协议的正整数要求，和 SendRepo.Arm 保持同一口径。
func randomNonce() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(9223372036854775807))
	if err != nil {
		return "", err
	}
	n.Add(n, big.NewInt(1))
	return n.String(), nil
}

func proactiveText(text string) bool {
	return strings.TrimSpace(text) != "" && utf8.ValidString(text) && utf8.RuneCountInString(text) <= 40 && !strings.ContainsRune(text, 0)
}

// outStatus 把投递结果映射为出事件的合法初始状态（done/retry/dead/skipped）。
func outStatus(outcome string) (event.Status, bool) {
	switch outcome {
	case sender.OutcomeAccepted:
		return event.StatusDone, true
	case sender.OutcomeNotPosted:
		return event.StatusSkipped, true
	case sender.OutcomeRejected, sender.OutcomeUnknown:
		return event.StatusDead, true
	}
	return "", false
}

func proactiveMeta(attemptID, outcome, kind string, v sender.Verdict) (json.RawMessage, error) {
	// 只持久化白名单字段；正文跟随事件本身，原始响应不进入审计。
	return json.Marshal(map[string]any{
		"attempt_id": attemptID,
		"outcome":    outcome,
		"error_kind": kind,
		"verified":   v.Verified,
		"evidence":   v.Evidence,
		"mode":       "proactive",
	})
}

func (p *ProactiveSender) record(ctx context.Context, attempt sender.Attempt, status event.Status, outcome, kind string, v sender.Verdict) error {
	meta, err := proactiveMeta(attempt.ID, outcome, kind, v)
	if err != nil {
		return err
	}
	out := event.Event{
		ID:      "live:out:" + attempt.RoomID + ":" + attempt.ID,
		Channel: event.ChLive,
		Dir:     event.DirOut,
		Session: attempt.RoomID,
		FromID:  attempt.ActorID,
		Text:    attempt.Text,
		At:      p.now().UTC(),
		Status:  status,
		Meta:    meta,
	}
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.options.CleanupTimeout)
	defer cancel()
	if _, err := p.recorder.Insert(save, out); err != nil {
		return fmt.Errorf("记录主动弹幕出事件失败：%w", err)
	}
	return nil
}

// Send 投递一条主动弹幕。发送失败仍会写一条说明原因的出事件，便于事后核对。
func (p *ProactiveSender) Send(ctx context.Context, text string) (sender.Result, error) {
	if ctx == nil || !proactiveText(text) {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput}
	}
	if err := ctx.Err(); err != nil {
		return sender.Result{}, err
	}
	id, err := p.newToken()
	if err != nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindRetry, Cause: err}
	}
	nonce, err := p.newNonce()
	if err != nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindRetry, Cause: err}
	}
	attempt := sender.Attempt{
		ID:        id,
		EventID:   "live:out:" + p.options.RoomID + ":" + id,
		ActorID:   p.options.SelfID,
		RoomID:    p.options.RoomID,
		Text:      text,
		Nonce:     nonce,
		State:     sender.StateReady,
		CreatedAt: p.now().UTC(),
	}

	session, err := p.transport.Open(ctx, attempt)
	if err != nil {
		kind := "preflight"
		var typed *sender.Error
		if errors.As(err, &typed) && typed.Kind == sender.KindAuth {
			kind = "auth"
		}
		result := sender.Result{Error: kind}
		recordErr := p.record(ctx, attempt, event.StatusSkipped, sender.OutcomeNotPosted, kind, sender.Verdict{Outcome: sender.OutcomeNotPosted, ErrorKind: kind, Evidence: "none"})
		return result, errors.Join(nodeFailure(sender.KindRetry, err), recordErr)
	}
	defer func() {
		if err := session.Close(); err != nil {
			p.log.Warn("主动弹幕发送子进程收尾异常", zap.String("action", "proactive_sender"))
		}
	}()

	permit := sender.Permit{AttemptID: attempt.ID, RequestID: attempt.ID, Nonce: nonce, ExpiresAt: p.now().Add(p.options.PermitTTL).UTC()}
	verdict, postErr := session.Post(ctx, permit)
	if postErr != nil {
		verdict = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "network", Evidence: "none", Origin: sender.OriginPost}
	}
	verdict.Origin = sender.OriginPost
	if err := verdict.Validate(); err != nil {
		verdict = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "protocol", Evidence: "none", Origin: sender.OriginPost}
	}
	status, ok := outStatus(verdict.Outcome)
	if !ok {
		verdict = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "protocol", Evidence: "none", Origin: sender.OriginPost}
		status = event.StatusDead
	}

	recordErr := p.record(ctx, attempt, status, verdict.Outcome, verdict.ErrorKind, verdict)
	result := sender.Result{OK: verdict.Outcome == sender.OutcomeAccepted, MsgID: verdict.MsgID, Verified: verdict.Verified, Error: verdict.ErrorKind}
	switch verdict.Outcome {
	case sender.OutcomeAccepted:
		return result, recordErr
	case sender.OutcomeNotPosted:
		return result, errors.Join(nodeFailure(sender.KindSkipped, postErr), recordErr)
	}
	return result, errors.Join(nodeFailure(sender.KindUncertain, postErr), recordErr)
}
