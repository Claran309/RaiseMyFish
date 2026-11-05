package main

// 本文件是 0.13a 的 presence 接线：只旁路观测，不改变调度/发送语义。

import (
	"context"
	"strings"
	"sync"

	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/presence"
)

// liveRoomState 记录当前 live 采集房间；空串表示没有在采集。
// 它是状态注入的唯一权威来源，避免 Presence 的 TTL 残留或 [watch] 旧配置串台。
type liveRoomState struct {
	mu   sync.RWMutex
	room string
}

func (s *liveRoomState) Set(room string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.room = strings.TrimSpace(room)
	s.mu.Unlock()
}

func (s *liveRoomState) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.room = ""
	s.mu.Unlock()
}

func (s *liveRoomState) Current() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.room
}

// resetWatching 清掉上一个采集房间留下的 watching，防止 TTL 内继续被当成「正在看」。
func resetWatching(table *presence.Table) {
	if table != nil {
		table.Clear(presence.KindWatching, string(event.ChLive))
	}
}

// presencePublisher 在采集事件转发前记录「正在看直播」。
type presencePublisher struct {
	inner collector.Publisher
	table *presence.Table
}

func withPresence(inner collector.Publisher, table *presence.Table) collector.Publisher {
	if inner == nil || table == nil {
		return inner
	}
	return presencePublisher{inner: inner, table: table}
}

func (p presencePublisher) Publish(ctx context.Context, ev event.Event) (bool, error) {
	// Detail 用房间号，摘要即「在看直播 30308220」
	if ev.Channel == event.ChLive {
		p.table.Set(presence.State{
			Kind:    presence.KindWatching,
			Channel: string(ev.Channel),
			Detail:  ev.Session,
			TTL:     presence.DefaultTTL,
		})
	}
	return p.inner.Publish(ctx, ev)
}

// presenceBus 在完整事件总线上覆写 Publish，其余方法原样透传。
type presenceBus struct {
	event.Bus
	inner collector.Publisher
}

func (p presenceBus) Publish(ctx context.Context, ev event.Event) (bool, error) {
	return p.inner.Publish(ctx, ev)
}

// withPresenceBus 让 channel.Collect 在写入总线前记录 watching；nil 参数原样返回。
func withPresenceBus(bus event.Bus, table *presence.Table) event.Bus {
	if bus == nil || table == nil {
		return bus
	}
	return presenceBus{Bus: bus, inner: withPresence(bus, table)}
}
