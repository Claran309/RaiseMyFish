package main

import (
	"context"
	"time"

	"RaiseMyFish/internal/collector"
	livecollector "RaiseMyFish/internal/collector/live"
	"RaiseMyFish/internal/config"
	"go.uber.org/zap"
)

type liveDiagnostic struct {
	Enabled           bool `json:"enabled"`
	Configured        bool `json:"configured"`
	ProcessStarted    bool `json:"process_started"`
	PipelineConnected bool `json:"pipeline_connected"`
}

func buildLive(cfg config.Config, log *zap.Logger) (*livecollector.Collector, error) {
	if !cfg.Channels.Live.Enabled {
		return nil, nil
	}
	l := cfg.Collector.Live
	o := livecollector.DefaultOptions()
	o.RoomID = l.RoomID
	o.SelfID = l.SelfID
	o.NodePath = l.NodePath
	o.ScriptPath = l.ScriptPath
	o.Cookie = l.Cookie
	o.CookieFile = l.CookieFile
	o.HTTPTimeout = time.Duration(l.HTTPTimeoutMS) * time.Millisecond
	o.ConnectTimeout = time.Duration(l.ConnectTimeoutMS) * time.Millisecond
	o.Heartbeat = time.Duration(l.HeartbeatMS) * time.Millisecond
	o.IdleTimeout = time.Duration(l.IdleMS) * time.Millisecond
	o.StartupTimeout = time.Duration(l.StartupMS) * time.Millisecond
	o.BackoffMin = time.Duration(l.BackoffMinMS) * time.Millisecond
	o.BackoffMax = time.Duration(l.BackoffMaxMS) * time.Millisecond
	o.StableAfter = time.Duration(l.StableAfterMS) * time.Millisecond
	o.MaxFailures = l.MaxFailures
	return livecollector.New(o, log)
}

// serveWithCollector单通道暂停不结束基础设施；取消基础设施后等待采集子进程退出。
func serveWithCollector(ctx context.Context, c collector.Collector, bus collector.Publisher, timeout time.Duration, log *zap.Logger, serve func(context.Context) error) error {
	child, cancel := context.WithCancel(ctx)
	finished := make(chan struct{}, 1)
	go func() {
		defer func() { finished <- struct{}{} }()
		if err := collector.RunToBus(child, c, bus, timeout, log); err != nil {
			log.Error("直播采集已暂停，需处理后重启通道", zap.String("action", "collector_paused"))
		}
	}()
	defer func() { cancel(); <-finished }()
	log.Info("已装配直播采集到事件总线，未启动模型消费或发送", zap.String("action", "collector_bus_start"))
	return serve(ctx)
}
