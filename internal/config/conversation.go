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
	// SummaryEnabled 决定是否启用滚动摘要 worker；默认关闭。
	SummaryEnabled bool `toml:"summary_enabled"`
	// SummaryEveryTurns 是「累计多少新轮次后触发摘要」的阈值。
	SummaryEveryTurns int `toml:"summary_every_turns"`
	// SummaryIdleSeconds 是会话空闲多久后触发摘要；0 表示关闭空闲触发。
	SummaryIdleSeconds int `toml:"summary_idle_seconds"`
	// SummaryIntervalSeconds 是 worker 扫描间隔。
	SummaryIntervalSeconds int `toml:"summary_interval_seconds"`
	// SummaryTimeoutSeconds 是单次摘要模型调用超时。
	SummaryTimeoutSeconds int `toml:"summary_timeout_seconds"`
	// SummaryMaxAttempts 是摘要任务最大尝试次数。
	SummaryMaxAttempts int `toml:"summary_max_attempts"`
	// SummaryContextTurns 是喂给摘要器的最近轮数上限。
	SummaryContextTurns int `toml:"summary_context_turns"`
	// CommentRolloutPercent 是评论 thread 会话的灰度百分比（0=关闭，100=全量）。
	CommentRolloutPercent int `toml:"comment_rollout_percent"`
	// DMRolloutPercent 是私信会话的灰度百分比（0=关闭，100=全量）。
	DMRolloutPercent int `toml:"dm_rollout_percent"`
}

func defaultConversation() Conversation {
	return Conversation{
		Enabled: false, DMEnabled: false, CommentEnabled: false,
		LiveWindowSeconds: 0, RecentTurns: 8, SummaryMaxRunes: 300, TTLHours: 168,
		SummaryEnabled: false, SummaryEveryTurns: 12, SummaryIdleSeconds: 300,
		SummaryIntervalSeconds: 60, SummaryTimeoutSeconds: 20, SummaryMaxAttempts: 3,
		SummaryContextTurns: 12,
		// 灰度默认全量：只有把 *_enabled 打开才会生效，rollout 用来逐步放量。
		CommentRolloutPercent: 100, DMRolloutPercent: 100,
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
	if c.SummaryEnabled {
		if c.SummaryEveryTurns < 2 || c.SummaryEveryTurns > 100 {
			return fmt.Errorf("conversation.summary_every_turns 需在2至100")
		}
		if c.SummaryIdleSeconds < 0 || c.SummaryIdleSeconds > 24*3600 {
			return fmt.Errorf("conversation.summary_idle_seconds 需在0至86400")
		}
		if c.SummaryIntervalSeconds < 5 || c.SummaryIntervalSeconds > 3600 {
			return fmt.Errorf("conversation.summary_interval_seconds 需在5至3600")
		}
		if c.SummaryTimeoutSeconds < 1 || c.SummaryTimeoutSeconds > 300 {
			return fmt.Errorf("conversation.summary_timeout_seconds 需在1至300")
		}
		if c.SummaryMaxAttempts < 1 || c.SummaryMaxAttempts > 10 {
			return fmt.Errorf("conversation.summary_max_attempts 需在1至10")
		}
		if c.SummaryContextTurns < 4 || c.SummaryContextTurns > 50 {
			return fmt.Errorf("conversation.summary_context_turns 需在4至50")
		}
	}
	if c.CommentRolloutPercent < 0 || c.CommentRolloutPercent > 100 {
		return fmt.Errorf("conversation.comment_rollout_percent 需在0至100")
	}
	if c.DMRolloutPercent < 0 || c.DMRolloutPercent > 100 {
		return fmt.Errorf("conversation.dm_rollout_percent 需在0至100")
	}
	return nil
}
