package config

import "errors"

// Presence 是常驻在线能力的部署配置（`[presence]`），默认关闭。
type Presence struct {
	// HeartbeatLikeSeconds 是常驻点赞间隔；0 表示关闭，开启时 10-600 秒。
	HeartbeatLikeSeconds int `toml:"heartbeat_like_seconds"`
}

// Validate 只做静态校验；凭据与 CSRF 在装配阶段检查。
func (p Presence) Validate() error {
	if p.HeartbeatLikeSeconds != 0 && (p.HeartbeatLikeSeconds < 10 || p.HeartbeatLikeSeconds > 600) {
		return errors.New("常驻点赞间隔必须为0（关闭）或10至600秒")
	}
	return nil
}
