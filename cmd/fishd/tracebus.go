package main

import (
	"context"
	"fmt"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/trace"
	"go.uber.org/zap"
)

// tracedBus 只包住 Publish 的 collect span；其余 Bus 方法原样透传。
type tracedBus struct {
	event.Bus
	tracer *trace.Local
}

func withTraceBus(bus event.Bus, tracer *trace.Local) event.Bus {
	if tracer == nil {
		return bus
	}
	return tracedBus{Bus: bus, tracer: tracer}
}

func (b tracedBus) Publish(ctx context.Context, ev event.Event) (bool, error) {
	ctx, span := b.tracer.StartEvent(ctx, ev.ID, "collect",
		trace.A("channel", string(ev.Channel)),
		trace.A("collect_mode", ev.CollectMode),
	)
	ok, err := b.Bus.Publish(ctx, ev)
	trace.Add(ctx, trace.A("inserted", ok))
	trace.End(span, err)
	return ok, err
}

// newTracer 按配置创建本地 trace；关闭时返回 nil，所有埋点自动 no-op。
func newTracer(cfg config.Config, log *zap.Logger) (*trace.Local, error) {
	if !cfg.Observability.Trace.Enabled {
		return nil, nil
	}
	tracer, err := trace.NewLocal(cfg.Observability.Trace.MaxSpans, cfg.Observability.Trace.Path)
	if err != nil {
		return nil, fmt.Errorf("装配本地trace失败：%w", err)
	}
	if endpoint := cfg.Observability.Trace.OTLPEndpoint; endpoint != "" {
		exporter, err := trace.NewOTLPExporter(
			endpoint,
			cfg.Observability.Trace.ServiceName,
			time.Duration(cfg.Observability.Trace.OTLPTimeoutMS)*time.Millisecond,
		)
		if err != nil {
			return nil, fmt.Errorf("装配OTLP导出失败：%w", err)
		}
		tracer.SetExporter(exporter)
		log.Info("OTLP trace 导出已启用", zap.String("action", "trace_export"), zap.String("endpoint", endpoint))
	}
	log.Info("本地trace已启用", zap.String("action", "trace_setup"), zap.String("path", cfg.Observability.Trace.Path), zap.Int("max_spans", cfg.Observability.Trace.MaxSpans))
	return tracer, nil
}
