package database

// 0.10d：`fishctl status` 的只读聚合实现。
//
// 只做 COUNT/MAX/SUM 查询：不改表结构、不加迁移、不写任何表。
// 口径写死在下面对常量里，方便 DSH 逐条核对；统计与隐私口径一旦要改，只需要动这里。

import (
	"context"
	"time"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// ChannelCounts 是单通道计数。
//
// 刻意不复用 observe.ChannelStat：存储层不该依赖渲染层的类型。
type ChannelCounts struct {
	Received int
	Replied  int
	Skipped  int
	Failed   int
}

// WindowCounts 是窗口内的聚合结果。
type WindowCounts struct {
	Received int
	Replied  int
	Skipped  int
	Failed   int
	// CostUSD 为 nil 表示这段时间没有可用的费用记录（不是 0）。
	CostUSD *float64
	// Channels 是按通道细分；无法归属通道的行不会进这张表。
	Channels map[string]ChannelCounts
}

// Stats 提供 status 需要的聚合查询。
type Stats struct{ db *gorm.DB }

// NewStats 注入既有连接，不自动迁移。
func NewStats(db *gorm.DB) (*Stats, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &Stats{db: db}, nil
}

// 计数口径：
//
//	Received = events 里 dir='in' 的事件数（本窗口收到了多少条待处理输入）
//	Skipped  = events 里 status='skipped' 的事件数（主动不回复）
//	Replied  = 窗口内 dir='in' 事件中，send_log.ok IS TRUE 的去重事件数
//	Failed   = 窗口内 dir='in' 事件中，send_log.ok IS FALSE 的去重事件数（event_id 缺失的老行仍计）
//	CostUSD  = llm_usage.cost_usd 求和（没有记录或全为 NULL → nil = 未知）
//
// 用去重事件数而不是尝试次数：同一条回复重试多次不该把「回复数」算多。
// 老数据的 send_log.channel 可能为空，回落到 events.channel。
const (
	// sentRowsSQL 只统计「窗口内的入向事件」：必须 join events 且按**事件时间**过滤。
	//
	// 修 bug：原先按 send_log.at 过滤、且不要求 e.dir='in'，会把窗口外事件、
	// 出向事件的发送记录都算进回复数，出现「回复 > 收到-跳过」，成功率爆百。
	sentRowsSQL = `
SELECT COALESCE(NULLIF(l.channel, ''), e.channel, '') AS channel,
       COUNT(DISTINCT l.event_id) AS n
FROM send_log l
JOIN events e ON e.id = l.event_id
WHERE l.ok IS TRUE AND e.dir = 'in' AND e.at >= ? AND e.at < ?
GROUP BY 1`

	// failedRowsSQL 与 sentRowsSQL 对称：也只统计「窗口内的入向事件」，
	// 按事件时间过滤 + 要求 dir='in'，避免 Failed 与 Replied 口径不一致。
	// 仍用 LEFT JOIN：event_id 缺失的老审计行也计入（无从归属到某条事件，但不能丢）。
	failedRowsSQL = `
SELECT COALESCE(NULLIF(l.channel, ''), e.channel, '') AS channel,
       COUNT(DISTINCT COALESCE(l.event_id, 'log:' || l.id::text)) AS n
FROM send_log l
LEFT JOIN events e ON e.id = l.event_id
WHERE l.ok IS FALSE AND (e.id IS NULL OR (e.dir = 'in' AND e.at >= ? AND e.at < ?))
GROUP BY 1`
)

// channelCountRow 是「通道 + 计数」的通用扫描目标。
type channelCountRow struct {
	Channel string
	N       int
}

// Counts 返回 [from, to) 窗口内的聚合计数。
//
// 时间窗口是左闭右开：`at >= from AND at < to`，相邻日报不会重复计同一条事件。
func (s *Stats) Counts(ctx context.Context, from, to time.Time) (WindowCounts, error) {
	if ctx == nil || to.Before(from) {
		return WindowCounts{}, store.ErrInvalid
	}

	result := WindowCounts{Channels: make(map[string]ChannelCounts)}
	// 汇总与细分一起累加，保证「总量 == 各通道之和」（空通道名的行会被跳过）
	add := func(channel string, apply func(*ChannelCounts)) {
		if channel == "" {
			return
		}
		c := result.Channels[channel]
		apply(&c)
		result.Channels[channel] = c
	}

	var received []channelCountRow
	if err := s.db.WithContext(ctx).Table("events").
		Select("channel, COUNT(*) AS n").
		Where("dir = ?", "in").
		Where("at >= ? AND at < ?", from, to).
		Group("channel").Scan(&received).Error; err != nil {
		return WindowCounts{}, &safeError{"统计收到事件失败", err}
	}
	for _, row := range received {
		result.Received += row.N
		add(row.Channel, func(c *ChannelCounts) { c.Received += row.N })
	}

	var skipped []channelCountRow
	if err := s.db.WithContext(ctx).Table("events").
		Select("channel, COUNT(*) AS n").
		Where("status = ?", "skipped").
		Where("at >= ? AND at < ?", from, to).
		Group("channel").Scan(&skipped).Error; err != nil {
		return WindowCounts{}, &safeError{"统计跳过事件失败", err}
	}
	for _, row := range skipped {
		result.Skipped += row.N
		add(row.Channel, func(c *ChannelCounts) { c.Skipped += row.N })
	}

	var sent []channelCountRow
	if err := s.db.WithContext(ctx).Raw(sentRowsSQL, from, to).Scan(&sent).Error; err != nil {
		return WindowCounts{}, &safeError{"统计已回复事件失败", err}
	}
	for _, row := range sent {
		result.Replied += row.N
		add(row.Channel, func(c *ChannelCounts) { c.Replied += row.N })
	}

	var failed []channelCountRow
	if err := s.db.WithContext(ctx).Raw(failedRowsSQL, from, to).Scan(&failed).Error; err != nil {
		return WindowCounts{}, &safeError{"统计发送失败事件失败", err}
	}
	for _, row := range failed {
		result.Failed += row.N
		add(row.Channel, func(c *ChannelCounts) { c.Failed += row.N })
	}

	var cost struct{ Total *float64 }
	if err := s.db.WithContext(ctx).
		Raw(`SELECT SUM(cost_usd) AS total FROM llm_usage WHERE at >= ? AND at < ?`, from, to).
		Scan(&cost).Error; err != nil {
		return WindowCounts{}, &safeError{"统计费用失败", err}
	}
	// SUM 在没有行时返回 NULL → 保持 nil，交给渲染层显示「未知」而不是 0
	result.CostUSD = cost.Total

	return result, nil
}

// LastSeen 返回各通道最近一次「收到事件」的时间。
//
// 只看 dir='in'：心跳要发现的是采集侧静默——如果算上 out 事件，
// 采集器挂了但还在补发旧回复时，通道会显得"还活着"，正是最难查的那种静默失败。
func (s *Stats) LastSeen(ctx context.Context) (map[string]time.Time, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}

	var rows []struct {
		Channel string
		Last    time.Time
	}
	if err := s.db.WithContext(ctx).Table("events").
		Select("channel, MAX(at) AS last").
		Where("dir = ?", "in").
		Group("channel").Scan(&rows).Error; err != nil {
		return nil, &safeError{"统计通道心跳失败", err}
	}

	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		if row.Channel == "" || row.Last.IsZero() {
			continue
		}
		out[row.Channel] = row.Last
	}
	return out, nil
}
