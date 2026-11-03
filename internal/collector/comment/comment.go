// Package comment 轮询视频评论区并把新评论写入事件总线。
//
// 边界：只做 HTTP 采集与事件构造，不调用模型、不发送回复；
// 去重以 rpid 为键，事件 ID 使用契约里的 `comment:{oid}:{rpid}`。
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
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

const (
	defaultAPIRoot = "https://api.bilibili.com"
	pageSize       = 20
	maxTextRunes   = 4000
	maxSeen        = 8192
	maxBodyBytes   = 1 << 20
)

// Kind 只展示受控类别，平台报文与凭据不进入错误文本。
type Kind string

const (
	KindInput    Kind = "input"
	KindAuth     Kind = "auth"
	KindNetwork  Kind = "network"
	KindProtocol Kind = "protocol"
	KindRate     Kind = "rate_limit"
	KindTimeout  Kind = "timeout"
)

// Error 保留程序可判断的原因链，不打印响应正文。
type Error struct {
	Kind  Kind
	Cause error `json:"-"`
}

func (e *Error) Error() string {
	kind := e.Kind
	switch kind {
	case KindInput, KindAuth, KindNetwork, KindProtocol, KindRate, KindTimeout:
	default:
		kind = KindProtocol
	}
	return "评论采集异常（" + string(kind) + "）"
}
func (e *Error) Unwrap() error { return e.Cause }

// Options 由可信装配提供；CookieFile 只保存路径，内容不进日志。
type Options struct {
	Videos      []string
	Interval    time.Duration
	SelfID      string
	Cookie      string `json:"-"`
	CookieFile  string `json:"-"`
	HTTPTimeout time.Duration
	APIRoot     string
}

func DefaultOptions() Options {
	return Options{Interval: time.Minute, HTTPTimeout: 10 * time.Second, APIRoot: defaultAPIRoot}
}

// Collector 只有一个 Start 拥有轮询循环；Health 可并发读取。
type Collector struct {
	options Options
	log     *zap.Logger
	client  *http.Client
	mu      sync.RWMutex
	health  collector.Health
	seen    map[string]struct{}
	started atomic.Bool
}

var _ collector.Collector = (*Collector)(nil)

var positiveID = regexp.MustCompile(`^[1-9][0-9]{0,127}$`)
var numericID = regexp.MustCompile(`^[0-9]{1,128}$`)

// canonicalID 接受平台十进制 ID，去掉前导零后要求非零。
func canonicalID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !numericID.MatchString(value) {
		return "", false
	}
	value = strings.TrimLeft(value, "0")
	return value, value != ""
}

// New 只做静态校验，不读凭据文件、不访问平台。
func New(o Options, log *zap.Logger) (*Collector, error) {
	if log == nil || len(o.Videos) == 0 || o.Interval <= 0 || o.HTTPTimeout <= 0 || (o.Cookie == "" && o.CookieFile == "") || strings.ContainsAny(o.Cookie+o.CookieFile, "\r\n\x00") {
		return nil, &Error{Kind: KindInput}
	}
	videos := make([]string, 0, len(o.Videos))
	unique := make(map[string]struct{}, len(o.Videos))
	for _, raw := range o.Videos {
		oid, ok := canonicalID(raw)
		if !ok {
			return nil, &Error{Kind: KindInput}
		}
		if _, duplicate := unique[oid]; duplicate {
			continue
		}
		unique[oid] = struct{}{}
		videos = append(videos, oid)
	}
	if len(videos) == 0 {
		return nil, &Error{Kind: KindInput}
	}
	if o.SelfID != "" {
		self, ok := canonicalID(o.SelfID)
		if !ok {
			return nil, &Error{Kind: KindInput}
		}
		o.SelfID = self
	}
	o.Videos = videos
	if strings.TrimSpace(o.APIRoot) == "" {
		o.APIRoot = defaultAPIRoot
	}
	o.APIRoot = strings.TrimRight(strings.TrimSpace(o.APIRoot), "/")
	return &Collector{
		options: o,
		log:     log,
		client:  &http.Client{Timeout: o.HTTPTimeout},
		seen:    make(map[string]struct{}),
	}, nil
}

func (c *Collector) Name() string { return "bili-comment" }

func (c *Collector) Health() collector.Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

func (c *Collector) setOK(ok bool) {
	c.mu.Lock()
	c.health.OK = ok
	c.mu.Unlock()
}

func (c *Collector) report(errc chan<- error, err *Error) {
	c.mu.Lock()
	c.health.LastError = err.Error()
	c.mu.Unlock()
	select {
	case errc <- err:
	default:
	}
}

