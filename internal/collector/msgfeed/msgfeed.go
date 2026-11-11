// Package msgfeed 监听 B站账号消息（回复我的、@我的），把新消息写成评论事件。
//
// 边界：
//   - 只做「拉消息 → 规整成评论事件 → 按事件身份去重 → 产出」，不调模型、不发回复；
//   - 回复锚点用消息里的 source_id（rpid），楼中楼再带 root_id，交给评论发送器回线程；
//   - 事件 ID 沿用评论契约 `comment:{oid}:{rpid}`，同一评论从 reply/at 两边出现只产一条；
//   - 首次接入没有水位时只建立基线，不补答历史消息；之后按账号水位增量产出；
//   - 水位在桥确认事件落库后才提交（StartAck），channel 接收不算落库。
package msgfeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

const (
	defaultInterval = time.Minute
	maxTextRunes    = 4000
	maxSeen         = 8192
	// 评论区类型：只支持发送器认识的视频/专栏/动态；图文(11)等跳过并记日志。
	TypeVideo   = 1
	TypeDraw    = 11
	TypeArticle = 12
	TypeDynamic = 17
)

// Kind 是受控错误类别，不携带平台报文。
type Kind string

const (
	KindInput   Kind = "input"
	KindAuth    Kind = "auth"
	KindNetwork Kind = "network"
	KindRate    Kind = "rate_limit"
	KindTimeout Kind = "timeout"
)

// Error 是可安全记录的采集错误。
type Error struct {
	Kind  Kind
	Cause error `json:"-"`
}

func (e *Error) Error() string {
	k := e.Kind
	switch k {
	case KindInput, KindAuth, KindNetwork, KindRate, KindTimeout:
	default:
		k = KindNetwork
	}
	return "消息监听异常（" + string(k) + "）"
}

func (e *Error) Unwrap() error { return e.Cause }

// Message 是一条规整后的账号消息。
type Message struct {
	// Feed 是来源：reply（回复我的）或 at（@我的），只进事件 Meta 便于排查。
	Feed string
	// BusinessID 是评论区类型；只接受 1/11/12/17。
	BusinessID int
	// OID 是评论区所在对象。
	OID string
	// RPID 是要回复的那条评论；Root 是楼中楼线程根（顶层为空）。
	RPID string
	Root string
	// FromID / FromName 是发消息的人。
	FromID   string
	FromName string
	Text     string
	At       time.Time
}

// Batch 是一轮拉取结果。
//
// Commit 在本批消息全部获得持久化确认后调用，用于保存账号水位；
// Close 释放账号切换准入，整批处理结束（成功/失败/取消）都必须调用一次。
type Batch struct {
	Messages []Message
	SelfID   string
	Commit   func() error
	Close    func()
}

// Fetcher 拉取增量消息；真实实现走 B站 msgfeed，测试用假实现。
type Fetcher interface {
	Fetch(ctx context.Context) (Batch, error)
}

// Options 是采集参数。
type Options struct {
	Interval time.Duration
	Now      func() time.Time
	Log      *zap.Logger
}

// Collector 实现 collector.Collector。
type Collector struct {
	fetcher Fetcher
	opts    Options
	log     *zap.Logger

	mu      sync.RWMutex
	health  collector.Health
	seen    map[string]struct{}
	started atomic.Bool
}

var _ collector.Collector = (*Collector)(nil)
var _ collector.AckCollector = (*Collector)(nil)

