package main

import (
	"fmt"
	"time"

	"RaiseMyFish/internal/channel"
	channelcomment "RaiseMyFish/internal/channel/comment"
	channeldm "RaiseMyFish/internal/channel/dm"
	channellive "RaiseMyFish/internal/channel/live"
	"RaiseMyFish/internal/collector"
	dmcollector "RaiseMyFish/internal/collector/dm"
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
	return newLiveCollector(cfg, log)
}

// newLiveCollector 忽略静态启用开关，供面板运行时启停复用。
func newLiveCollector(cfg config.Config, log *zap.Logger) (*livecollector.Collector, error) {
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

// buildChannelRegistry 注册当前启用的通道；两个采集器都为 nil 时返回空注册表。
//
// snd 允许为 nil（只采集、未装配消费循环），此时通道的 Send 会明确报错。
func buildChannelRegistry(live *livecollector.Collector, comments collector.Collector, dms *dmcollector.Collector, snd sender.Sender, timeout time.Duration, log *zap.Logger) (*channel.Registry, error) {
	registry := channel.NewRegistry()
	if live != nil {
		ch, err := channellive.New(live, snd, timeout, log)
		if err != nil {
			return nil, fmt.Errorf("装配 live 通道失败：%w", err)
		}
		if err := registry.Register(ch); err != nil {
			return nil, fmt.Errorf("注册 live 通道失败：%w", err)
		}
	}
	if comments != nil {
		ch, err := channelcomment.New(comments, snd, timeout, log)
		if err != nil {
			return nil, fmt.Errorf("装配 comment 通道失败：%w", err)
		}
		if err := registry.Register(ch); err != nil {
			return nil, fmt.Errorf("注册 comment 通道失败：%w", err)
		}
	}
	if dms != nil {
		ch, err := channeldm.New(dms, snd, timeout, log)
		if err != nil {
			return nil, fmt.Errorf("装配 dm 通道失败：%w", err)
		}
		if err := registry.Register(ch); err != nil {
			return nil, fmt.Errorf("注册 dm 通道失败：%w", err)
		}
	}
	return registry, nil
}