// Start 先立即轮询一轮，之后按 Interval 轮询；取消返回 nil，不关闭调用方 channel。
func (c *Collector) Start(ctx context.Context, out chan<- event.Event, errc chan<- error) error {
	if ctx == nil || out == nil || errc == nil || !c.started.CompareAndSwap(false, true) {
		return &Error{Kind: KindInput}
	}
	defer c.setOK(false)

	// 凭据在启动时读取一次：失败属于不可自愈错误，直接停采集等人工处理。
	cookie, err := bili.LoadCookie(c.options.Cookie, c.options.CookieFile)
	if err != nil {
		fatal := &Error{Kind: KindAuth, Cause: err}
		c.report(errc, fatal)
		return fatal
	}
	c.log.Info("评论采集已启动",
		zap.String("action", "comment_collector_start"),
		zap.Int("videos", len(c.options.Videos)),
		zap.Duration("interval", c.options.Interval),
	)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if fatal := c.cycle(ctx, cookie, out, errc); fatal != nil {
			return fatal
		}
		timer := time.NewTimer(c.options.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// cycle 轮询全部视频；只有凭据类错误会让采集退出，其余错误留给下一轮。
func (c *Collector) cycle(ctx context.Context, cookie string, out chan<- event.Event, errc chan<- error) *Error {
	ok := true
	for _, oid := range c.options.Videos {
		if ctx.Err() != nil {
			return nil
		}
		replies, err := c.fetch(ctx, cookie, oid)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			ok = false
			c.report(errc, err)
			if err.Kind == KindAuth {
				return err
			}
			c.log.Warn("评论轮询失败，等待下一轮",
				zap.String("action", "comment_poll"),
				zap.String("oid", oid),
				zap.String("kind", string(err.Kind)),
			)
			continue
		}
		for _, reply := range replies {
			ev, keep, err := c.normalize(oid, reply)
			if err != nil {
				c.report(errc, err)
				continue
			}
			if !keep {
				continue
			}
			select {
			case out <- ev:
				c.mu.Lock()
				c.health.LastEventAt = time.Now().UTC()
				c.mu.Unlock()
			case <-ctx.Done():
				return nil
			}
		}
	}
	c.setOK(ok)
	return nil
}

// decimal 接受平台返回的数字或数字字符串；只保留十进制并拒绝 0。
type decimal string

func (d *decimal) UnmarshalJSON(data []byte) error {
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
	*d = decimal(raw)
	return nil
}

type apiReply struct {
	Rpid   decimal `json:"rpid"`
	Mid    decimal `json:"mid"`
	Ctime  int64   `json:"ctime"`
	Root   decimal `json:"root"`
	Parent decimal `json:"parent"`
	Member struct {
		Mid   decimal `json:"mid"`
		UName string  `json:"uname"`
	} `json:"member"`
	Content struct {
		Message string `json:"message"`
	} `json:"content"`
}

type envelope struct {
	Code int `json:"code"`
	Data struct {
		Replies []apiReply `json:"replies"`
	} `json:"data"`
}

// fetch 发一次只读请求，绝不重试。
func (c *Collector) fetch(parent context.Context, cookie, oid string) ([]apiReply, *Error) {
	ctx, cancel := context.WithTimeout(parent, c.options.HTTPTimeout)
	defer cancel()
	query := url.Values{}
	query.Set("type", "1")
	query.Set("oid", oid)
	query.Set("pn", "1")
	query.Set("ps", fmt.Sprint(pageSize))
	query.Set("sort", "2")
	endpoint := c.options.APIRoot + "/x/v2/reply?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &Error{Kind: KindInput}
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) RaiseMyFish/comment")
	resp, err := c.client.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return nil, &Error{Kind: KindNetwork, Cause: parent.Err()}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &Error{Kind: KindTimeout}
		}
		return nil, &Error{Kind: KindNetwork}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Kind: statusKind(resp.StatusCode), Cause: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return nil, &Error{Kind: KindProtocol}
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, &Error{Kind: KindProtocol}
	}
	if env.Code != 0 {
		return nil, &Error{Kind: codeKind(env.Code), Cause: fmt.Errorf("平台返回 code %d", env.Code)}
	}
	return env.Data.Replies, nil
}

func statusKind(status int) Kind {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return KindAuth
	case http.StatusTooManyRequests, http.StatusPreconditionFailed:
		return KindRate
	default:
		return KindNetwork
	}
}

func codeKind(code int) Kind {
	switch code {
	case -101, -111, -403:
		return KindAuth
	case -352, -412:
		return KindRate
	default:
		return KindProtocol
	}
}

// normalize 校验单条评论；重复与自己的评论返回 keep=false。
func (c *Collector) normalize(oid string, r apiReply) (event.Event, bool, *Error) {
	invalid := func() (event.Event, bool, *Error) { return event.Event{}, false, &Error{Kind: KindProtocol} }
	rpid, mid := string(r.Rpid), string(r.Member.Mid)
	if mid == "" {
		mid = string(r.Mid)
	}
	if !positiveID.MatchString(rpid) || !positiveID.MatchString(mid) {
		return invalid()
	}
	if !c.firstSeen(oid, rpid) {
		return event.Event{}, false, nil
	}
	if mid == c.options.SelfID {
		return event.Event{}, false, nil
	}
	text := r.Content.Message
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return invalid()
	}
	if runes := []rune(text); len(runes) > maxTextRunes {
		text = string(runes[:maxTextRunes])
	}
	name := r.Member.UName
	if !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		name = ""
	}
	if utf8.RuneCountInString(name) > 128 {
		name = string([]rune(name)[:128])
	}
	if r.Ctime <= 0 {
		return invalid()
	}
	meta, err := json.Marshal(struct {
		Source string `json:"source"`
		Root   string `json:"root,omitempty"`
		Parent string `json:"parent,omitempty"`
	}{"bili-comment", string(r.Root), string(r.Parent)})
	if err != nil {
		return invalid()
	}
	return event.Event{
		ID:       "comment:" + oid + ":" + rpid,
		Channel:  event.ChComment,
		Dir:      event.DirIn,
		Session:  oid,
		FromID:   mid,
		FromName: name,
		Text:     text,
		Private:  false,
		ReplyTo:  rpid,
		At:       time.Unix(r.Ctime, 0).UTC(),
		Status:   event.StatusPending,
		Meta:     meta,
	}, true, nil
}

// firstSeen 以 oid:rpid 为键去重；超过上限清空重来，持久化层仍会按事件 ID 幂等。
func (c *Collector) firstSeen(oid, rpid string) bool {
	key := oid + ":" + rpid
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[key]; ok {
		return false
	}
	if len(c.seen) >= maxSeen {
		c.seen = make(map[string]struct{}, maxSeen)
	}
	c.seen[key] = struct{}{}
	return true
}
