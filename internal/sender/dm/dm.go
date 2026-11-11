// Package dm 实现私信发送，并把「防封号」的护栏放在最前面。
//
// 默认 AllFollowers=true：所有发信人都可回，不做白名单/互关/日收信人限制。
// 显式 AllFollowers=false 时启用旧护护栏：
//   - 只允许发给主人 + 白名单；
//   - 一天最多 1 个收信人（防刷量）；
//   - 同一收信人 10 分钟限频 1 条；
//   - 互关检查：relation.attribute 必须是 6，否则拒发。
//
// 失败不重试；错误按 auth / retry / uncertain 分类交给上层。
package dm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

const (
	// defaultRate 是同一收信人的最小发送间隔（仅关闭 all_followers 时生效）。
	defaultRate = 10 * time.Minute
	// mutualAttribute 是「互相关注」的 attribute 值。
	mutualAttribute = 6
	// maxTextRunes 与派发单一致。
	maxTextRunes = 200
	// 被动退避：平台限流后 30s 起翻倍，封顶 10 分钟（按收信人记录）。
	backoffBase       = 30 * time.Second
	backoffMax        = 10 * time.Minute
	maxBackoffEntries = 1024
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
	// AllFollowers=true 时所有发信人可回；false 时启用白名单/互关/日收信人护栏。
	AllFollowers bool
	// AllowedMIDs 是白名单；主人 mid 由调用方并入。
	AllowedMIDs []string
	// RateLimit 是同收信人最小间隔，<=0 用 10 分钟。
	RateLimit time.Duration
	// Now 可注入时钟（测试用，日期切换也靠它）。
	Now func() time.Time
	// RecipientPath 是当日收信人状态文件；空用 state/dm-recipient.json。
	RecipientPath string
}

// Sender 实现 sender.Sender 的发送部分；Resume 交给上层（本通道不做重投）。
type Sender struct {
	transport    Transporter
	relation     Relation
	allFollowers bool
	rate         time.Duration
	now          func() time.Time

	// allowed 热更新：面板改白名单时整体替换，读写都要过锁。
	allowedMu sync.RWMutex
	allowed   map[string]struct{}

	store *RecipientStore

	mu       sync.Mutex
	lastSent map[string]time.Time
	backoffs map[string]backoffState
}

