// Package comment 通过 B站评论接口发送回复。
//
// 边界：一次 Send 只 POST 一次，绝不自动重试；平台返回 rpid 即成功。
// 不主动限速：只有平台明确返回限流/频繁时才按事件做指数退避（30s 起，最长 10 分钟）。
package comment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

const (
	defaultAPIRoot = "https://api.bilibili.com"
	maxReplyRunes  = 200
	maxBodyBytes   = 1 << 20
	// commentClosed 是「评论区已关闭」，平台明确拒收，重试没有意义。
	commentClosed = 12002
	// 被动退避：平台限流后 30s 起翻倍，封顶 10 分钟。
	backoffBase       = 30 * time.Second
	backoffMax        = 10 * time.Minute
	maxBackoffEntries = 1024
	// 评论类型：视频 / 动态；事件 Meta 缺省时按视频处理。
	commentTypeVideo   = 1
	commentTypeArticle = 12
	commentTypeDynamic = 17
)

// Options 由可信装配提供；凭据只保存值或路径，不进入日志。
type Options struct {
	Cookie      string `json:"-"`
	CookieFile  string `json:"-"`
	HTTPTimeout time.Duration
	APIRoot     string
}

func DefaultOptions() Options {
	return Options{HTTPTimeout: 10 * time.Second, APIRoot: defaultAPIRoot}
}

// Service 实现 sender.Sender；退避表并发安全。
type Service struct {
	options Options
	client  *http.Client
	log     *zap.Logger
	now     func() time.Time

	backoffMu sync.Mutex
	backoffs  map[string]backoffState
}

// backoffState 记录某条评论被平台限流后的下次可发时间与连续失败次数。
type backoffState struct {
	until    time.Time
	attempts int
}

var _ sender.Sender = (*Service)(nil)

var positiveID = regexp.MustCompile(`^[1-9][0-9]{0,127}$`)
var numericID = regexp.MustCompile(`^[0-9]{1,128}$`)

func canonicalID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !numericID.MatchString(value) {
		return "", false
	}
	value = strings.TrimLeft(value, "0")
	return value, value != ""
}

// New 只做静态校验，不读凭据文件、不访问平台。
func New(o Options, log *zap.Logger) (*Service, error) {
	if log == nil || o.HTTPTimeout <= 0 || (o.Cookie == "" && o.CookieFile == "") || strings.ContainsAny(o.Cookie+o.CookieFile, "\r\n\x00") {
		return nil, &sender.Error{Kind: sender.KindInput}
	}
	if strings.TrimSpace(o.APIRoot) == "" {
		o.APIRoot = defaultAPIRoot
	}
	o.APIRoot = strings.TrimRight(strings.TrimSpace(o.APIRoot), "/")
	return &Service{
		options:  o,
		client:   &http.Client{Timeout: o.HTTPTimeout},
		log:      log,
		now:      time.Now,
		backoffs: make(map[string]backoffState),
	}, nil
}

func (s *Service) Name() string { return "bili-comment" }

// Send 校验后最多 POST 一次；成功返回 rpid。
//
// 不主动限速；只有平台限流后才按事件退避，未到点的重投不会产生请求。
func (s *Service) Send(ctx context.Context, c event.Claim, p sender.Reply) (sender.Result, error) {
	if ctx == nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput}
	}
	oid, rpid, root, text, err := validate(c, p)
	if err != nil {
		return sender.Result{}, err
	}
	// 凭据每轮重读，支持运行期轮换文件。
	cookie, err := bili.LoadCookie(s.options.Cookie, s.options.CookieFile)
	if err != nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindAuth, Cause: err}
	}
	csrf := bili.CSRFToken(cookie)
	if csrf == "" {
		return sender.Result{}, &sender.Error{Kind: sender.KindAuth, Cause: errors.New("B站凭据缺少bili_jct")}
	}
	if ctx.Err() != nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindRetry, Cause: ctx.Err()}
	}
	now := s.now()
	if wait := s.backoffRemaining(c.Event.ID, now); wait > 0 {
		s.log.Info("评论仍在平台限流退避中", zap.String("action", "comment_send_backoff"), zap.String("event_id", c.Event.ID), zap.Duration("wait", wait))
		return sender.Result{}, &sender.Error{Kind: sender.KindRetry, RetryAt: now.Add(wait), Cause: errors.New("评论发送退避中")}
	}
	result, err := s.post(ctx, cookie, csrf, oid, rpid, root, text, replyType(c.Event))
	if err != nil {
		kind := errorKind(err)
		if kind == sender.KindRetry && ctx.Err() == nil {
			until := s.noteRateLimited(c.Event.ID, s.now())
			var typed *sender.Error
			if errors.As(err, &typed) {
				typed.RetryAt = until
			}
			s.log.Warn("评论发送被平台限流，指数退避",
				zap.String("action", "comment_send_backoff_scheduled"),
				zap.String("event_id", c.Event.ID),
				zap.Time("retry_at", until),
			)
		} else {
			s.clearBackoff(c.Event.ID)
			s.log.Warn("评论发送失败", zap.String("action", "comment_send_failed"), zap.String("event_id", c.Event.ID), zap.String("kind", string(kind)))
		}
		return result, err
	}
	s.clearBackoff(c.Event.ID)
	return result, nil
}

