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

// CommentCollector 是评论轮询采集配置。
//
// AutoDiscover 开启时按 DiscoverIntervalSeconds 周期刷新自己账号的视频/动态列表，
// 与手动 Videos 合并后轮询；发现失败只保留手动列表，不影响已有轮询。
type CommentCollector struct {
	Enabled                 bool     `toml:"enabled"`
	AutoDiscover            bool     `toml:"auto_discover"`
	Videos                  []string `toml:"videos"`
	IntervalSeconds         int      `toml:"interval_seconds"`
	DiscoverIntervalSeconds int      `toml:"discover_interval_seconds"`
}

func defaultCommentCollector() CommentCollector {
	return CommentCollector{AutoDiscover: true, IntervalSeconds: 60, DiscoverIntervalSeconds: 600}
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
	if !c.AutoDiscover && len(c.Videos) == 0 {
		return errors.New("评论采集关闭自动发现时需要配置 videos")
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
