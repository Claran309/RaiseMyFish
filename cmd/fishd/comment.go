package main

// 评论通道装配：采集开关在 [collector.comment]，发送开关在 [sender.comment]，
// 两者共用 [channels.comment] 与同一账号的 B站凭据。

import (
	"fmt"
	"time"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/collector"
	commentcollector "RaiseMyFish/internal/collector/comment"
	"RaiseMyFish/internal/collector/msgfeed"
	"RaiseMyFish/internal/config"
	"go.uber.org/zap"
)

// dynamicCommentBackfillWindow 是动态评论首轮补采窗口。
//
// 动态评论没有持久水位，而首次接入时需要补上近期漏采的评论（如主人当天 13:43 那条）；
// 只回刷最近 24 小时，避免把动态下的历史评论一次性全回一遍。
const dynamicCommentBackfillWindow = 24 * time.Hour

// buildComment 按静态配置装配评论采集源：默认消息监听，可切回视频轮询。
func buildComment(cfg config.Config, accounts *account.Service, log *zap.Logger) (collector.Collector, error) {
	if !cfg.Channels.Comment.Enabled || !cfg.Collector.Comment.Enabled {
		return nil, nil
	}
	return newCommentSource(cfg, cfg.Collector.Comment.Videos, accounts, log)
}

// newCommentSource 按 feed_enabled 选择来源；忽略静态启用开关，供面板热更新。
//
// feed 模式默认再并行一个动态评论轮询：msgfeed 不含「评论我的动态」，
// 只靠消息监听会漏掉动态下的新评论。
func newCommentSource(cfg config.Config, videos []string, accounts *account.Service, log *zap.Logger) (collector.Collector, error) {
	if !cfg.Collector.Comment.FeedEnabled {
		return newCommentCollector(cfg, videos, log)
	}
	feed, err := newCommentFeed(cfg, accounts, log)
	if err != nil {
		return nil, err
	}
	if !cfg.Collector.Comment.DynamicPollEnabled {
		return feed, nil
	}
	dynamics, err := newDynamicCommentCollector(cfg, log)
	if err != nil {
		// 动态轮询是补采来源：装配失败不能连累消息监听。
		log.Warn("动态评论轮询装配失败，仅启用消息监听",
			zap.String("action", "comment_dynamic_setup"),
			zap.Error(err),
		)
		return feed, nil
	}
	group, err := collector.NewGroup(feed, dynamics)
	if err != nil {
		return nil, fmt.Errorf("组合评论采集来源失败：%w", err)
	}
	group.WithLogger(log)
	log.Info("评论采集已装配：消息监听 + 动态评论轮询",
		zap.String("action", "comment_dynamic_setup"),
		zap.Duration("dynamic_interval", time.Duration(cfg.Collector.Comment.IntervalSeconds)*time.Second),
		zap.Duration("dynamic_discover_interval", time.Duration(cfg.Collector.Comment.DiscoverIntervalSeconds)*time.Second),
	)
	return group, nil
}

// newCommentFeed 装配账号消息监听：覆盖回复我的与@我的，账号切换由账号服务冻结整轮。
func newCommentFeed(cfg config.Config, accounts *account.Service, log *zap.Logger) (collector.Collector, error) {
	client, err := newCommentSpaceClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("装配消息监听客户端失败：%w", err)
	}
	cursors, err := msgfeed.NewFileCursorStore(cfg.Collector.Comment.FeedStatePath)
	if err != nil {
		return nil, fmt.Errorf("装配消息监听水位失败：%w", err)
	}
	// typed-nil 指针装进接口会变成「非 nil 来源」，这里按需赋值。
	var actors msgfeed.ActorSource
	var admission msgfeed.Admission
	if accounts != nil {
		actors = accounts
		admission = accounts
	}
	source := &msgfeed.BiliSource{
		Client:    client,
		Actors:    actors,
		Admission: admission,
		SelfID:    selfMID(cfg),
		Cursors:   cursors,
		Log:       log,
	}
	interval := time.Duration(cfg.Collector.Comment.FeedIntervalSeconds) * time.Second
	collector, err := msgfeed.New(source, msgfeed.Options{Interval: interval, Log: log})
	if err != nil {
		return nil, fmt.Errorf("装配消息监听失败：%w", err)
	}
	log.Info("消息监听已装配",
		zap.String("action", "msgfeed_setup"),
		zap.Duration("interval", interval),
		zap.Bool("account_switch", accounts != nil),
	)
	return collector, nil
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

// newCommentSpaceClient 复用直播采集解析到的同一账号凭据与动态 Resolver。
func newCommentSpaceClient(cfg config.Config) (*bili.Client, error) {
	timeout := 10 * time.Second
	if cfg.Sender != nil && cfg.Sender.HTTPTimeoutMS > 0 {
		timeout = time.Duration(cfg.Sender.HTTPTimeoutMS) * time.Millisecond
	}
	return bili.New(bili.Options{
		Cookie:     cfg.Collector.Live.Cookie,
		CookieFile: cfg.Collector.Live.CookieFile,
		Resolver:   cfg.BiliResolver,
		SelfMID:    selfMID(cfg),
		Timeout:    timeout,
	})
}

// newCommentDiscoverer 为视频轮询装配自动发现；关闭动态轮询时只拉视频。
func newCommentDiscoverer(cfg config.Config, log *zap.Logger) (commentcollector.Discoverer, error) {
	client, err := newCommentSpaceClient(cfg)
	if err != nil {
		return nil, err
	}
	log.Info("评论自动发现已装配",
		zap.String("action", "comment_discover_setup"),
		zap.Duration("interval", time.Duration(cfg.Collector.Comment.DiscoverIntervalSeconds)*time.Second),
		zap.Bool("dynamics", cfg.Collector.Comment.DynamicPollEnabled),
	)
	return commentcollector.BiliDiscoverer{
		Client:       client,
		MID:          selfMID(cfg),
		VideoLimit:   50,
		DynamicLimit: 20,
		SkipDynamics: !cfg.Collector.Comment.DynamicPollEnabled,
	}, nil
}

// newDynamicCommentCollector 装配只轮询动态评论区的采集器（feed 模式并行来源）。
func newDynamicCommentCollector(cfg config.Config, log *zap.Logger) (*commentcollector.Collector, error) {
	client, err := newCommentSpaceClient(cfg)
	if err != nil {
		return nil, err
	}
	o := commentcollector.DefaultOptions()
	o.Interval = time.Duration(cfg.Collector.Comment.IntervalSeconds) * time.Second
	o.Cookie = cfg.Collector.Live.Cookie
	o.CookieFile = cfg.Collector.Live.CookieFile
	o.SelfID = selfMID(cfg)
	// 动态没有手动 oid 列表，必须靠发现；dynamic_poll_enabled 本身就是开关。
	o.AutoDiscover = true
	o.DiscoverInterval = time.Duration(cfg.Collector.Comment.DiscoverIntervalSeconds) * time.Second
	o.Discoverer = commentcollector.DynamicDiscoverer{
		Client: client,
		MID:    selfMID(cfg),
		Limit:  20,
	}
	o.RecentSince = time.Now().Add(-dynamicCommentBackfillWindow)
	collector, err := commentcollector.New(o, log)
	if err != nil {
		return nil, fmt.Errorf("装配动态评论采集失败：%w", err)
	}
	return collector, nil
}