// backoffRemaining 返回该事件距离下次可发的剩余时间；到期也保留 attempts，成功时才清除。
func (s *Service) backoffRemaining(eventID string, now time.Time) time.Duration {
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	st, ok := s.backoffs[eventID]
	if !ok || !st.until.After(now) {
		// 已到点也保留 attempts，让下一次限流继续翻倍；成功时才清除。
		return 0
	}
	return st.until.Sub(now)
}

// noteRateLimited 记录一次平台限流并返回下次可发时间：30s 起翻倍，封顶 10 分钟。
func (s *Service) noteRateLimited(eventID string, now time.Time) time.Time {
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	st := s.backoffs[eventID]
	st.attempts++
	delay := backoffBase
	for i := 1; i < st.attempts && delay < backoffMax; i++ {
		delay *= 2
	}
	if delay > backoffMax {
		delay = backoffMax
	}
	st.until = now.Add(delay)
	s.backoffs[eventID] = st
	if len(s.backoffs) > maxBackoffEntries {
		for id, other := range s.backoffs {
			if id != eventID && !other.until.After(now) {
				delete(s.backoffs, id)
			}
		}
	}
	if len(s.backoffs) > maxBackoffEntries {
		// 极端情况下只保留当前事件，避免退避表无界增长。
		s.backoffs = map[string]backoffState{eventID: st}
	}
	return st.until
}

func (s *Service) clearBackoff(eventID string) {
	s.backoffMu.Lock()
	delete(s.backoffs, eventID)
	s.backoffMu.Unlock()
}

// replyType 从事件 Meta 取评论类型；缺省/非法值按视频评论处理。
func replyType(e event.Event) int {
	if len(e.Meta) == 0 {
		return commentTypeVideo
	}
	var meta struct {
		CommentType int `json:"comment_type"`
	}
	if err := json.Unmarshal(e.Meta, &meta); err != nil {
		return commentTypeVideo
	}
	switch meta.CommentType {
	case commentTypeVideo, commentTypeArticle, commentTypeDynamic:
		return meta.CommentType
	default:
		return commentTypeVideo
	}
}

// Resume 不重新 POST：评论接口没有安全的只读核验，重投可能造成重复回复。
func (s *Service) Resume(context.Context, event.Claim) (sender.Result, error) {
	return sender.Result{}, &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("评论发送不支持恢复核验")}
}

func validate(c event.Claim, p sender.Reply) (string, string, string, string, error) {
	input := func() (string, string, string, string, error) {
		return "", "", "", "", &sender.Error{Kind: sender.KindInput}
	}
	e := c.Event
	if c.Lease.Owner == "" || c.Lease.Version < 1 || e.Channel != event.ChComment || e.Dir != event.DirIn || e.Private || e.ID == "" {
		return input()
	}
	oid, ok := canonicalID(e.Session)
	if !ok {
		return input()
	}
	parts := strings.SplitN(e.ID, ":", 3)
	if len(parts) != 3 || parts[0] != string(event.ChComment) || parts[1] != oid {
		return input()
	}
	target := strings.TrimSpace(e.ReplyTo)
	if target == "" {
		target = parts[2]
	}
	rpid, ok := canonicalID(target)
	if !ok {
		return input()
	}
	// 多级回复：root 取采集到的线程根（顶层评论为 0，回落到本条 rpid）；
	// parent 始终是「被回复的这条评论」。
	root := replyRoot(e)
	if root == "" {
		root = rpid
	}
	if p.Scope.Channel != e.Channel || p.Scope.Session != e.Session || p.Scope.Private || len(p.Actions) != 0 {
		return input()
	}
	text := p.Text
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return input()
	}
	// 平台上限 200 字；模型偶尔超长时截断而不是让整条回复进 dead。
	if runes := []rune(text); len(runes) > maxReplyRunes {
		text = string(runes[:maxReplyRunes])
	}
	return oid, rpid, root, text, nil
}

