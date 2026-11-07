// Package dm 轮询 B站私信会话并把新消息写成事件（channel=dm）。
//
// 边界：
//   - 只做「拉会话 → 解析 → 按 msg_id 去重 → 产出事件」，不调模型、不发回复；
//   - 鉴权类错误直接停采集（继续轮询只会白挨风控），其余错误等下一轮；
//   - 事件 ID 固定 `dm:{peer}:{msg_id}`，保证幂等。
package dm

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

	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

const (
	defaultInterval = 2 * time.Minute
	maxTextRunes    = 4000
	maxSeen         = 8192
)

// Kind 是受控错误类别，不携带平台报文。
type Kind string

const (
	KindInput   Kind = "input"
	KindAuth    Kind = "auth"
	KindNetwork Kind = "network"
)

// Error 是可安全记录的采集错误。
type Error struct {
	Kind  Kind
	Cause error `json:"-"`
}

func (e *Error) Error() string {
	k := e.Kind
	switch k {
	case KindInput, KindAuth, KindNetwork:
	default:
		k = KindNetwork
	}
	return "私信采集异常（" + string(k) + "）"
}

func (e *Error) Unwrap() error { return e.Cause }

// Message 是一条规整后的私信。
type Message struct {
	// ID 是平台消息 id（去重键）。
	ID string
	// PeerID 是对话对方的 uid（也是事件 Session）。
	PeerID string
	// PeerName 是对方昵称。
	PeerName string
	// Text 是正文。
	Text string
	// At 是平台时间；为零时用采集时刻。
	At time.Time
}

// Batch 是一轮拉取结果。
//
// Commit 在本批消息全部成功送入总线后调用，用于持久化水位；
// 中途取消则不提交，下一轮会重新拉取（事件 ID 幂等兜底可能重复）。
type Batch struct {
	Messages []Message
	Commit   func() error
}

// Fetcher 拉取「未读/新」私信；真实实现走 HTTP，测试用假实现。
type Fetcher interface {
	Fetch(ctx context.Context) (Batch, error)
}

// Options 是采集参数。
type Options struct {
	// Interval 是轮询间隔；<=0 用 2 分钟。
	Interval time.Duration
	// Now 可注入时钟（测试用）。
	Now func() time.Time
	// Log 必须非 nil。
	Log *zap.Logger
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

// New 只做静态校验，不联网。
func New(f Fetcher, o Options) (*Collector, error) {
	if f == nil {
		return nil, &Error{Kind: KindInput, Cause: errors.New("私信来源不能为 nil")}
	}
	if o.Log == nil {
		return nil, &Error{Kind: KindInput, Cause: errors.New("日志器不能为 nil")}
	}
	if o.Interval <= 0 {
		o.Interval = defaultInterval
	}
	if o.Interval < 10*time.Second || o.Interval > time.Hour {
		return nil, &Error{Kind: KindInput, Cause: errors.New("私信轮询间隔需在10秒至1小时")}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Collector{fetcher: f, opts: o, log: o.Log, seen: make(map[string]struct{})}, nil
}

// Name 与其他采集器保持一致（bus 里用于区分来源）。
func (c *Collector) Name() string { return "bili-dm" }

// Health 返回快照。
func (c *Collector) Health() collector.Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.health
}

// Start 轮询直到 ctx 取消或鉴权失效；只会有一个 Start 拥有循环。
func (c *Collector) Start(ctx context.Context, events chan<- event.Event, errs chan<- error) error {
	if ctx == nil {
		return &Error{Kind: KindInput, Cause: errors.New("上下文不能为 nil")}
	}
	if !c.started.CompareAndSwap(false, true) {
		return &Error{Kind: KindInput, Cause: errors.New("私信采集已启动")}
	}
	ticker := time.NewTicker(c.opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.poll(ctx, events); err != nil {
				c.record()
				if errors.Is(err, errAuth) {
					// 鉴权失效：停采集，别继续挨风控
					if errs != nil {
						errs <- err
					}
					return err
				}
			}
		}
	}
}

// errAuth 是内部哨兵：命中即停采集。
var errAuth = errors.New("私信采集鉴权失效")

