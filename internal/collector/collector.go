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

// DeliveryAck 是桥对一条事件的持久化回执。
//
// Err 为 nil 表示 bus.Publish 已成功（含幂等重复）；否则该事件没有落库。
type DeliveryAck struct {
	EventID string
	Err     error
}

// AckCollector 是「持久化确认后才推进水位」的采集器可选契约。
//
// 实现者仍满足 Collector；RunToBus 会优先调用 StartAck，并在每条事件
// Publish 完成之后回执。采集器必须收到成功回执才能提交水位，并且要在
// 发送下一条事件之前读取回执（回执通道不提供缓存）。
// 事件被 channel 接收不等于已落库。
type AckCollector interface {
	Collector
	StartAck(ctx context.Context, events chan<- event.Event, acks <-chan DeliveryAck, errs chan<- error) error
}