// replyRoot 从事件 Meta 取线程根评论；缺失/非法/0 返回空。
func replyRoot(e event.Event) string {
	if len(e.Meta) == 0 {
		return ""
	}
	var meta struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(e.Meta, &meta); err != nil {
		return ""
	}
	root, ok := canonicalID(meta.Root)
	if !ok {
		return ""
	}
	return root
}

func (s *Service) post(ctx context.Context, cookie, csrf, oid, rpid, root, text string, commentType int) (sender.Result, error) {
	form := url.Values{}
	form.Set("oid", oid)
	form.Set("type", strconv.Itoa(commentType))
	form.Set("message", text)
	form.Set("root", root)
	form.Set("parent", rpid)
	form.Set("csrf", csrf)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.options.APIRoot+"/x/v2/reply/add", strings.NewReader(form.Encode()))
	if err != nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput}
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) RaiseMyFish/comment")
	resp, err := s.client.Do(req)
	if err != nil {
		// POST 可能已被平台接收，不能自动重试。
		return sender.Result{}, &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("评论发送请求失败")}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return sender.Result{Error: "network"}, statusFailure(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain}
	}
	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Rpid    platformID `json:"rpid"`
			RpidStr platformID `json:"rpid_str"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain}
	}
	if env.Code != 0 {
		kind, code := codeFailure(env.Code, env.Message)
		return sender.Result{Error: code}, kind
	}
	rpidOut := string(env.Data.Rpid)
	if rpidOut == "" {
		rpidOut = string(env.Data.RpidStr)
	}
	if !positiveID.MatchString(rpidOut) {
		// code=0 但拿不到 rpid：平台可能已收下，不能重试。
		return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain}
	}
	return sender.Result{OK: true, MsgID: rpidOut, Verified: true}, nil
}

func statusFailure(status int) error {
	switch status {
	case http.StatusPreconditionFailed, http.StatusTooManyRequests:
		// RetryAt 由 Send 按事件退避表填写。
		return &sender.Error{Kind: sender.KindRetry, Cause: fmt.Errorf("平台限流 HTTP %d", status)}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &sender.Error{Kind: sender.KindAuth}
	default:
		return &sender.Error{Kind: sender.KindUncertain}
	}
}

func codeFailure(code int, message string) (error, string) {
	switch code {
	case -101, -111, -403:
		return &sender.Error{Kind: sender.KindAuth}, "auth"
	case -352, -412, -509:
		return &sender.Error{Kind: sender.KindRetry, Cause: fmt.Errorf("平台限流 code %d", code)}, "rate_limit"
	case commentClosed:
		return &sender.Error{Kind: sender.KindDead, Cause: errors.New("评论区已关闭")}, "unavailable"
	}
	// 平台有时用业务 code 表达「操作太快」；文本命中同样按限流退避。
	if rateLimitMessage(message) {
		return &sender.Error{Kind: sender.KindRetry, Cause: fmt.Errorf("平台限流 code %d", code)}, "rate_limit"
	}
	// 未知错误可能已被平台接收，不能断言可重试，也不能直接判死。
	return &sender.Error{Kind: sender.KindUncertain, Cause: fmt.Errorf("平台返回 code %d", code)}, "unknown_manual"
}

// rateLimitMessage 识别平台限流/频繁的中英文提示（只用于分类，不写日志）。
func rateLimitMessage(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	for _, token := range []string{"操作太快", "操作太频繁", "太频繁", "请求过快", "稍后再试", "稍后重试", "rate limit", "too many", "too frequent", "frequent"} {
		if strings.Contains(msg, token) {
			return true
		}
	}
	return false
}

func errorKind(err error) sender.Kind {
	var typed *sender.Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return sender.KindUncertain
}

// platformID 接受数字或数字字符串，拒绝 0 与非十进制。
type platformID string

func (p *platformID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		raw = strings.TrimSpace(text)
	}
	if raw == "" {
		return nil
	}
	if !positiveID.MatchString(raw) {
		return errors.New("平台ID不是正整数")
	}
	*p = platformID(raw)
	return nil
}
