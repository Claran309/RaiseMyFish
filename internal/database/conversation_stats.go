package database

// 本文件实现 C8 的会话观测聚合：命中率、轮数、摘要年龄、
// prompt token 增量、P95 延迟增量与重复回复率。
//
// 口径一律以「有会话 vs 无会话」的对照给出，没有样本时返回 nil 而不是 0：
// 「没数据」和「没有变化」在运维判断里是两件事。

import (
	"context"
	"time"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// conversationStatChannels 是会话观测覆盖的通道；直播不建持久会话。
var conversationStatChannels = []string{"dm", "comment"}

// ConversationStats 汇总窗口内的会话观测。
func (s *Stats) ConversationStats(ctx context.Context, from, to time.Time, now time.Time) (store.ConversationStats, error) {
	if ctx == nil || to.Before(from) || now.IsZero() {
		return store.ConversationStats{}, store.ErrInvalid
	}
	out := store.ConversationStats{From: from, To: to, GeneratedAt: now}
	for _, channel := range conversationStatChannels {
		stat, err := queryConversationStat(ctx, s.db, channel, from, to, now)
		if err != nil {
			return store.ConversationStats{}, err
		}
		out.Channels = append(out.Channels, stat)
	}
	return out, nil
}

func queryConversationStat(ctx context.Context, db *gorm.DB, channel string, from, to, now time.Time) (store.ConversationStat, error) {
	stat := store.ConversationStat{Channel: channel}
	// 1) 会话状态：未过期 / 已过期 / 有摘要 / 平均轮数 / 摘要年龄。
	var state struct {
		Active      int64   `gorm:"column:active"`
		Expired     int64   `gorm:"column:expired"`
		WithSummary int64   `gorm:"column:with_summary"`
		TurnsAvg    float64 `gorm:"column:turns_avg"`
		SummaryAge  float64 `gorm:"column:summary_age"`
	}
	if err := db.WithContext(ctx).Raw(`
SELECT
  COUNT(*) FILTER (WHERE expires_at IS NULL OR expires_at > ?) AS active,
  COUNT(*) FILTER (WHERE expires_at IS NOT NULL AND expires_at <= ?) AS expired,
  COUNT(*) FILTER (WHERE (expires_at IS NULL OR expires_at > ?) AND btrim(summary) <> '') AS with_summary,
  COALESCE(AVG(turn_count) FILTER (WHERE expires_at IS NULL OR expires_at > ?), 0)::float8 AS turns_avg,
  COALESCE(AVG(EXTRACT(EPOCH FROM (? - summary_updated_at)))
           FILTER (WHERE (expires_at IS NULL OR expires_at > ?)
                     AND summary_updated_at IS NOT NULL
                     AND btrim(summary) <> ''), 0)::float8 AS summary_age
FROM conversation_state
WHERE channel = ?`,
		now, now, now, now, now, now, channel).Scan(&state).Error; err != nil {
		return stat, &safeError{"统计会话状态失败", err}
	}
	stat.Active, stat.Expired = state.Active, state.Expired
	stat.WithSummary, stat.TurnsAvg, stat.SummaryAgeSeconds = state.WithSummary, state.TurnsAvg, state.SummaryAge
	if stat.Active > 0 {
		stat.SummaryCoverage = float64(stat.WithSummary) / float64(stat.Active)
	}

	// 2) 窗口内入向事件的会话命中率。
	var hit struct {
		Events           int64 `gorm:"column:events"`
		WithConversation int64 `gorm:"column:with_conversation"`
	}
	if err := db.WithContext(ctx).Raw(`
SELECT COUNT(*) AS events,
       COUNT(*) FILTER (WHERE conversation_id IS NOT NULL AND btrim(conversation_id) <> '') AS with_conversation
FROM events
WHERE channel = ? AND dir = 'in' AND at >= ? AND at < ?`,
		channel, from, to).Scan(&hit).Error; err != nil {
		return stat, &safeError{"统计会话命中率失败", err}
	}
	stat.Events, stat.WithConversation = hit.Events, hit.WithConversation
	if stat.Events > 0 {
		stat.HitRate = float64(stat.WithConversation) / float64(stat.Events)
	}

	// 3) prompt token 对照：purpose=reply 的调用按事件是否带会话分组。
	var tokens struct {
		ConvAvg  float64 `gorm:"column:conv_avg"`
		PlainAvg float64 `gorm:"column:plain_avg"`
		ConvN    int64   `gorm:"column:conv_n"`
		PlainN   int64   `gorm:"column:plain_n"`
	}
	if err := db.WithContext(ctx).Raw(`
WITH reply_calls AS (
  SELECT e.conversation_id AS conversation_id, u.in_tokens
    FROM llm_usage u
    JOIN events e ON e.id = u.correlation_id
   WHERE u.purpose = 'reply' AND u.ok IS TRUE AND u.in_tokens IS NOT NULL
     AND e.channel = ? AND e.dir = 'in' AND e.at >= ? AND e.at < ?
)
SELECT COALESCE(AVG(in_tokens) FILTER (WHERE conversation_id IS NOT NULL AND btrim(conversation_id) <> ''), 0)::float8 AS conv_avg,
       COALESCE(AVG(in_tokens) FILTER (WHERE conversation_id IS NULL OR btrim(conversation_id) = ''), 0)::float8 AS plain_avg,
       COUNT(*) FILTER (WHERE conversation_id IS NOT NULL AND btrim(conversation_id) <> '') AS conv_n,
       COUNT(*) FILTER (WHERE conversation_id IS NULL OR btrim(conversation_id) = '') AS plain_n
FROM reply_calls`,
		channel, from, to).Scan(&tokens).Error; err != nil {
		return stat, &safeError{"统计会话token对照失败", err}
	}
	stat.TokenSamples = tokens.ConvN + tokens.PlainN
	stat.ConvInputTokens, stat.PlainInputTokens = tokens.ConvAvg, tokens.PlainAvg
	if tokens.ConvN > 0 && tokens.PlainN > 0 && tokens.PlainAvg > 0 {
		delta := (tokens.ConvAvg - tokens.PlainAvg) / tokens.PlainAvg * 100
		stat.PromptTokenDeltaPct = &delta
	}

	// 4) 用户侧 P95 延迟对照：首次成功发送 - 采集时间。
	var latency struct {
		ConvP95  float64 `gorm:"column:conv_p95"`
		PlainP95 float64 `gorm:"column:plain_p95"`
		ConvN    int64   `gorm:"column:conv_n"`
		PlainN   int64   `gorm:"column:plain_n"`
	}
	if err := db.WithContext(ctx).Raw(`
WITH first_ok AS (
  SELECT DISTINCT ON (l.event_id)
         e.conversation_id AS conversation_id,
         (EXTRACT(EPOCH FROM (l.at - COALESCE(e.collected_at, e.at))) * 1000)::float8 AS user_ms
    FROM send_log l
    JOIN events e ON e.id = l.event_id
   WHERE l.ok IS TRUE AND e.channel = ? AND e.dir = 'in'
     AND e.at >= ? AND e.at < ? AND l.at >= COALESCE(e.collected_at, e.at)
   ORDER BY l.event_id, l.at ASC
)
SELECT COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY user_ms)
                  FILTER (WHERE conversation_id IS NOT NULL AND btrim(conversation_id) <> ''), 0)::float8 AS conv_p95,
       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY user_ms)
                  FILTER (WHERE conversation_id IS NULL OR btrim(conversation_id) = ''), 0)::float8 AS plain_p95,
       COUNT(*) FILTER (WHERE conversation_id IS NOT NULL AND btrim(conversation_id) <> '') AS conv_n,
       COUNT(*) FILTER (WHERE conversation_id IS NULL OR btrim(conversation_id) = '') AS plain_n
FROM first_ok`,
		channel, from, to).Scan(&latency).Error; err != nil {
		return stat, &safeError{"统计会话延迟对照失败", err}
	}
	stat.ConvP95MS, stat.PlainP95MS = latency.ConvP95, latency.PlainP95
	if latency.ConvN > 0 && latency.PlainN > 0 {
		delta := latency.ConvP95 - latency.PlainP95
		stat.P95LatencyDeltaMS = &delta
	}

	// 5) 重复回复率：同会话相邻两条出向正文完全一致。
	var repeat struct {
		OutEvents   int64 `gorm:"column:out_events"`
		RepeatedOut int64 `gorm:"column:repeated_out"`
	}
	if err := db.WithContext(ctx).Raw(`
WITH outs AS (
  SELECT conversation_id, text,
         LAG(text) OVER (PARTITION BY conversation_id ORDER BY at, id) AS prev_text
    FROM events
   WHERE channel = ? AND dir = 'out' AND status = 'done'
     AND at >= ? AND at < ?
     AND conversation_id IS NOT NULL AND btrim(conversation_id) <> ''
)
SELECT COUNT(*) AS out_events,
       COUNT(*) FILTER (WHERE prev_text IS NOT NULL AND btrim(prev_text) = btrim(text)) AS repeated_out
FROM outs`,
		channel, from, to).Scan(&repeat).Error; err != nil {
		return stat, &safeError{"统计重复回复失败", err}
	}
	stat.OutEvents, stat.RepeatedOut = repeat.OutEvents, repeat.RepeatedOut
	if stat.OutEvents > 0 {
		stat.RepetitionRate = float64(stat.RepeatedOut) / float64(stat.OutEvents)
	}
	return stat, nil
}
