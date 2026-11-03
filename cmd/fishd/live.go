package main

import (
	"fmt"
	"time"

	"RaiseMyFish/internal/channel"
	channellive "RaiseMyFish/internal/channel/live"
	livecollector "RaiseMyFish/internal/collector/live"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/sender"
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

// buildChannelRegistry 注册当前启用的通道；live 未启用时返回空注册表。
//
// snd 允许为 nil（只采集、未装配消费循环），此时 live 通道的 Send 会明确报错。
func buildChannelRegistry(live *livecollector.Collector, snd sender.Sender, timeout time.Duration, log *zap.Logger) (*channel.Registry, error) {
	registry := channel.NewRegistry()
	if live == nil {
		return registry, nil
	}
	ch, err := channellive.New(live, snd, timeout, log)
	if err != nil {
		return nil, fmt.Errorf("装配 live 通道失败：%w", err)
	}
	if err := registry.Register(ch); err != nil {
		return nil, fmt.Errorf("注册 live 通道失败：%w", err)
	}
	return registry, nil
}