// backoffState 记录某收信人被平台限流后的下次可发时间与连续失败次数。
type backoffState struct {
	until    time.Time
	attempts int
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
	if !o.AllFollowers && len(allowed) == 0 {
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
	s := &Sender{
		transport: t, relation: r, allFollowers: o.AllFollowers,
		allowed: allowed, rate: rate, now: now,
		lastSent: make(map[string]time.Time),
		backoffs: make(map[string]backoffState),
	}
	if o.AllFollowers {
		return s, nil
	}
	path := strings.TrimSpace(o.RecipientPath)
	if path == "" {
		path = "state/dm-recipient.json"
	}
	store, err := NewRecipientStore(path)
	if err != nil {
		return nil, err
	}
	s.store = store
	return s, nil
}

// Name 返回通道名。
func (s *Sender) Name() string { return "dm" }

// Resume 不实现重投：私信一次成型，重投风险高于收益。
func (s *Sender) Resume(_ context.Context, claim event.Claim) (sender.Result, error) {
	return sender.Result{}, &Error{Kind: KindUncertain, Cause: errors.New("私信不支持重投")}
}

// Normalize 校验回复并返回平台实际会发送的正文（去首尾空白，不截断）。
//
// 投递计划用它落不可变快照：计划一旦写入，恢复时只按快照正文重投。
func Normalize(c event.Claim, p sender.Reply) (sender.Reply, error) {
	if c.Lease.Owner == "" || c.Lease.Version < 1 || c.Event.Channel != event.ChDM || c.Event.Dir != event.DirIn || !c.Event.Private {
		return sender.Reply{}, &Error{Kind: KindInput, Cause: errors.New("私信事件不匹配")}
	}
	mid, ok := event.DMPeer(c.Event)
	if !ok || !isDigits(mid) {
		return sender.Reply{}, &Error{Kind: KindInput, Cause: errors.New("私信缺少收信人")}
	}
	if p.Scope.Channel != c.Event.Channel || p.Scope.Session != c.Event.Session || !p.Scope.Private {
		return sender.Reply{}, &Error{Kind: KindInput, Cause: errors.New("回复作用域不匹配")}
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return sender.Reply{}, &Error{Kind: KindInput, Cause: errors.New("私信缺少正文")}
	}
	if len([]rune(text)) > maxTextRunes {
		return sender.Reply{}, &Error{Kind: KindInput, Cause: errors.New("私信正文不能超过200字")}
	}
	return sender.Reply{Text: text, Scope: p.Scope}, nil
}

// Send 走完整护栏链后发送。claim.Session 是对话 mid，reply.Text 是正文。
func (s *Sender) Send(ctx context.Context, claim event.Claim, reply sender.Reply) (sender.Result, error) {
	if ctx == nil {
		return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("上下文不能为 nil")}
	}
	mid, ok := event.DMPeer(claim.Event)
	if !ok || !isDigits(mid) {
		mid = ""
	}
	text := strings.TrimSpace(reply.Text)
	if mid == "" || text == "" {
		return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("私信缺少收信人或正文")}
	}
	if len([]rune(text)) > maxTextRunes {
		return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("私信正文不能超过200字")}
	}
	// 平台限流后的被动退避：未到点直接交回调度，不产生请求。
	if wait := s.backoffRemaining(mid, s.now()); wait > 0 {
		return sender.Result{}, &Error{Kind: KindRetry, RetryAt: s.now().Add(wait), Cause: errors.New("私信仍在限流退避中")}
	}

	// 日切快照：预占、释放、提交必须落在同一天，不能跨午夜各算各的。
	day, actor, attempt := "", "", ""
	if !s.allFollowers {
		day, actor = dayKey(s.now()), actorKey(ctx)
		// 1. 白名单
		if !s.allowedMID(mid) {
			return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("收信人不在私信白名单")}
		}
		// 每笔尝试一个独立令牌：只有它能释放本次写入的预占。
		token, tokenErr := newAttemptToken()
		if tokenErr != nil {
			return sender.Result{}, tokenErr
		}
		attempt = token
		// 2. 当日收信人原子预占（必须先于任何 POST）
		if err := s.reserveRecipient(day, actor, mid, attempt); err != nil {
			return sender.Result{}, err
		}
		// 3. 限频
		if err := s.checkRate(mid); err != nil {
			s.releaseRecipient(day, actor, mid, attempt)
			return sender.Result{}, err
		}
		// 4. 互关检查
		attribute, err := s.relation.Attribute(ctx, mid)
		if err != nil {
			s.releaseRecipient(day, actor, mid, attempt)
			return sender.Result{}, &Error{Kind: KindRetry, Cause: errors.New("关注关系查询失败")}
		}
		if attribute != mutualAttribute {
			s.releaseRecipient(day, actor, mid, attempt)
			return sender.Result{}, &Error{Kind: KindInput, Cause: errors.New("与收信人不是互关，拒绝发送")}
		}
	}

	msgID, err := s.transport.SendDM(ctx, mid, text)
	if err != nil {
		classified := classify(err)
		// 只有明确未发送的拒绝才释放本尝试的预占；retry/unknown 可能已送达平台。
		if !s.allFollowers {
			if definitelyNotSent(classified) {
				s.releaseRecipient(day, actor, mid, attempt)
			} else {
				s.retainRecipient(day, actor, mid, attempt)
			}
		}
		var typed *Error
		if errors.As(classified, &typed) && typed.Kind == KindRetry && ctx.Err() == nil {
			typed.RetryAt = s.noteRateLimited(mid, s.now())
		}
		return sender.Result{}, classified
	}
	s.clearBackoff(mid)
	if s.allFollowers {
		return sender.Result{OK: true, MsgID: msgID, Verified: msgID != ""}, nil
	}
	s.markSent(mid)
	// 预占已写盘；成功发送写 committed，防止并发的失败尝试误释放名额。
	if err := s.commitRecipient(day, actor, mid, attempt); err != nil {
		return sender.Result{OK: true, MsgID: msgID, Verified: msgID != ""}, &Error{Kind: KindUncertain, Cause: errors.New("私信已发出但收信人状态未落盘")}
	}
	return sender.Result{OK: true, MsgID: msgID, Verified: msgID != ""}, nil
}

