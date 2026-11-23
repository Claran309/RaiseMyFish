package config

import "fmt"

// Conversation 是会话状态开关；默认全关，保证与现网行为一致。
type Conversation struct {
	Enabled        bool `toml:"enabled"`
	DMEnabled      bool `toml:"dm_enabled"`
	CommentEnabled bool `toml:"comment_enabled"`
	// LiveWindowSeconds 是直播短窗口 TTL；0 表示关闭。
	LiveWindowSeconds int `toml:"live_window_seconds"`
	// RecentTurns 是注入的最近轮数。
	RecentTurns int `toml:"recent_turns"`
	// SummaryMaxRunes 是摘要长度上限。
	SummaryMaxRunes int `toml:"summary_max_runes"`
	// TTLHours 是会话状态过期时间。
	TTLHours int `toml:"ttl_hours"`
}

func defaultConversation() Conversation {
	return Conversation{
		Enabled: false, DMEnabled: false, CommentEnabled: false,
		LiveWindowSeconds: 0, RecentTurns: 8, SummaryMaxRunes: 300, TTLHours: 168,
	}
}

// Validate 拒绝越界参数；关闭时不强制校验。
func (c Conversation) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.LiveWindowSeconds < 0 || c.LiveWindowSeconds > 300 {
		return fmt.Errorf("conversation.live_window_seconds 需在0至300")
	}
	if c.RecentTurns < 1 || c.RecentTurns > 20 {
		return fmt.Errorf("conversation.recent_turns 需在1至20")
	}
	if c.SummaryMaxRunes < 50 || c.SummaryMaxRunes > 1000 {
		return fmt.Errorf("conversation.summary_max_runes 需在50至1000")
	}
	if c.TTLHours < 1 || c.TTLHours > 24*365 {
		return fmt.Errorf("conversation.ttl_hours 需在1至8760")
	}
	return nil
}
