// Package dm 实现私信发送，并把「防封号」的护栏放在最前面。
//
// 护栏（派发单要求，一条都不能放宽）：
//   - 只允许发给主人 + 白名单；
//   - 一天最多 1 个收信人（防刷量）；
//   - 同一收信人 10 分钟限频 1 条；
//   - 互关检查：relation.attribute 必须是 6，否则拒发。
//
// 失败不重试；错误按 auth / retry / uncertain 分类交给上层。
package dm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

const (
	// defaultRate 是同一收信人的最小发送间隔。
	defaultRate = 10 * time.Minute
	// mutualAttribute 是「互相关注」的 attribute 值。
	mutualAttribute = 6
	// maxTextRunes 与派发单一致。
	maxTextRunes = 200
)

// Kind 是发送错误类别（映射到 sender.Kind）。
type Kind string

const (
	KindInput     Kind = "input"
	KindAuth      Kind = "auth"
	KindRetry     Kind = "retry"
	KindUncertain Kind = "uncertain"
)

// Transporter 真正把私信发出去（POST /msg/send，带 cookie/csrf）。
type Transporter interface {
	SendDM(ctx context.Context, mid, text string) (msgID string, err error)
}

// Relation 查关注关系；attribute==6 表示互关。
type Relation interface {
	Attribute(ctx context.Context, mid string) (int, error)
}

// Options 是发送参数。
type Options struct {
	// AllowedMIDs 是白名单；主人 mid 由调用方并入。
	AllowedMIDs []string
	// RateLimit 是同收信人最小间隔，<=0 用 10 分钟。
	RateLimit time.Duration
	// Now 可注入时钟（测试用，日期切换也靠它）。
	Now func() time.Time
}

// Sender 实现 sender.Sender 的发送部分；Resume 交给上层（本通道不做重投）。
type Sender struct {
	transport Transporter
	relation  Relation
	allowed   map[string]struct{}
	rate      time.Duration
	now       func() time.Time

	mu        sync.Mutex
	day       string
	recipient string
	lastSent  map[string]time.Time
}

var _ sender.Sender = (*Sender)(nil)

// New 校验依赖与白名单；不改动任何配置。
func New(t Transporter, r Relation, o Options) (*Sender, error) {
	if t == nil {
		return nil, &Error{Kind: KindInput, Cause: errors.New("私信发送器不能为 nil")}
	}
	if r == nil {
		return nil, &Error{Kind: KindInput, Cause: errors.New("关注关系检查不能为 nil")}
	}
	allowed := make(map[string]struct{}, len(o.AllowedMIDs))
	for _, raw := range o.AllowedMIDs {
		mid := strings.TrimSpace(raw)
		if mid == "" {
			continue
		}
		if !isDigits(mid) {
			return nil, &Error{Kind: KindInput, Cause: errors.New("白名单 mid 必须是纯数字")}
		}
		allowed[mid] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, &Error{Kind: KindInput, Cause: errors.New("私信白名单不能为空")}
	}
	rate := o.RateLimit
	if rate <= 0 {
		rate = defaultRate
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Sender{transport: t, relation: r, allowed: allowed, rate: rate, now: now, lastSent: make(map[string]time.Time)}, nil
}

// Name 返回通道名。
func (s *Sender) Name() string { return "dm" }

// Resume 不实现重投：私信一次成型，重投风险高于收益。
func (s *Sender) Resume(_ context.Context, claim event.Claim) (sender.Result, error) {
	return sender.Result{}, &Error{Kind: KindUncertain, Cause: errors.New("私信不支持重投")}
}

// Send 走完整护栏链后发送。claim.Session 是对话 mid，reply.Text 是正文。
func (s *Sender) Send(ctx context.Context, claim event.Claim, reply sender.Reply) (sender.Result, error) {
	if ctx == nil {
		return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("上下文不能为 nil")}
	}
	mid := strings.TrimSpace(claim.Event.Session)
	if mid == "" {
		mid = strings.TrimSpace(claim.Event.FromID)
	}
	text := strings.TrimSpace(reply.Text)
	if mid == "" || text == "" {
		return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("私信缺少收信人或正文")}
	}
	if len([]rune(text)) > maxTextRunes {
		return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("私信正文不能超过200字")}
	}

	// 1. 白名单
	if !s.allowedMID(mid) {
		return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("收信人不在私信白名单")}
	}
	// 2. 当日收信人锁定
	if err := s.reserveRecipient(mid); err != nil {
		return sender.Result{}, err
	}
	// 3. 限频
	if err := s.checkRate(mid); err != nil {
		return sender.Result{}, err
	}
	// 4. 互关检查
	attribute, err := s.relation.Attribute(ctx, mid)
	if err != nil {
		return sender.Result{}, &Error{Kind: KindRetry, Cause: errors.New("关注关系查询失败")}
	}
	if attribute != mutualAttribute {
		return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("与收信人不是互关，拒绝发送")}
	}

	msgID, err := s.transport.SendDM(ctx, mid, text)
	if err != nil {
		return sender.Result{}, classify(err)
	}
	s.markSent(mid)
	return sender.Result{OK: true, MsgID: msgID, Verified: msgID != ""}, nil
}

// allowedMID 检查白名单。
func (s *Sender) allowedMID(mid string) bool {
	_, ok := s.allowed[mid]
	return ok
}

// reserveRecipient 实现「一天最多 1 个收信人」：换人就拒绝，同人放行。
func (s *Sender) reserveRecipient(mid string) error {
	day := s.now().Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.day != day {
		s.day = day
		s.recipient = ""
	}
	if s.recipient != "" && s.recipient != mid {
		return &Error{Kind: KindInput, Cause: errors.New("今天已经给别的收信人发过私信")}
	}
	s.recipient = mid
	return nil
}

// checkRate 同一收信人 10 分钟一条。
func (s *Sender) checkRate(mid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastSent[mid]; ok && s.now().Sub(last) < s.rate {
		return &Error{Kind: KindInput, Cause: errors.New("私信太频繁，10 分钟后再试")}
	}
	return nil
}

func (s *Sender) markSent(mid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSent[mid] = s.now()
}

// Error 是受控错误，携带可映射的类别。
type Error struct {
	Kind  Kind
	Cause error `json:"-"`
}

func (e *Error) Error() string {
	k := e.Kind
	switch k {
	case KindInput, KindAuth, KindRetry, KindUncertain:
	default:
		k = KindUncertain
	}
	return "私信发送失败（" + string(k) + "）"
}

func (e *Error) Unwrap() error { return e.Cause }

// classify 把传输层错误映射成受控类别（风控/权限 → auth，网络 → retry，其余 uncertain）。
func classify(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "auth"), strings.Contains(msg, "403"), strings.Contains(msg, "封禁"), strings.Contains(msg, "风控"):
		return &Error{Kind: KindAuth, Cause: errors.New("私信被风控或权限拒绝")}
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "network"), strings.Contains(msg, "5"):
		return &Error{Kind: KindRetry, Cause: errors.New("私信网络异常")}
	default:
		return &Error{Kind: KindUncertain, Cause: errors.New("私信结果不确定")}
	}
}

func isDigits(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
