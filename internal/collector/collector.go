// Package collector 定义现有采集契约及到事件总线的有界转发。
package collector

import (
	"RaiseMyFish/internal/event"
	"context"
	"time"
)

// Collector不访问数据库或模型；Start不关闭调用方持有的channel。
type Collector interface {
	Name() string
	Start(context.Context, chan<- event.Event, chan<- error) error
	Health() Health
}

// Health为快照；LastEventAt是最近产出事件的接收时间，而非平台At。
type Health struct {
	OK          bool
	LastEventAt time.Time
	LastError   string
	Reconnects  int
}

// Publisher是桥接所需的最小接口，已有event.Bus可直接注入。
type Publisher interface {
	Publish(context.Context, event.Event) (bool, error)
}