// allowedMID 检查白名单。
func (s *Sender) allowedMID(mid string) bool {
	s.allowedMu.RLock()
	defer s.allowedMu.RUnlock()
	_, ok := s.allowed[mid]
	return ok
}

// SetAllowedMIDs 整体替换白名单（面板热更新用）。
//
// 调用方负责把主人 mid 并入；空列表或非法 mid 一律拒绝，避免误清空护栏。
func (s *Sender) SetAllowedMIDs(mids []string) error {
	allowed := make(map[string]struct{}, len(mids))
	for _, raw := range mids {
		mid := strings.TrimSpace(raw)
		if mid == "" {
			continue
		}
		if !isDigits(mid) {
			return &Error{Kind: KindInput, Cause: errors.New("白名单 mid 必须是纯数字")}
		}
		allowed[mid] = struct{}{}
	}
	if len(allowed) == 0 {
		return &Error{Kind: KindInput, Cause: errors.New("私信白名单不能为空")}
	}
	s.allowedMu.Lock()
	s.allowed = allowed
	s.allowedMu.Unlock()
	return nil
}

// newAttemptToken 生成一笔发送尝试的令牌（进程间唯一）。
func newAttemptToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", &Error{Kind: KindRetry, Cause: errors.New("生成私信预占令牌失败")}
	}
	return hex.EncodeToString(buf[:]), nil
}

// reserveRecipient 在 POST 前原子预占 actor/day 名额：换人就拒绝，同人放行。
func (s *Sender) reserveRecipient(day, actor, mid, attempt string) error {
	if err := s.store.Reserve(day, actor, mid, attempt); err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return err
		}
		return &Error{Kind: KindRetry, Cause: err}
	}
	return nil
}

// releaseRecipient 只释放本尝试明确未发出 POST 的预占；
// unknown/committed 与其它在途尝试的令牌都不受影响。
func (s *Sender) releaseRecipient(day, actor, mid, attempt string) {
	s.store.Release(day, actor, mid, attempt)
}

// retainRecipient 把本尝试结算为 unknown：结果可能已送达，当天名额不再释放。
func (s *Sender) retainRecipient(day, actor, mid, attempt string) {
	s.store.MarkUnknown(day, actor, mid, attempt)
}

// commitRecipient 在发送成功后锁定当天名额。
func (s *Sender) commitRecipient(day, actor, mid, attempt string) error {
	return s.store.Commit(day, actor, mid, attempt)
}

// actorKey 返回本次发送的账号身份（B站 MID）。
//
// durable 层会在调用内层发送器前注入账号快照；没有快照的旧调用沿用空键，
// 保持单账号装配原有的「全局一天一人」语义。
func actorKey(ctx context.Context) string {
	if actor, ok := bili.ActorFrom(ctx); ok {
		if mid := strings.TrimSpace(actor.MID); mid != "" {
			return mid
		}
	}
	return ""
}

