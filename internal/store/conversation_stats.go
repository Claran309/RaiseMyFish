package store

import (
	"context"
	"time"
)

// ConversationStat 是单通道的会话状态观测。
//
// 口径（详见 docs/conversation-state.md）：
//   - Active 是未过期会话数，Expired 是已过期但仍在库里的会话数；
//   - HitRate 是窗口内入向事件里带 conversation_id 的比例；
//   - TurnsAvg 只统计未过期会话；
//   - SummaryAgeSeconds 只统计未过期且有摘要的会话，没有样本时是 0 而不是负数；
//   - PromptTokenDeltaPct 是「有会话 / 无会话」两类回复的平均输入 token 差；
//     任一侧没有样本时为 nil，不能用 0 冒充「没变化」；
//   - P95LatencyDeltaMS 同理，基于 event_timings 的用户侧端到端延迟。
type ConversationStat struct {
	Channel string
	// Active / Expired 是会话状态行数。
	Active  int64
	Expired int64
	// WithSummary 是未过期且有摘要的会话数。
	WithSummary int64
	// SummaryCoverage 是 WithSummary / Active；Active 为 0 时为 0。
	SummaryCoverage float64
	// TurnsAvg 是未过期会话的平均轮数。
	TurnsAvg float64
	// SummaryAgeSeconds 是未过期会话摘要的平均年龄（秒）。
	SummaryAgeSeconds float64
	// Events 是窗口内入向事件数，WithConversation 是其中带会话键的条数。
	Events           int64
	WithConversation int64
	// HitRate 是 WithConversation / Events。
	HitRate float64
	// ConvInputTokens / PlainInputTokens 是两类回复的平均输入 token；
	// 0 值配合 Samples=0 表示无样本。
	ConvInputTokens  float64
	PlainInputTokens float64
	TokenSamples     int64
	// PromptTokenDeltaPct 是相对无会话基线的百分比变化；无基线时为 nil。
	PromptTokenDeltaPct *float64
	// ConvP95MS / PlainP95MS 是用户侧 P95 延迟（毫秒）。
	ConvP95MS  float64
	PlainP95MS float64
	// P95LatencyDeltaMS 是绝对差值；无基线时为 nil。
	P95LatencyDeltaMS *float64
	// OutEvents / RepeatedOut 是窗口内出向回复数与「同会话内重复上一条」的条数。
	OutEvents   int64
	RepeatedOut int64
	// RepetitionRate 是 RepeatedOut / OutEvents。
	RepetitionRate float64
}

// ConversationStats 是会话观测总表。
type ConversationStats struct {
	From        time.Time
	To          time.Time
	Channels    []ConversationStat
	GeneratedAt time.Time
}

// ConversationStatsRepo 提供会话观测聚合（database.Stats 实现）。
//
// 单独成接口而不是塞进 WebRepo：面板只是消费方之一，
// CLI/日报也要读同一份口径，不能绑死在面板的数据源上。
type ConversationStatsRepo interface {
	ConversationStats(ctx context.Context, from, to, now time.Time) (ConversationStats, error)
}
