package main

import (
	"testing"
	"time"

	livecollector "RaiseMyFish/internal/collector/live"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

func TestBuildLiveDisabledDoesNotStartNode(t *testing.T) {
	var cfg config.Config
	c, err := buildLive(cfg, zap.NewNop())
	if err != nil || c != nil {
		t.Fatal("关闭还装配采集")
	}
}

func TestBuildChannelRegistryWithoutLive(t *testing.T) {
	registry, err := buildChannelRegistry(nil, nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatalf("空注册表装配失败：%v", err)
	}
	if len(registry.All()) != 0 {
		t.Fatal("未启用 live 时不应注册通道")
	}
	if _, ok := registry.Get(event.ChLive); ok {
		t.Fatal("未启用 live 时不应能查到通道")
	}
}

func TestBuildChannelRegistryRegistersLive(t *testing.T) {
	// 只做静态校验，不读取凭据文件、不执行 Node 子进程。
	o := livecollector.DefaultOptions()
	o.RoomID = "30308220"
	o.SelfID = "3747574135655903"
	o.Cookie = "SESSDATA=test"
	c, err := livecollector.New(o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := buildChannelRegistry(c, nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatalf("注册 live 通道失败：%v", err)
	}
	ch, ok := registry.Get(event.ChLive)
	if !ok || ch.Name() != event.ChLive {
		t.Fatalf("live 通道未按名字注册：ok=%v ch=%v", ok, ch)
	}
	if len(registry.All()) != 1 {
		t.Fatalf("通道数量不符：%d", len(registry.All()))
	}
}
