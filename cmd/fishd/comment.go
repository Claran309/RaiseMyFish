package main

// 评论通道装配：采集开关在 [collector.comment]，发送开关在 [sender.comment]，
// 两者共用 [channels.comment] 与同一账号的 B站凭据。

import (
	"fmt"
	"time"

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
	collector, err := commentcollector.New(o, log)
	if err != nil {
		return nil, fmt.Errorf("装配评论采集失败：%w", err)
	}
	return collector, nil
}
