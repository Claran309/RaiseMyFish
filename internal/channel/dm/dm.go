// Package dm 把私信采集器与发送器适配成 channel.Channel。
//
// 与 comment 适配器同一套语义：Collect 异步转发到总线，Send 原样转给 sender；
// 这里不实现平台细节、去重或限频（都在 collector/dm 与 sender/dm 里）。
package dm

import (
	"context"
	"errors"
	"sync"
	"time"

	"RaiseMyFish/internal/channel"
	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// Channel 是 dm 通道适配器；sender 允许为 nil（只采集）。
type Channel struct {
	collector collector.Collector
	sender    sender.Sender
	timeout   time.Duration
	log       *zap.Logger
}

var _ channel.Channel = (*Channel)(nil)

// New 校验依赖；timeout 是总线发布超时。
func New(c collector.Collector, snd sender.Sender, timeout time.Duration, log *zap.Logger) (*Channel, error) {
	if c == nil {
		return nil, errors.New("私信采集器不能为 nil")
	}
	if timeout <= 0 {
		return nil, errors.New("总线发布超时必须为正")
	}
	if log == nil {
		return nil, errors.New("日志器不能为 nil")
	}
	return &Channel{collector: c, sender: snd, timeout: timeout, log: log}, nil
}

func (c *Channel) Name() event.Channel { return event.ChDM }

// Collect 异步启动采集转发；stop 取消并等待采集器退出，可重复调用。
func (c *Channel) Collect(ctx context.Context, bus event.Bus) (func(), error) {
	if ctx == nil {
		return nil, errors.New("采集上下文不能为 nil")
	}
	if bus == nil {
		return nil, errors.New("事件总线不能为 nil")
	}
	child, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := collector.RunToBus(child, c.collector, bus, c.timeout, c.log); err != nil {
			c.log.Error("私信采集已暂停，需处理后重启通道", zap.String("action", "collector_paused"))
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	return stop, nil
}

// Send 转给底层发送器；未装配发送器时明确报错。
func (c *Channel) Send(ctx context.Context, claim event.Claim, reply channel.Reply) (channel.Result, error) {
	if c.sender == nil {
		return channel.Result{}, errors.New("私信通道未装配发送器")
	}
	return c.sender.Send(ctx, claim, reply)
}
