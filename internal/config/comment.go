package config

// 本文件定义评论通道的采集与发送配置（P1 评论线）。
//
// 采集与发送各有独立开关：只采集不回复、或只回复不采集都允许；
// 但两者都依赖 [channels.comment] 这条通道开关与同一套 B站凭据。

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// CommentCollector 是评论采集配置：
//   - FeedEnabled=true（默认）：监听账号消息（回复我的、@我的），覆盖别人视频下的 @；
//   - DynamicPollEnabled=true（默认）：轮询自己动态的评论区，补 msgfeed 不含的「评论我的动态」；
//     消息监听开启时两者并行，关闭时沿用旧的视频/动态轮询（AutoDiscover + Videos）；
//   - FeedEnabled=false：视频/动态轮询模式，DynamicPollEnabled 决定自动发现是否包含动态。
//
// 消息监听是主用来源；轮询代码不删，改 feed_enabled=false 即可切回/备用。
type CommentCollector struct {
	Enabled bool `toml:"enabled"`
	// FeedEnabled 切换消息监听；关闭后走视频轮询。
	FeedEnabled bool `toml:"feed_enabled"`
	// FeedIntervalSeconds 是消息监听轮询间隔（默认 60 秒）。
	FeedIntervalSeconds int `toml:"feed_interval_seconds"`
	// FeedStatePath 是消息监听水位文件，按账号保存，跨重启保留。
	FeedStatePath string `toml:"feed_state_path"`
	// DynamicPollEnabled 轮询自己动态的评论区；feed 模式并行、轮询模式纳入自动发现。
	DynamicPollEnabled bool `toml:"dynamic_poll_enabled"`
	// 以下字段用于动态轮询与 FeedEnabled=false 的视频轮询。
	AutoDiscover            bool     `toml:"auto_discover"`
	Videos                  []string `toml:"videos"`
	IntervalSeconds         int      `toml:"interval_seconds"`
	DiscoverIntervalSeconds int      `toml:"discover_interval_seconds"`
}

func defaultCommentCollector() CommentCollector {
	return CommentCollector{
		FeedEnabled:             true,
		FeedIntervalSeconds:     60,
		FeedStatePath:           "state/msgfeed-cursors.json",
		DynamicPollEnabled:      true,
		AutoDiscover:            true,
		IntervalSeconds:         60,
		DiscoverIntervalSeconds: 600,
	}
}

var positiveOID = regexp.MustCompile(`^[0-9]{1,128}$`)

func canonicalOID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !positiveOID.MatchString(value) {
		return "", false
	}
	value = strings.TrimLeft(value, "0")
	return value, value != ""
}

// Validate 只检查静态配置，不读取凭据文件或访问平台。
func (c CommentCollector) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.FeedEnabled {
		if c.FeedIntervalSeconds < 10 || c.FeedIntervalSeconds > 3600 {
			return errors.New("消息监听 feed_interval_seconds 需在10至3600之间")
		}
		if strings.ContainsAny(c.FeedStatePath, "\x00\r\n") {
			return errors.New("消息监听 feed_state_path 非法")
		}
		if !c.DynamicPollEnabled {
			return nil
		}
		// 动态评论轮询与消息监听并行，复用轮询/发现间隔。
		if c.IntervalSeconds < 10 || c.IntervalSeconds > 3600 {
			return errors.New("动态评论轮询 interval_seconds 需在10至3600之间")
		}
		if c.DiscoverIntervalSeconds < 60 || c.DiscoverIntervalSeconds > 86400 {
			return errors.New("动态评论轮询 discover_interval_seconds 需在60至86400之间")
		}
		return nil
	}
	if !c.AutoDiscover && len(c.Videos) == 0 {
		return errors.New("评论采集关闭自动发现时需要配置 videos")
	}
	if c.AutoDiscover && len(c.Videos) == 0 && !c.DynamicPollEnabled {
		return errors.New("评论采集关闭动态轮询时，自动发现需要至少一个手动 videos")
	}
	seen := make(map[string]struct{}, len(c.Videos))
	for _, raw := range c.Videos {
		oid, ok := canonicalOID(raw)
		if !ok {
			return errors.New("评论采集 videos 必须是平台视频 oid")
		}
		if _, duplicate := seen[oid]; duplicate {
			return fmt.Errorf("评论采集 videos 存在重复 oid %s", oid)
		}
		seen[oid] = struct{}{}
	}
	if c.IntervalSeconds < 10 || c.IntervalSeconds > 3600 {
		return errors.New("评论采集 interval_seconds 需在10至3600之间")
	}
	if c.AutoDiscover && (c.DiscoverIntervalSeconds < 60 || c.DiscoverIntervalSeconds > 86400) {
		return errors.New("评论自动发现 discover_interval_seconds 需在60至86400之间")
	}
	return nil
}

// CommentSender 是评论发送通道的独立开关，默认关闭。
type CommentSender struct {
	Enabled bool `toml:"enabled"`
}

// accountSelfID 优先 [sender].self_id，其次采集侧同一账号的 UID。
func (c Config) accountSelfID() string {
	if c.Sender != nil {
		if id := strings.TrimSpace(c.Sender.SelfID); id != "" {
			return id
		}
	}
	return strings.TrimSpace(c.Collector.Live.SelfID)
}
