// Package comment 通过 B站评论接口发送回复。
//
// 边界：一次 Send 只 POST 一次，绝不自动重试；平台返回 rpid 即成功。
// 发送频率硬限制为 1 条/分钟（评论风控比弹幕严），失败也占用额度。
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
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

const (
	defaultAPIRoot = "https://api.bilibili.com"
	maxReplyRunes  = 200
	maxBodyBytes   = 1 << 20
	// commentClosed 是「评论区已关闭」，平台明确拒收，重试没有意义。
	commentClosed = 12002
)

// Options 由可信装配提供；凭据只保存值或路径，不进入日志。
type Options struct {
	Cookie      string `json:"-"`
	CookieFile  string `json:"-"`
	HTTPTimeout time.Duration
	APIRoot     string
	MinInterval time.Duration
}

func DefaultOptions() Options {
	return Options{HTTPTimeout: 10 * time.Second, APIRoot: defaultAPIRoot, MinInterval: time.Minute}
}

// Service 实现 sender.Sender；limiter 并发安全。
type Service struct {
	options Options
	client  *http.Client
	limiter *rate.Limiter
	log     *zap.Logger
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
	if log == nil || o.HTTPTimeout <= 0 || o.MinInterval <= 0 || (o.Cookie == "" && o.CookieFile == "") || strings.ContainsAny(o.Cookie+o.CookieFile, "\r\n\x00") {
		return nil, &sender.Error{Kind: sender.KindInput}
	}
	if strings.TrimSpace(o.APIRoot) == "" {
		o.APIRoot = defaultAPIRoot
	}
	o.APIRoot = strings.TrimRight(strings.TrimSpace(o.APIRoot), "/")
	return &Service{
		options: o,
		client:  &http.Client{Timeout: o.HTTPTimeout},
		limiter: rate.NewLimiter(rate.Every(o.MinInterval), 1),
		log:     log,
	}, nil
}

func (s *Service) Name() string { return "bili-comment" }

// Send 校验后最多 POST 一次；成功返回 rpid。
func (s *Service) Send(ctx context.Context, c event.Claim, p sender.Reply) (sender.Result, error) {
	if ctx == nil {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput}
	}
	oid, rpid, text, err := validate(c, p)
	if err != nil {
		return sender.Result{}, err
	}
	// 凭据每轮重读，支持运行期轮换文件；csrf 缺失不消耗限频额度。
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
	now := time.Now()
	reservation := s.limiter.ReserveN(now, 1)
	if !reservation.OK() {
		return sender.Result{}, &sender.Error{Kind: sender.KindInput}
	}
	if delay := reservation.DelayFrom(now); delay > 0 {
		// 没发出去就不占额度，让调度按 RetryAt 再来。
		reservation.CancelAt(now)
		s.log.Warn("评论发送触发限频", zap.String("action", "comment_send_rate_wait"), zap.Duration("delay", delay))
		return sender.Result{}, &sender.Error{Kind: sender.KindRetry, RetryAt: now.Add(delay), Cause: errors.New("评论发送限频")}
	}
	result, err := s.post(ctx, cookie, csrf, oid, rpid, text)
	if err != nil {
		s.log.Warn("评论发送失败", zap.String("action", "comment_send_failed"), zap.String("event_id", c.Event.ID), zap.String("kind", string(errorKind(err))))
	}
	return result, err
}

// Resume 不重新 POST：评论接口没有安全的只读核验，重投可能造成重复回复。
func (s *Service) Resume(context.Context, event.Claim) (sender.Result, error) {
	return sender.Result{}, &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("评论发送不支持恢复核验")}
}

func validate(c event.Claim, p sender.Reply) (string, string, string, error) {
	input := func() (string, string, string, error) { return "", "", "", &sender.Error{Kind: sender.KindInput} }
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
	return oid, rpid, text, nil
}

func (s *Service) post(ctx context.Context, cookie, csrf, oid, rpid, text string) (sender.Result, error) {
	form := url.Values{}
	form.Set("oid", oid)
	form.Set("type", "1")
	form.Set("message", text)
	form.Set("root", rpid)
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
		Code int `json:"code"`
		Data struct {
			Rpid    platformID `json:"rpid"`
			RpidStr platformID `json:"rpid_str"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return sender.Result{Error: "unknown_manual"}, &sender.Error{Kind: sender.KindUncertain}
	}
	if env.Code != 0 {
		kind, code := codeFailure(env.Code)
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
		return &sender.Error{Kind: sender.KindRetry, RetryAt: time.Now().Add(time.Minute)}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &sender.Error{Kind: sender.KindAuth}
	default:
		return &sender.Error{Kind: sender.KindUncertain}
	}
}

func codeFailure(code int) (error, string) {
	switch code {
	case -101, -111, -403:
		return &sender.Error{Kind: sender.KindAuth}, "auth"
	case -352, -412:
		return &sender.Error{Kind: sender.KindRetry, RetryAt: time.Now().Add(time.Minute)}, "rate_limit"
	case commentClosed:
		return &sender.Error{Kind: sender.KindDead, Cause: errors.New("评论区已关闭")}, "unavailable"
	default:
		return &sender.Error{Kind: sender.KindDead, Cause: fmt.Errorf("平台返回 code %d", code)}, "unknown_manual"
	}
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
