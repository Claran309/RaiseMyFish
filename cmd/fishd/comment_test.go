package main

// 本文件测评论采集装配与通道注册：开关、参数投影、与 live 共存。

import (
	"testing"
	"time"

	commentcollector "RaiseMyFish/internal/collector/comment"
	livecollector "RaiseMyFish/internal/collector/live"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

func commentConfig() config.Config {
	var cfg config.Config
	cfg.Channels.Comment.Enabled = true
	cfg.Collector.Comment.Enabled = true
	cfg.Collector.Comment.Videos = []string{"123"}
	cfg.Collector.Comment.IntervalSeconds = 30
	cfg.Collector.Live.Cookie = "SESSDATA=test"
	cfg.Collector.Live.SelfID = "8"
	return cfg
}

func TestBuildCommentDisabled(t *testing.T) {
	c, err := buildComment(config.Config{}, zap.NewNop())
	if err != nil || c != nil {
		t.Fatal("关闭时不应装配评论采集")
	}
	cfg := commentConfig()
	cfg.Channels.Comment.Enabled = false
	if c, err := buildComment(cfg, zap.NewNop()); err != nil || c != nil {
		t.Fatal("通道关闭时不应装配评论采集")
	}
	cfg = commentConfig()
	cfg.Collector.Comment.Enabled = false
	if c, err := buildComment(cfg, zap.NewNop()); err != nil || c != nil {
		t.Fatal("采集关闭时不应装配评论采集")
	}
}

func TestBuildCommentEnabled(t *testing.T) {
	c, err := buildComment(commentConfig(), zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if c == nil || c.Name() != "bili-comment" {
		t.Fatalf("评论采集器不符：%v", c)
	}
}

func TestBuildCommentRejectsInvalidVideos(t *testing.T) {
	cfg := commentConfig()
	cfg.Collector.Comment.Videos = []string{"abc"}
	if _, err := buildComment(cfg, zap.NewNop()); err == nil {
		t.Fatal("非法 oid 应导致装配失败")
	}
}

func TestBuildChannelRegistryRegistersBoth(t *testing.T) {
	liveOptions := livecollector.DefaultOptions()
	liveOptions.RoomID = "30308220"
	liveOptions.SelfID = "3747574135655903"
	liveOptions.Cookie = "SESSDATA=test"
	live, err := livecollector.New(liveOptions, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	commentOptions := commentcollector.DefaultOptions()
	commentOptions.Videos = []string{"123"}
	commentOptions.SelfID = "8"
	commentOptions.Cookie = "SESSDATA=test"
	comments, err := commentcollector.New(commentOptions, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	registry, err := buildChannelRegistry(live, comments, nil, nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatalf("注册通道失败：%v", err)
	}
	if len(registry.All()) != 2 {
		t.Fatalf("通道数量 = %d", len(registry.All()))
	}
	if _, ok := registry.Get(event.ChLive); !ok {
		t.Error("缺少 live 通道")
	}
	if ch, ok := registry.Get(event.ChComment); !ok || ch.Name() != event.ChComment {
		t.Error("缺少 comment 通道")
	}
	// All 按通道名排序：comment 在 live 之前。
	if registry.All()[0].Name() != event.ChComment {
		t.Errorf("通道排序不符：%v", registry.All())
	}
}
