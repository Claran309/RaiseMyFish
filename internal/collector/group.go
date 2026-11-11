package collector

// 本文件实现多来源组合采集器（如消息监听 + 动态评论轮询）。
//
// 关键约束：RunToBus 对 AckCollector 只提供一个落库回执通道，组合器必须把
// 每条回执转给真正产出该事件的子采集器，否则消息监听的水位等不到落库确认。
// 组合器因此串行放行事件：发出一条、等到回执、再放下一条。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

// Group 把多个来源合成一个 Collector。
type Group struct {
	children []Collector
	log      *zap.Logger
	started  atomic.Bool
}

var _ Collector = (*Group)(nil)
var _ AckCollector = (*Group)(nil)

// NewGroup 校验来源列表；子采集器为空或 nil 直接拒绝。
func NewGroup(children ...Collector) (*Group, error) {
	if len(children) == 0 {
		return nil, errors.New("组合采集器至少需要一个来源")
	}
	for _, child := range children {
		if nilValue(child) {
			return nil, errors.New("组合采集器来源不能为空")
		}
	}
	return &Group{children: append([]Collector(nil), children...), log: zap.NewNop()}, nil
}

// WithLogger 注入诊断日志；必须在 Start 前调用。
func (g *Group) WithLogger(log *zap.Logger) *Group {
	if log != nil {
		g.log = log
	}
	return g
}

func (g *Group) Name() string {
	if len(g.children) == 1 {
		return g.children[0].Name()
	}
	return "group"
}

// Health 聚合子采集器：任一不健康即整体不健康，事件时间取最近，重连数求和。
func (g *Group) Health() Health {
	var out Health
	out.OK = len(g.children) > 0
	for _, child := range g.children {
		health := child.Health()
		if !health.OK {
			out.OK = false
		}
		if health.LastEventAt.After(out.LastEventAt) {
			out.LastEventAt = health.LastEventAt
		}
		if out.LastError == "" && health.LastError != "" {
			out.LastError = health.LastError
		}
		out.Reconnects += health.Reconnects
	}
	return out
}

type groupChild struct {
	collector Collector
	events    chan event.Event
	errs      chan error
	// acks 只在子采集器实现 AckCollector 且上游提供回执通道时创建。
	acks chan DeliveryAck
}

type groupEvent struct {
	source int
	event  event.Event
}

type groupResult struct {
	name string
	err  error
}

// Start 是直连模式：子采集器按旧语义自行提交水位，组合器不等待回执。
func (g *Group) Start(ctx context.Context, events chan<- event.Event, errs chan<- error) error {
	return g.run(ctx, events, nil, errs)
}

// StartAck 实现 AckCollector：每条事件等到上游落库回执后，再转给产出它的子采集器。
func (g *Group) StartAck(ctx context.Context, events chan<- event.Event, acks <-chan DeliveryAck, errs chan<- error) error {
	return g.run(ctx, events, acks, errs)
}

func (g *Group) run(ctx context.Context, events chan<- event.Event, bridgeAcks <-chan DeliveryAck, errs chan<- error) error {
	if ctx == nil || events == nil || errs == nil {
		return errors.New("组合采集器参数无效")
	}
	if !g.started.CompareAndSwap(false, true) {
		return errors.New("组合采集器已启动")
	}
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	children := make([]*groupChild, len(g.children))
	results := make(chan groupResult, len(g.children))
	merged := make(chan groupEvent)
	for i, child := range g.children {
		item := &groupChild{
			collector: child,
			events:    make(chan event.Event),
			errs:      make(chan error),
		}
		children[i] = item
		if _, ok := child.(AckCollector); ok && bridgeAcks != nil {
			item.acks = make(chan DeliveryAck)
		}
		wg.Add(3)
		go g.forwardEvents(runCtx, i, item, merged, &wg)
		go g.forwardErrors(runCtx, item, errs, &wg)
		go g.runChild(runCtx, item, results, &wg)
	}

	for {
		select {
		case <-runCtx.Done():
			return nil
		case result := <-results:
			if runCtx.Err() != nil {
				return nil
			}
			if result.err != nil {
				return fmt.Errorf("组合采集源 %s 已退出：%w", result.name, result.err)
			}
			return fmt.Errorf("组合采集源 %s 提前退出", result.name)
		case item := <-merged:
			select {
			case events <- item.event:
			case <-runCtx.Done():
				return nil
			}
			if bridgeAcks == nil {
				continue
			}
			var ack DeliveryAck
			select {
			case ack = <-bridgeAcks:
			case <-runCtx.Done():
				return nil
			}
			if ack.EventID != item.event.ID {
				return fmt.Errorf("组合采集确认错位：期望 %s，实际 %s", item.event.ID, ack.EventID)
			}
			if target := children[item.source]; target.acks != nil {
				select {
				case target.acks <- ack:
				case <-runCtx.Done():
					return nil
				}
			}
		}
	}
}

func (g *Group) forwardEvents(ctx context.Context, index int, child *groupChild, merged chan<- groupEvent, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-child.events:
			select {
			case merged <- groupEvent{source: index, event: ev}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (g *Group) forwardErrors(ctx context.Context, child *groupChild, errs chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-child.errs:
			// 桥的 collector_error 不区分来源，这里补一条带来源名的日志便于定位。
			if err != nil {
				g.log.Warn("组合采集子来源报告异常",
					zap.String("action", "collector_source_error"),
					zap.String("source", child.collector.Name()),
					zap.Error(err),
				)
			}
			select {
			case errs <- err:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (g *Group) runChild(ctx context.Context, child *groupChild, results chan<- groupResult, wg *sync.WaitGroup) {
	defer wg.Done()
	var err error
	if child.acks != nil {
		err = child.collector.(AckCollector).StartAck(ctx, child.events, child.acks, child.errs)
	} else {
		err = child.collector.Start(ctx, child.events, child.errs)
	}
	select {
	case results <- groupResult{name: child.collector.Name(), err: err}:
	case <-ctx.Done():
	}
}
