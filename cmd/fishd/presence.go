package main

// 本文件是 0.13a 的 presence 接线：只旁路观测，不改变调度/发送语义。

import (
	"context"

	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/presence"
)

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