// New 只做静态校验，不联网。
func New(f Fetcher, o Options) (*Collector, error) {
	if f == nil {
		return nil, &Error{Kind: KindInput, Cause: errors.New("消息来源不能为 nil")}
	}
	if o.Log == nil {
		return nil, &Error{Kind: KindInput, Cause: errors.New("日志器不能为 nil")}
	}
	if o.Interval <= 0 {
		o.Interval = defaultInterval
	}
	if o.Interval < 10*time.Second || o.Interval > time.Hour {
		return nil, &Error{Kind: KindInput, Cause: errors.New("消息监听间隔需在10秒至1小时")}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Collector{fetcher: f, opts: o, log: o.Log, seen: make(map[string]struct{})}, nil
}

// Name 与视频轮询同名：都是 comment 通道的采集源。
func (c *Collector) Name() string { return "bili-comment" }

// Health 返回快照。
func (c *Collector) Health() collector.Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

// Start 实现 collector.Collector；直连（无持久化回执通道）时按旧语义提交水位。
func (c *Collector) Start(ctx context.Context, events chan<- event.Event, errs chan<- error) error {
	return c.start(ctx, events, nil, errs)
}

// StartAck 实现 collector.AckCollector：每条事件等到落库回执后才记已见/推水位。
func (c *Collector) StartAck(ctx context.Context, events chan<- event.Event, acks <-chan collector.DeliveryAck, errs chan<- error) error {
	return c.start(ctx, events, acks, errs)
}

// start 先立即拉一轮，之后按 Interval 轮询；鉴权失效直接停采集。
func (c *Collector) start(ctx context.Context, events chan<- event.Event, acks <-chan collector.DeliveryAck, errs chan<- error) error {
	if ctx == nil {
		return &Error{Kind: KindInput, Cause: errors.New("上下文不能为 nil")}
	}
	if !c.started.CompareAndSwap(false, true) {
		return &Error{Kind: KindInput, Cause: errors.New("消息监听已启动")}
	}
	c.log.Info("消息监听已启动", zap.String("action", "msgfeed_start"), zap.Duration("interval", c.opts.Interval))
	for {
		if err := c.poll(ctx, events, acks); err != nil {
			c.record(err)
			if errors.Is(err, errAuth) {
				if errs != nil {
					select {
					case errs <- err:
					default:
					}
				}
				return err
			}
			c.log.Warn("消息监听本轮失败，等待下一轮", zap.String("action", "msgfeed_poll"), zap.Error(err))
		}
		timer := time.NewTimer(c.opts.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// errAuth 是内部哨兵：命中即停采集。
var errAuth = errors.New("消息监听鉴权失效")

// poll 拉一轮并产出事件；acks 非 nil 时每条事件都要等到桥的持久化回执。
func (c *Collector) poll(ctx context.Context, events chan<- event.Event, acks <-chan collector.DeliveryAck) error {
	batch, err := c.fetcher.Fetch(ctx)
	if err != nil {
		if errors.Is(err, errAuth) {
			return err
		}
		var kinded *Error
		if errors.As(err, &kinded) && kinded.Kind == KindAuth {
			return fmt.Errorf("%w: %v", errAuth, err)
		}
		return err
	}
	if batch.Close != nil {
		defer batch.Close()
	}
	self := strings.TrimSpace(batch.SelfID)
	if len(batch.Messages) > 0 && !validMID(self) {
		return &Error{Kind: KindInput, Cause: errors.New("消息批次缺少接收账号，拒绝产出归属不明的事件")}
	}
	now := c.opts.Now()
	// 稳定顺序：同一条评论从 reply/at 两边出现时，先到先出，事件 ID 去重。
	sort.SliceStable(batch.Messages, func(i, j int) bool {
		if !batch.Messages[i].At.Equal(batch.Messages[j].At) {
			return batch.Messages[i].At.Before(batch.Messages[j].At)
		}
		return batch.Messages[i].RPID < batch.Messages[j].RPID
	})
	for _, m := range batch.Messages {
		ev, ok := c.toEvent(m, self)
		if !ok {
			continue
		}
		if c.saw(ev.ID) {
			continue
		}
		select {
		case events <- ev:
		case <-ctx.Done():
			return nil
		}
		if acks != nil {
			select {
			case ack := <-acks:
				if ack.EventID != ev.ID {
					return fmt.Errorf("消息监听确认错位：期望 %s，实际 %s", ev.ID, ack.EventID)
				}
				if ack.Err != nil {
					return fmt.Errorf("消息事件持久化失败：%w", ack.Err)
				}
			case <-ctx.Done():
				return nil
			}
		}
		c.markSeen(ev.ID)
		c.recordEvent(now)
	}
	if batch.Commit != nil {
		if err := batch.Commit(); err != nil {
			return fmt.Errorf("保存消息监听水位：%w", err)
		}
	}
	return nil
}

// toEvent 校验并构造评论事件；事件 ID 与视频轮询保持同一契约。
func (c *Collector) toEvent(m Message, self string) (event.Event, bool) {
	oid, rpid := strings.TrimSpace(m.OID), strings.TrimSpace(m.RPID)
	from := strings.TrimSpace(m.FromID)
	text := m.Text
	if !validMID(oid) || !validMID(rpid) || !validMID(from) ||
		strings.Trim(oid, "0") == "" || strings.Trim(rpid, "0") == "" || strings.Trim(from, "0") == "" {
		return event.Event{}, false
	}
	if from == self {
		return event.Event{}, false
	}
	if m.BusinessID != TypeVideo && m.BusinessID != TypeDraw && m.BusinessID != TypeArticle && m.BusinessID != TypeDynamic {
		c.log.Debug("消息监听到不支持的评论区类型，已跳过",
			zap.String("action", "msgfeed_skip_type"),
			zap.Int("business_id", m.BusinessID),
			zap.String("feed", m.Feed),
		)
		return event.Event{}, false
	}
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return event.Event{}, false
	}
	if runes := []rune(text); len(runes) > maxTextRunes {
		text = string(runes[:maxTextRunes])
	}
	if m.At.IsZero() || m.At.Unix() <= 0 {
		return event.Event{}, false
	}
	name := m.FromName
	if !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		name = ""
	}
	if utf8.RuneCountInString(name) > 128 {
		name = string([]rune(name)[:128])
	}
	root := strings.TrimSpace(m.Root)
	if !validMID(root) || strings.Trim(root, "0") == "" {
		root = ""
	}
	meta, err := json.Marshal(struct {
		Source      string `json:"source"`
		Feed        string `json:"feed"`
		CommentType int    `json:"comment_type"`
		Root        string `json:"root,omitempty"`
		Parent      string `json:"parent,omitempty"`
	}{
		Source:      "bili-msgfeed",
		Feed:        m.Feed,
		CommentType: m.BusinessID,
		Root:        root,
		Parent:      rpid,
	})
	if err != nil {
		return event.Event{}, false
	}
	return event.Event{
		ID:       "comment:" + oid + ":" + rpid,
		Channel:  event.ChComment,
		Dir:      event.DirIn,
		Session:  oid,
		FromID:   from,
		FromName: name,
		Text:     text,
		Private:  false,
		ReplyTo:  rpid,
		At:       m.At.UTC(),
		Status:   event.StatusPending,
		Meta:     meta,
	}, true
}

// validMID 校验平台 UID/oid/rpid：纯数字且长度受限。
func validMID(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (c *Collector) saw(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.seen[id]
	return ok
}

func (c *Collector) markSeen(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) >= maxSeen {
		c.seen = make(map[string]struct{}, maxSeen/2)
	}
	c.seen[id] = struct{}{}
}

func (c *Collector) record(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.OK = false
	c.health.LastError = "消息监听失败"
	c.health.Reconnects++
}

func (c *Collector) recordEvent(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.OK = true
	c.health.LastEventAt = now
}
