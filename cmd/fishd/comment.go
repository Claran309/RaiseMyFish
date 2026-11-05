package main

// 评论通道装配：采集开关在 [collector.comment]，发送开关在 [sender.comment]，
// 两者共用 [channels.comment] 与同一账号的 B站凭据。

import (
	"fmt"
	"time"

	"RaiseMyFish/internal/bili"
	commentcollector "RaiseMyFish/internal/collector/comment"
	"RaiseMyFish/internal/config"
	"go.uber.org/zap"
)

func buildComment(cfg config.Config, log *zap.Logger) (*commentcollector.Collector, error) {
	if !cfg.Channels.Comment.Enabled || !cfg.Collector.Comment.Enabled {
		return nil, nil
	}
	return newCommentCollector(cfg, cfg.Collector.Comment.Videos, log)
}

// newCommentCollector 用给定视频列表装配，供面板热更新（忽略静态启用开关）。
func newCommentCollector(cfg config.Config, videos []string, log *zap.Logger) (*commentcollector.Collector, error) {
	o := commentcollector.DefaultOptions()
	o.Videos = videos
	o.Interval = time.Duration(cfg.Collector.Comment.IntervalSeconds) * time.Second
	o.Cookie = cfg.Collector.Live.Cookie
	o.CookieFile = cfg.Collector.Live.CookieFile
	// 配置层已保证评论采集启用时 self_id 合法；这里只做同名回落。
	o.SelfID = selfMID(cfg)
	o.AutoDiscover = cfg.Collector.Comment.AutoDiscover
	if o.AutoDiscover {
		o.DiscoverInterval = time.Duration(cfg.Collector.Comment.DiscoverIntervalSeconds) * time.Second
		discoverer, err := newCommentDiscoverer(cfg, log)
		if err != nil {
			return nil, fmt.Errorf("装配评论自动发现失败：%w", err)
		}
		o.Discoverer = discoverer
	}
	collector, err := commentcollector.New(o, log)
	if err != nil {
		return nil, fmt.Errorf("装配评论采集失败：%w", err)
	}
	return collector, nil
}

// newCommentDiscoverer 复用直播采集解析到的同一账号凭据与动态 Resolver。
func newCommentDiscoverer(cfg config.Config, log *zap.Logger) (commentcollector.Discoverer, error) {
	timeout := 10 * time.Second
	if cfg.Sender != nil && cfg.Sender.HTTPTimeoutMS > 0 {
		timeout = time.Duration(cfg.Sender.HTTPTimeoutMS) * time.Millisecond
	}
	client, err := bili.New(bili.Options{
		Cookie:     cfg.Collector.Live.Cookie,
		CookieFile: cfg.Collector.Live.CookieFile,
		Resolver:   cfg.BiliResolver,
		SelfMID:    selfMID(cfg),
		Timeout:    timeout,
	})
	if err != nil {
		return nil, err
	}
	log.Info("评论自动发现已装配",
		zap.String("action", "comment_discover_setup"),
		zap.Duration("interval", time.Duration(cfg.Collector.Comment.DiscoverIntervalSeconds)*time.Second),
	)
	return commentcollector.BiliDiscoverer{Client: client, MID: selfMID(cfg), VideoLimit: 50, DynamicLimit: 20}, nil
}
