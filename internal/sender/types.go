// Package sender 定义P0-SEND-1纯类型；具体发送实现位于live子包。
package sender

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Action struct{ Name string }
type Reply struct {
	Text    string
	Actions []Action
	Meta    json.RawMessage
	Scope   memory.Scope
}
type Result struct {
	OK       bool
	MsgID    string
	Verified bool
	Error    string
}
type Sender interface {
	Name() string
	Send(context.Context, event.Claim, Reply) (Result, error)
	Resume(context.Context, event.Claim) (Result, error)
}
type Kind string

const (
	KindInput     Kind = "input"
	KindRetry     Kind = "retry"
	KindDead      Kind = "dead"
	KindSkipped   Kind = "skipped"
	KindAuth      Kind = "auth"
	KindUncertain Kind = "uncertain"
)

// Error.Settled只在PG原子释放输入租约后置true，便于worker区分已处理策略结果与基础设施失败。
type Error struct {
	Kind    Kind
	RetryAt time.Time
	Settled bool
	Cause   error `json:"-"`
}

func (e *Error) Error() string {
	kind := e.Kind
	switch kind {
	case KindInput, KindRetry, KindDead, KindSkipped, KindAuth, KindUncertain:
	default:
		kind = KindInput
	}
	return "弹幕发送结果（" + string(kind) + "）"
}
func (e *Error) Unwrap() error { return e.Cause }
func Handled(err error) bool   { var e *Error; return errors.As(err, &e) && e.Settled }

// ErrKind 供上层统一读取错误类别（与各通道错误类型同名方法）。
func (e *Error) ErrKind() string { return string(e.Kind) }

// RetryAfter 返回调度层应遵守的最早重试时间。
func (e *Error) RetryAfter() (time.Time, bool) { return e.RetryAt, e.Kind == KindRetry }

const (
	StateReady     = "ready"
	StateArmed     = "armed"
	StateAccepted  = "accepted"
	StateUnknown   = "unknown"
	StateRejected  = "rejected"
	StateNotPosted = "not_posted"
	StateSkipped   = "skipped"
	StateDead      = "dead"
	StateManual    = "manual"
)
const (
	OutcomeAccepted  = "accepted"
	OutcomeRejected  = "rejected"
	OutcomeUnknown   = "unknown"
	OutcomeNotPosted = "not_posted"
)
const (
	OriginPost   = "post"
	OriginVerify = "verify"
)

// Attempt包含不可变快照与受控结果，不含Cookie、模型Meta或原始响应。
// ReplyMID/ReplyName是运行时从事件Meta与正文反推的结构化@目标，不落库。
type Attempt struct {
	ID           string
	EventID      string
	ActorID      string
	RoomID       string
	Text         string
	ReplyMID     string `gorm:"-" json:"-"`
	ReplyName    string `gorm:"-" json:"-"`
	Nonce        string
	State        string
	ErrorKind    string
	MsgID        string
	Evidence     string
	Number       int
	VerifyCount  int
	PrepFailures int
	Verified     bool
	CreatedAt    time.Time
	PermitUntil  *time.Time
}
type Permit struct {
	AttemptID string    `json:"attempt_id"`
	RequestID string    `json:"request_id"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Verdict struct {
	Outcome   string `json:"outcome"`
	MsgID     string `json:"msg_id"`
	Verified  bool   `json:"verified"`
	ErrorKind string `json:"error_kind"`
	Evidence  string `json:"evidence"`
	Origin    string `json:"-"`
}
type Deferral struct {
	Kind         Kind
	Code         string
	RetryAt      time.Time
	CountFailure bool
}
type Policy struct {
	MinInterval      time.Duration
	DuplicateWindow  time.Duration
	PermitTTL        time.Duration
	RetryDelay       time.Duration
	MaxAttempts      int
	MaxVerifications int
}

func DefaultPolicy() Policy {
	return Policy{MinInterval: 10 * time.Second, DuplicateWindow: 10 * time.Second, PermitTTL: time.Second, RetryDelay: 10 * time.Second, MaxAttempts: 3, MaxVerifications: 3}
}
func (p Policy) Validate() error {
	if p.MinInterval <= 0 || p.DuplicateWindow <= 0 || p.PermitTTL <= 0 || p.PermitTTL > 10*time.Second || p.RetryDelay <= 0 || p.MaxAttempts < 1 || p.MaxAttempts > 3 || p.MaxVerifications < 1 || p.MaxVerifications > 3 {
		return &Error{Kind: KindInput}
	}
	return nil
}

var positiveID = regexp.MustCompile(`^[1-9][0-9]{0,127}$`)
var stableID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func ValidActor(id string) bool { return positiveID.MatchString(id) }
func (r Reply) Validate(c event.Claim) error {
	e := c.Event
	if c.Lease.Owner == "" || c.Lease.Version < 1 || e.ID == "" || e.Channel != event.ChLive || e.Dir != event.DirIn || e.Private || !positiveID.MatchString(e.Session) || memory.ValidateScope(r.Scope) != nil || r.Scope.Channel != e.Channel || r.Scope.Session != e.Session || r.Scope.Private || len(r.Actions) != 0 || strings.TrimSpace(r.Text) == "" || !utf8.ValidString(r.Text) || utf8.RuneCountInString(r.Text) > 40 || strings.ContainsRune(r.Text, 0) {
		return &Error{Kind: KindInput}
	}
	return nil
}
func (v Verdict) Validate() error {
	if v.Origin != OriginPost && v.Origin != OriginVerify {
		return &Error{Kind: KindInput}
	}
	switch v.Outcome {
	case OutcomeAccepted, OutcomeRejected, OutcomeUnknown, OutcomeNotPosted:
	default:
		return &Error{Kind: KindInput}
	}
	if v.Verified {
		if (v.Origin == OriginPost && v.Evidence != "receipt_id") || (v.Origin == OriginVerify && v.Evidence != "history_id" && v.Evidence != "history_nonce") {
			return &Error{Kind: KindInput}
		}
		if v.Outcome != OutcomeAccepted || !stableID.MatchString(v.MsgID) || strings.Trim(v.MsgID, "0") == "" || (v.Evidence != "receipt_id" && v.Evidence != "history_id" && v.Evidence != "history_nonce") {
			return &Error{Kind: KindInput}
		}
	} else if v.Evidence != "" && v.Evidence != "none" {
		return &Error{Kind: KindInput}
	}
	if v.MsgID != "" && !stableID.MatchString(v.MsgID) {
		return &Error{Kind: KindInput}
	}
	if !SafeCode(v.ErrorKind) {
		return fmt.Errorf("发送分类无效：%w", &Error{Kind: KindInput})
	}
	return nil
}
func SafeCode(code string) bool {
	switch code {
	case "", "auth", "credentials", "invalid", "rate_limit", "content", "unavailable", "protocol", "network", "timeout", "cancelled", "eof", "permit", "unconfirmed", "preflight", "rate_wait", "duplicate_window", "unknown_manual":
		return true
	}
	return false
}
func (a Attempt) Result() Result {
	return Result{OK: a.State == StateAccepted, MsgID: a.MsgID, Verified: a.Verified, Error: a.ErrorKind}
}
