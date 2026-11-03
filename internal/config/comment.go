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

// CommentCollector 是视频评论轮询采集配置。
type CommentCollector struct {
	Enabled         bool     `toml:"enabled"`
	Videos          []string `toml:"videos"`
	IntervalSeconds int      `toml:"interval_seconds"`
}

func defaultCommentCollector() CommentCollector {
	return CommentCollector{IntervalSeconds: 60}
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
	if len(c.Videos) == 0 {
		return errors.New("评论采集启用需要配置 videos")
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
	return nil
}

// CommentSender 是评论发送通道的独立开关，默认关闭。
type CommentSender struct {
	Enabled bool `toml:"enabled"`
}

// commentSelfID 优先 [sender].self_id，其次采集侧同一账号的 UID。
func (c Config) commentSelfID() string {
	if c.Sender != nil {
		if id := strings.TrimSpace(c.Sender.SelfID); id != "" {
			return id
		}
	}
	return strings.TrimSpace(c.Collector.Live.SelfID)
}
