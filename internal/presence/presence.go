// Package presence 维护主体「此刻在做什么」的进程内状态；P0 不落库。
package presence

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	KindWatching = "watching"
	KindReplying = "replying"
	KindSending  = "sending"
	KindGift     = "gift"
	KindEditing  = "editing"
)

// DefaultTTL 是未指定 TTL 时的过期时长。
const DefaultTTL = 5 * time.Minute

// State 是主体状态（多值：一个人可以同时"在看"和"在回"）。
type State struct {
	Kind    string
	Channel string
	Detail  string
	Since   time.Time
	TTL     time.Duration
}

type key struct {
	kind    string
	channel string
}

// Table 是状态表（内存，进程内）。
type Table struct {
	mu     sync.RWMutex
	states map[key]State
}

func NewTable() *Table {
	return &Table{states: make(map[key]State)}
}

// Set 写入/更新状态；同 Kind+Channel 覆盖。Since 为零时取当前时间，TTL 非正时取 DefaultTTL。
func (t *Table) Set(s State) {
	if t == nil || s.Kind == "" {
		return
	}
	now := time.Now()
	if s.Since.IsZero() {
		s.Since = now
	}
	if s.TTL <= 0 {
		s.TTL = DefaultTTL
	}
	t.mu.Lock()
	t.states[key{kind: s.Kind, channel: s.Channel}] = s
	t.mu.Unlock()
}

func (t *Table) Clear(kind, channel string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.states, key{kind: kind, channel: channel})
	t.mu.Unlock()
}

// Active 返回未过期状态，按 Since 升序；时间相同的按 Kind、Channel 稳定排序。
func (t *Table) Active(now time.Time) []State {
	if t == nil {
		return []State{}
	}
	t.mu.RLock()
	active := make([]State, 0, len(t.states))
	for _, s := range t.states {
		if now.Before(s.Since.Add(s.TTL)) {
			active = append(active, s)
		}
	}
	t.mu.RUnlock()
	sort.Slice(active, func(i, j int) bool {
		if !active[i].Since.Equal(active[j].Since) {
			return active[i].Since.Before(active[j].Since)
		}
		if active[i].Kind != active[j].Kind {
			return active[i].Kind < active[j].Kind
		}
		return active[i].Channel < active[j].Channel
	})
	return active
}

var labels = map[string]string{
	KindWatching: "在看直播",
	KindReplying: "在回弹幕",
	KindSending:  "在发弹幕",
	KindGift:     "在送礼",
	KindEditing:  "在剪视频",
}

// Summary 生成一句话摘要，例如「在看直播 30308220 / 在回弹幕」；空表返回空字符串。
func (t *Table) Summary(now time.Time) string {
	active := t.Active(now)
	parts := make([]string, 0, len(active))
	seen := make(map[string]struct{}, len(active))
	for _, s := range active {
		label, ok := labels[s.Kind]
		if !ok {
			label = s.Kind
		}
		if s.Detail != "" {
			label += " " + s.Detail
		}
		if _, dup := seen[label]; dup {
			continue
		}
		seen[label] = struct{}{}
		parts = append(parts, label)
	}
	return strings.Join(parts, " / ")
}