// definitelyNotSent 判断平台是否明确没有接收。
//
// 鉴权/输入拒绝说明请求被平台挡下；retry/unknown 可能已经送达，
// 必须保留当天名额，宁可少发给另一个收信人。
func definitelyNotSent(err error) bool {
	var typed *Error
	if !errors.As(err, &typed) {
		return false
	}
	return typed.Kind == KindAuth || typed.Kind == KindInput
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

// backoffRemaining 返回该收信人距离下次可发的剩余时间；到期也保留 attempts，成功时才清除。
func (s *Sender) backoffRemaining(mid string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.backoffs[mid]
	if !ok || !st.until.After(now) {
		// 已到点也保留 attempts，让下一次限流继续翻倍；成功时才清除。
		return 0
	}
	return st.until.Sub(now)
}

// noteRateLimited 记录一次平台限流并返回下次可发时间：30s 起翻倍，封顶 10 分钟。
func (s *Sender) noteRateLimited(mid string, now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.backoffs[mid]
	st.attempts++
	delay := backoffBase
	for i := 1; i < st.attempts && delay < backoffMax; i++ {
		delay *= 2
	}
	if delay > backoffMax {
		delay = backoffMax
	}
	st.until = now.Add(delay)
	s.backoffs[mid] = st
	if len(s.backoffs) > maxBackoffEntries {
		for other, state := range s.backoffs {
			if other != mid && !state.until.After(now) {
				delete(s.backoffs, other)
			}
		}
	}
	if len(s.backoffs) > maxBackoffEntries {
		// 极端情况下只保留当前收信人，避免退避表无界增长。
		s.backoffs = map[string]backoffState{mid: st}
	}
	return st.until
}

func (s *Sender) clearBackoff(mid string) {
	s.mu.Lock()
	delete(s.backoffs, mid)
	s.mu.Unlock()
}

// Error 是受控错误，携带可映射的类别与可选的下次重试时间。
type Error struct {
	Kind    Kind
	RetryAt time.Time
	Cause   error `json:"-"`
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

// ErrKind 供上层统一读取错误类别（与 sender.Error 同名方法）。
func (e *Error) ErrKind() string { return string(e.Kind) }

// RetryAfter 返回调度层应遵守的最早重试时间。
func (e *Error) RetryAfter() (time.Time, bool) { return e.RetryAt, e.Kind == KindRetry }

// classify 把传输层错误映射成受控类别（限流/网络 → retry，风控/权限 → auth，其余 uncertain）。
//
// 用 %w 保留原始错误：dead/unknown 的日志要能展开到「缺 bili_jct」「缺本账号UID」
// 这类请求前就被拦下的原因，而不是只剩一句笼统的「结果不确定」。
func classify(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	msg := strings.ToLower(err.Error())
	switch {
	case rateLimitMessage(msg):
		return &Error{Kind: KindRetry, Cause: fmt.Errorf("私信被平台限流：%w", err)}
	case strings.Contains(msg, "bili_jct"), strings.Contains(msg, "本账号uid"), strings.Contains(msg, "未配置b站凭据"):
		return &Error{Kind: KindAuth, Cause: fmt.Errorf("私信缺少可用凭据：%w", err)}
	case strings.Contains(msg, "mid 必须"), strings.Contains(msg, "内容不能为空"), strings.Contains(msg, "编码失败"):
		return &Error{Kind: KindInput, Cause: fmt.Errorf("私信请求参数无效：%w", err)}
	case strings.Contains(msg, "auth"), strings.Contains(msg, "403"), strings.Contains(msg, "封禁"), strings.Contains(msg, "风控"):
		return &Error{Kind: KindAuth, Cause: fmt.Errorf("私信被风控或权限拒绝：%w", err)}
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "network"):
		return &Error{Kind: KindRetry, Cause: fmt.Errorf("私信网络异常：%w", err)}
	default:
		return &Error{Kind: KindUncertain, Cause: fmt.Errorf("私信结果不确定：%w", err)}
	}
}

func rateLimitMessage(lowered string) bool {
	for _, token := range []string{"rate limit", "too many", "too frequent", "frequent", "操作太快", "操作太频繁", "太频繁", "请求过快", "稍后再试", "稍后重试"} {
		if strings.Contains(lowered, token) {
			return true
		}
	}
	return false
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
