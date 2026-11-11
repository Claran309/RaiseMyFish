package main

// 本文件测评论采集装配与通道注册：开关、参数投影、与 live 共存。

import (
	"testing"
	"time"

	"RaiseMyFish/internal/collector"
	commentcollector "RaiseMyFish/internal/collector/comment"
	livecollector "RaiseMyFish/internal/collector/live"
	"RaiseMyFish/internal/collector/msgfeed"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

func commentConfig() config.Config {
	var cfg config.Config
	cfg.Channels.Comment.Enabled = true
	cfg.Collector.Comment.Enabled = true
	// 本文件测视频轮询装配；消息监听由 feedConfig 单独覆盖。
	cfg.Collector.Comment.FeedEnabled = false
	cfg.Collector.Comment.Videos = []string{"123"}
	cfg.Collector.Comment.IntervalSeconds = 30
	cfg.Collector.Live.Cookie = "SESSDATA=test"
	cfg.Collector.Live.SelfID = "8"
	return cfg
}

func feedConfig() config.Config {
	var cfg config.Config
	cfg.Channels.Comment.Enabled = true
	cfg.Collector.Comment.Enabled = true
	cfg.Collector.Comment.FeedEnabled = true
	cfg.Collector.Comment.FeedIntervalSeconds = 60
	cfg.Collector.Live.Cookie = "SESSDATA=test"
	cfg.Collector.Live.SelfID = "8"
	return cfg
}

func TestBuildCommentDisabled(t *testing.T) {
	c, err := buildComment(config.Config{}, nil, zap.NewNop())
	if err != nil || c != nil {
		t.Fatal("关闭时不应装配评论采集")
	}
	cfg := commentConfig()
	cfg.Channels.Comment.Enabled = false
	if c, err := buildComment(cfg, nil, zap.NewNop()); err != nil || c != nil {
		t.Fatal("通道关闭时不应装配评论采集")
	}
	cfg = commentConfig()
	cfg.Collector.Comment.Enabled = false
	if c, err := buildComment(cfg, nil, zap.NewNop()); err != nil || c != nil {
		t.Fatal("采集关闭时不应装配评论采集")
	}
}

func TestBuildCommentEnabled(t *testing.T) {
	c, err := buildComment(commentConfig(), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if c == nil || c.Name() != "bili-comment" {
		t.Fatalf("评论采集器不符：%v", c)
	}
}

func TestBuildCommentFeedEnabled(t *testing.T) {
	cfg := feedConfig()
	cfg.Collector.Comment.FeedStatePath = t.TempDir() + "/feed.json"
	c, err := buildComment(cfg, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("消息监听装配失败：%v", err)
	}
	if c == nil || c.Name() != "bili-comment" {
		t.Fatalf("消息监听采集器不符：%v", c)
	}
	if _, ok := c.(*msgfeed.Collector); !ok {
		t.Fatalf("feed_enabled=true 应装配消息监听，实际 %T", c)
	}
}

func TestBuildCommentFeedRejectsBadStatePath(t *testing.T) {
	cfg := feedConfig()
	cfg.Collector.Comment.FeedStatePath = "state\x00bad.json"
	if _, err := buildComment(cfg, nil, zap.NewNop()); err == nil {
		t.Fatal("非法水位路径应导致装配失败")
	}
}

func TestNewCommentSourceFollowsMode(t *testing.T) {
	poll, err := newCommentSource(commentConfig(), []string{"123"}, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("轮询装配失败：%v", err)
	}
	if _, ok := poll.(*commentcollector.Collector); !ok {
		t.Fatalf("feed_enabled=false 应装配视频轮询，实际 %T", poll)
	}
	feed := feedConfig()
	feed.Collector.Comment.FeedStatePath = t.TempDir() + "/feed.json"
	got, err := newCommentSource(feed, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("消息监听装配失败：%v", err)
	}
	if _, ok := got.(*msgfeed.Collector); !ok {
		t.Fatalf("feed_enabled=true 应装配消息监听，实际 %T", got)
	}
}

// TestNewCommentSourceFeedWithDynamicPoll feed 模式开启动态轮询时组合两个来源。
func TestNewCommentSourceFeedWithDynamicPoll(t *testing.T) {
	cfg := feedConfig()
	cfg.Collector.Comment.FeedStatePath = t.TempDir() + "/feed.json"
	cfg.Collector.Comment.DynamicPollEnabled = true
	cfg.Collector.Comment.IntervalSeconds = 60
	cfg.Collector.Comment.DiscoverIntervalSeconds = 600
	got, err := newCommentSource(cfg, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("组合装配失败：%v", err)
	}
	if _, ok := got.(*collector.Group); !ok {
		t.Fatalf("feed+动态轮询应装配组合采集器，实际 %T", got)
	}
	if got.Name() != "group" {
		t.Errorf("组合采集器名称 = %q", got.Name())
	}
}

func TestBuildCommentRejectsInvalidVideos(t *testing.T) {
	cfg := commentConfig()
	cfg.Collector.Comment.Videos = []string{"abc"}
	if _, err := buildComment(cfg, nil, zap.NewNop()); err == nil {
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

// TestBuildChannelRegistryWithFeedCollector 消息监听实例也要能注册成 comment 通道。
func TestBuildChannelRegistryWithFeedCollector(t *testing.T) {
	cfg := feedConfig()
	cfg.Collector.Comment.FeedStatePath = t.TempDir() + "/feed.json"
	feed, err := buildComment(cfg, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("消息监听装配失败：%v", err)
	}
	registry, err := buildChannelRegistry(nil, feed, nil, nil, time.Second, zap.NewNop())
	if err != nil {
		t.Fatalf("注册通道失败：%v", err)
	}
	ch, ok := registry.Get(event.ChComment)
	if !ok || ch.Name() != event.ChComment {
		t.Fatalf("缺少 comment 通道：%v", registry.All())
	}
}
