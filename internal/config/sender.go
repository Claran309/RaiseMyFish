package config

// 本文件定义弹幕发送通道的部署配置（`[sender]`）。
//
// 为什么要和 `[collector.live]` 分开：采集脚本是 `danmaku-await.mjs`（长连监听），
// 发送脚本是 `danmaku.mjs`（一次性 POST），两者路径与参数不同，混用必然发不出去。
// 凭据仍复用采集侧解析到的 FISH_BILI_COOKIE——同一个账号，没必要配两遍。

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// 发送默认值。
const (
	defaultSenderNodePath   = "node"
	defaultSenderScriptPath = "tools/bili-whale/danmaku.mjs"
	defaultSenderTimeoutMS  = 35000
	senderTimeoutMinMS      = 1000
	senderTimeoutMaxMS      = 600000
)

// positiveSenderID 与采集侧同一口径：平台 UID 只能是十进制数字。
var positiveSenderID = regexp.MustCompile(`^[0-9]{1,128}$`)

// Sender 是弹幕发送通道配置；nil 表示未配置发送。
type Sender struct {
	NodePath      string `toml:"node_path"`
	ScriptPath    string `toml:"script_path"`
	SelfID        string `toml:"self_id"`
	HTTPTimeoutMS int    `toml:"http_timeout_ms"`
}

// DefaultSender 返回一份可直接使用的默认发送配置。
func DefaultSender() Sender {
	return Sender{
		NodePath:      defaultSenderNodePath,
		ScriptPath:    defaultSenderScriptPath,
		HTTPTimeoutMS: defaultSenderTimeoutMS,
	}
}

// ApplyDefaults 只补零值，不覆盖用户显式写的值。
func (s *Sender) ApplyDefaults() {
	if s == nil {
		return
	}
	if strings.TrimSpace(s.NodePath) == "" {
		s.NodePath = defaultSenderNodePath
	}
	if strings.TrimSpace(s.ScriptPath) == "" {
		s.ScriptPath = defaultSenderScriptPath
	}
	if s.HTTPTimeoutMS == 0 {
		s.HTTPTimeoutMS = defaultSenderTimeoutMS
	}
}

// Validate 校验发送配置；nil（未配置）直接通过。
func (s *Sender) Validate() error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(s.ScriptPath) == "" {
		return errors.New("发送通道 script_path 不能为空")
	}
	if strings.TrimSpace(s.NodePath) == "" {
		return errors.New("发送通道 node_path 不能为空")
	}
	if strings.ContainsAny(s.NodePath+s.ScriptPath, "\r\n\x00") {
		return errors.New("发送通道路径不能包含控制字符")
	}
	if s.HTTPTimeoutMS < senderTimeoutMinMS || s.HTTPTimeoutMS > senderTimeoutMaxMS {
		return fmt.Errorf("发送通道 http_timeout_ms 需在%d至%d之间", senderTimeoutMinMS, senderTimeoutMaxMS)
	}
	// self_id 可留空（回落到采集侧同一账号），填了就必须是合法 UID
	if id := strings.TrimSpace(s.SelfID); id != "" {
		if !positiveSenderID.MatchString(id) || strings.Trim(id, "0") == "" {
			return errors.New("发送通道 self_id 必须是平台数字UID")
		}
	}
	return nil
}