// poll 拉一轮并产出事件。
func (c *Collector) poll(ctx context.Context, events chan<- event.Event) error {
	batch, err := c.fetcher.Fetch(ctx)
	if err != nil {
		var kinded *Error
		if errors.As(err, &kinded) && kinded.Kind == KindAuth {
			return fmt.Errorf("%w: %v", errAuth, err)
		}
		return err
	}
	now := c.opts.Now()
	// 稳定顺序：按时间升序，保证下游看到的是会话顺序
	sort.SliceStable(batch.Messages, func(i, j int) bool {
		if !batch.Messages[i].At.Equal(batch.Messages[j].At) {
			return batch.Messages[i].At.Before(batch.Messages[j].At)
		}
		return batch.Messages[i].ID < batch.Messages[j].ID
	})
	for _, m := range batch.Messages {
		ev, id, ok := c.toEvent(m, now)
		if !ok {
			continue
		}
		select {
		case events <- ev:
			// 只有事件真正进入总线后才记已见并允许推进水位。
			c.markSeen(id)
			c.recordEvent(now)
		case <-ctx.Done():
			return nil
		}
	}
	if batch.Commit != nil {
		if err := batch.Commit(); err != nil {
			return fmt.Errorf("保存私信采集水位：%w", err)
		}
	}
	return nil
}

// toEvent 校验并构造事件；返回原始消息 ID 供成功入总线后记已见。
func (c *Collector) toEvent(m Message, now time.Time) (event.Event, string, bool) {
	id := strings.TrimSpace(m.ID)
	peer := strings.TrimSpace(m.PeerID)
	text := strings.TrimSpace(m.Text)
	if id == "" || peer == "" || text == "" {
		return event.Event{}, "", false
	}
	if len([]rune(text)) > maxTextRunes {
		text = string([]rune(text)[:maxTextRunes])
	}
	if c.saw(id) {
		return event.Event{}, "", false
	}
	at := m.At
	if at.IsZero() {
		at = now
	}
	name := strings.TrimSpace(m.PeerName)
	if name == "" {
		name = peer
	}
	return event.Event{
		ID:       "dm:" + peer + ":" + id,
		Channel:  event.ChDM,
		Dir:      event.DirIn,
		Private:  true,
		Session:  peer,
		FromID:   peer,
		FromName: name,
		Text:     text,
		At:       at,
		Status:   event.StatusPending,
	}, id, true
}

func (c *Collector) saw(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.seen[id]
	return ok
}

// markSeen 记录 msg_id。超过上限时整体重建（简易有界集）。
func (c *Collector) markSeen(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) >= maxSeen {
		// map 无序，这里直接重建：宁可重看几条，也不无限增长
		c.seen = make(map[string]struct{}, maxSeen/2)
	}
	c.seen[id] = struct{}{}
}

func (c *Collector) record() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.OK = false
	c.health.LastError = "私信采集失败"
	c.health.Reconnects++
}

func (c *Collector) recordEvent(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.OK = true
	c.health.LastEventAt = now
}

// Session 是平台会话结构（解析用）。
type Session struct {
	TalkerID json.RawMessage `json:"talker_id"`
	// LastMsg 是会话里最近一条消息。
	LastMsg struct {
		MsgKey    json.RawMessage `json:"msg_key"`
		SenderUID json.RawMessage `json:"sender_uid"`
		Content   string          `json:"content"`
		Timestamp int64           `json:"timestamp"`
	} `json:"last_msg"`
}

// ParseSessions 解析 /msg/sessions 的响应体（只取「不是我发的」消息）。
//
// 用 RawMessage 兼容平台把 uid 写成字符串或数字两种形态。
func ParseSessions(body []byte, selfID string) ([]Message, error) {
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Sessions []Session `json:"session_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &Error{Kind: KindNetwork, Cause: errors.New("会话响应不是合法JSON")}
	}
	if payload.Code != 0 {
		return nil, &Error{Kind: KindAuth, Cause: fmt.Errorf("会话接口返回 code %d", payload.Code)}
	}
	self := strings.TrimSpace(selfID)
	out := make([]Message, 0, len(payload.Data.Sessions))
	for _, s := range payload.Data.Sessions {
		peer := rawID(s.TalkerID)
		msgID := rawID(s.LastMsg.MsgKey)
		sender := rawID(s.LastMsg.SenderUID)
		text := strings.TrimSpace(s.LastMsg.Content)
		if peer == "" || msgID == "" || text == "" {
			continue
		}
		if self != "" && sender == self {
			continue // 自己发的，不采集
		}
		at := time.Time{}
		if s.LastMsg.Timestamp > 0 {
			at = time.Unix(s.LastMsg.Timestamp, 0)
		}
		out = append(out, Message{ID: msgID, PeerID: peer, Text: text, At: at})
	}
	return out, nil
}

// rawID 兼容 "123" 与 123 两种 JSON 形态。
func rawID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}
