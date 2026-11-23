package database

// 0.10d：`fishctl status` 的只读聚合实现。
//
// 只做 COUNT/MAX/SUM 查询：不改表结构、不加迁移、不写任何表。
// 口径写死在下面对常量里，方便 DSH 逐条核对；统计与隐私口径一旦要改，只需要动这里。

import (
	"context"
	"math"
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
	// KnownCostUSD 是窗口内已计价调用的费用合计；nil 表示没有已计价调用（不是 0）。
	KnownCostUSD *float64
	// UnknownCalls 是费用未知（未配置报价）的调用数。
	UnknownCalls int
	// TotalCalls 是窗口内的调用总数。
	TotalCalls int
	// Coverage 是已计价调用占比；TotalCalls 为 0 时按 0 处理。
	Coverage float64
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
//	费用口径 = known_cost(SUM) + unknown_calls(cost_usd IS NULL) + total_calls，与 Web 面板一致
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

	var cost struct {
		KnownCost    *float64
		UnknownCalls int
		TotalCalls   int
	}
	// 与 Web 面板同一口径：SUM 保留 NULL 语义（全无报价 → known_cost=nil，不 COALESCE 成 0），
	// 同时给出未知条数与总条数，渲染层才能区分空账本、真实零、全未知与混合。
	if err := s.db.WithContext(ctx).
		Raw(`SELECT SUM(cost_usd) AS known_cost,
		            COUNT(*) FILTER (WHERE cost_usd IS NULL) AS unknown_calls,
		            COUNT(*) AS total_calls
		       FROM llm_usage WHERE at >= ? AND at < ?`, from, to).
		Scan(&cost).Error; err != nil {
		return WindowCounts{}, &safeError{"统计费用失败", err}
	}
	result.KnownCostUSD = cost.KnownCost
	result.UnknownCalls = cost.UnknownCalls
	result.TotalCalls = cost.TotalCalls
	if cost.TotalCalls > 0 {
		result.Coverage = float64(cost.TotalCalls-cost.UnknownCalls) / float64(cost.TotalCalls)
	}

	return result, nil
}

// LatencyStat 是单通道回复延迟（毫秒）。
//
// 口径与 Replied 一致：只统计窗口内入向事件的第一条成功发送记录；
// 重试多次不会把延迟重复计入，也不会把失败尝试算进来。
type LatencyStat struct {
	Count int64
	P50MS int64
	P90MS int64
	P99MS int64
	AvgMS int64
	MaxMS int64
}

// LatencyWindow 是一个统计窗口的分位结果；样本不足时不展示分位。
type LatencyWindow struct {
	Count            int64
	P50MS            int64
	P90MS            int64
	P99MS            int64
	AvgMS            int64
	MaxMS            int64
	SampleSufficient bool
}

// LatencySegments 是采集/排队/生成/发送拆分；缺失阶段按零值表示 unknown。
type LatencySegments struct {
	Collect     LatencyWindow
	Queue       LatencyWindow
	Generate    LatencyWindow
	Send        LatencyWindow
	E2EUser     LatencyWindow
	E2EPlatform LatencyWindow
}

// LatencyDetail 是同一通道的全量与 fresh 口径。
type LatencyDetail struct {
	All      LatencyWindow
	Fresh    LatencyWindow
	Segments LatencySegments
}

// latencyChannels 是面板/日报关心的业务通道；qq 等未启用通道不混进结果。
var latencyChannels = map[string]bool{"live": true, "comment": true, "dm": true}

// latencyRowsSQL 计算「事件时间 → 首次成功发送时间」的延迟分位。
//
// DISTINCT ON 保证每个事件只取最早一次成功发送；过滤 l.at >= e.at
// 防止时钟回拨造成负延迟污染分位。EXTRACT 返回 numeric，必须显式转 float8，
// 否则 percentile_cont 会因没有 numeric 重载而报错。
const latencyRowsSQL = `
WITH first_ok AS (
    SELECT DISTINCT ON (l.event_id)
           l.event_id,
           COALESCE(NULLIF(l.channel, ''), e.channel) AS channel,
           l.at AS sent_at,
           e.at AS event_at
      FROM send_log l
      JOIN events e ON e.id = l.event_id
     WHERE l.ok IS TRUE AND e.dir = 'in'
       AND e.at >= ? AND e.at < ?
       AND l.at >= e.at
     ORDER BY l.event_id, l.at ASC
)
SELECT channel,
       COUNT(*) AS n,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8) AS p50_ms,
       percentile_cont(0.9) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8) AS p90_ms,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8) AS p99_ms,
       AVG(EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8 AS avg_ms,
       MAX(EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8 AS max_ms
  FROM first_ok
 GROUP BY channel`

// Latency 返回 [from, to) 窗口内各通道回复延迟分位。
func (s *Stats) Latency(ctx context.Context, from, to time.Time) (map[string]LatencyStat, error) {
	return queryLatency(ctx, s.db, from, to)
}

// queryLatency 供 Stats 与 WebRepo 共用，避免两处 SQL 口径漂移。
func queryLatency(ctx context.Context, db *gorm.DB, from, to time.Time) (map[string]LatencyStat, error) {
	if ctx == nil || db == nil || to.Before(from) {
		return nil, store.ErrInvalid
	}
	// 列别名带 _ms 后缀便于读 SQL，这里必须用 tag 显式映射；
	// 靠字段名推导会去找 p50/p90/avg/max 列，扫出全 0。
	var rows []struct {
		Channel string  `gorm:"column:channel"`
		N       int64   `gorm:"column:n"`
		P50     float64 `gorm:"column:p50_ms"`
		P90     float64 `gorm:"column:p90_ms"`
		P99     float64 `gorm:"column:p99_ms"`
		Avg     float64 `gorm:"column:avg_ms"`
		Max     float64 `gorm:"column:max_ms"`
	}
	if err := db.WithContext(ctx).Raw(latencyRowsSQL, from, to).Scan(&rows).Error; err != nil {
		return nil, &safeError{"统计回复延迟失败", err}
	}
	out := make(map[string]LatencyStat, len(rows))
	for _, row := range rows {
		if !latencyChannels[row.Channel] || row.N == 0 {
			continue
		}
		out[row.Channel] = LatencyStat{
			Count: row.N,
			P50MS: roundMS(row.P50),
			P90MS: roundMS(row.P90),
			P99MS: roundMS(row.P99),
			AvgMS: roundMS(row.Avg),
			MaxMS: roundMS(row.Max),
		}
	}
	return out, nil
}

// latencyDetailSQL 以每条入事件的首次成功发送为样本，LEFT JOIN 计时表：
// 历史事件没有 event_timings 时仍保留 e2e_platform，但各拆分阶段为 NULL。
const latencyDetailSQL = `
WITH first_ok AS (
    SELECT DISTINCT ON (l.event_id)
           COALESCE(NULLIF(l.channel, ''), e.channel) AS channel,
           l.at AS sent_at,
           e.at AS event_at,
           e.collected_at,
           t.enqueued_at,
           t.claimed_at,
           t.generated_at,
           t.send_started_at
      FROM send_log l
      JOIN events e ON e.id = l.event_id
      LEFT JOIN event_timings t ON t.event_id = e.id
     WHERE l.ok IS TRUE AND e.dir = 'in'
       AND e.at >= ? AND e.at < ?
       AND l.at >= e.at
       AND (? = 'all' OR (e.collect_mode IS DISTINCT FROM 'backfill' AND e.collected_at >= ?))
     ORDER BY l.event_id, l.at ASC
)
SELECT channel,
       COUNT(*) AS n,
       COUNT(collected_at) AS collect_n,
       COUNT(claimed_at) AS queue_n,
       COUNT(generated_at) AS generate_n,
       COUNT(send_started_at) AS send_n,
       COUNT(collected_at) AS user_n,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8) AS p50_ms,
       percentile_cont(0.9) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8) AS p90_ms,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8) AS p99_ms,
       AVG(EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8 AS avg_ms,
       MAX(EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::float8 AS max_ms,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (collected_at - event_at)) * 1000)::float8) AS collect_p50,
       percentile_cont(0.9) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (collected_at - event_at)) * 1000)::float8) AS collect_p90,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (collected_at - event_at)) * 1000)::float8) AS collect_p99,
       AVG(EXTRACT(EPOCH FROM (collected_at - event_at)) * 1000)::float8 AS collect_avg,
       MAX(EXTRACT(EPOCH FROM (collected_at - event_at)) * 1000)::float8 AS collect_max,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (claimed_at - collected_at)) * 1000)::float8) AS queue_p50,
       percentile_cont(0.9) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (claimed_at - collected_at)) * 1000)::float8) AS queue_p90,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (claimed_at - collected_at)) * 1000)::float8) AS queue_p99,
       AVG(EXTRACT(EPOCH FROM (claimed_at - collected_at)) * 1000)::float8 AS queue_avg,
       MAX(EXTRACT(EPOCH FROM (claimed_at - collected_at)) * 1000)::float8 AS queue_max,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (generated_at - claimed_at)) * 1000)::float8) AS generate_p50,
       percentile_cont(0.9) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (generated_at - claimed_at)) * 1000)::float8) AS generate_p90,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (generated_at - claimed_at)) * 1000)::float8) AS generate_p99,
       AVG(EXTRACT(EPOCH FROM (generated_at - claimed_at)) * 1000)::float8 AS generate_avg,
       MAX(EXTRACT(EPOCH FROM (generated_at - claimed_at)) * 1000)::float8 AS generate_max,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - send_started_at)) * 1000)::float8) AS send_p50,
       percentile_cont(0.9) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - send_started_at)) * 1000)::float8) AS send_p90,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - send_started_at)) * 1000)::float8) AS send_p99,
       AVG(EXTRACT(EPOCH FROM (sent_at - send_started_at)) * 1000)::float8 AS send_avg,
       MAX(EXTRACT(EPOCH FROM (sent_at - send_started_at)) * 1000)::float8 AS send_max,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - collected_at)) * 1000)::float8) AS user_p50,
       percentile_cont(0.9) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - collected_at)) * 1000)::float8) AS user_p90,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY (EXTRACT(EPOCH FROM (sent_at - collected_at)) * 1000)::float8) AS user_p99,
       AVG(EXTRACT(EPOCH FROM (sent_at - collected_at)) * 1000)::float8 AS user_avg,
       MAX(EXTRACT(EPOCH FROM (sent_at - collected_at)) * 1000)::float8 AS user_max
  FROM first_ok
 GROUP BY channel`

// LatencyDetailStats 返回各通道 all/fresh 与五段拆分。
//
// fresh 只统计最近 freshFor 内采集、且未标记 backfill 的事件；
// 历史事件缺少 collected_at 时不会进入 fresh，也不会被伪造成 0。
func (s *Stats) LatencyDetailStats(ctx context.Context, from, to time.Time, freshFor time.Duration) (map[string]LatencyDetail, error) {
	if ctx == nil || to.Before(from) || freshFor <= 0 {
		return nil, store.ErrInvalid
	}
	all, err := queryLatencyDetail(ctx, s.db, from, to, "all", time.Time{})
	if err != nil {
		return nil, err
	}
	fresh, err := queryLatencyDetail(ctx, s.db, from, to, "fresh", to.Add(-freshFor))
	if err != nil {
		return nil, err
	}
	out := make(map[string]LatencyDetail, len(all))
	for channel, detail := range all {
		out[channel] = detail
	}
	for channel, detail := range fresh {
		item := out[channel]
		item.Fresh = detail.All
		out[channel] = item
	}
	return out, nil
}

func queryLatencyDetail(ctx context.Context, db *gorm.DB, from, to time.Time, mode string, freshFrom time.Time) (map[string]LatencyDetail, error) {
	if mode != "all" && mode != "fresh" {
		return nil, store.ErrInvalid
	}
	var rows []struct {
		Channel     string  `gorm:"column:channel"`
		N           int64   `gorm:"column:n"`
		CollectN    int64   `gorm:"column:collect_n"`
		QueueN      int64   `gorm:"column:queue_n"`
		GenerateN   int64   `gorm:"column:generate_n"`
		SendN       int64   `gorm:"column:send_n"`
		UserN       int64   `gorm:"column:user_n"`
		P50         float64 `gorm:"column:p50_ms"`
		P90         float64 `gorm:"column:p90_ms"`
		P99         float64 `gorm:"column:p99_ms"`
		Avg         float64 `gorm:"column:avg_ms"`
		Max         float64 `gorm:"column:max_ms"`
		CollectP50  float64 `gorm:"column:collect_p50"`
		CollectP90  float64 `gorm:"column:collect_p90"`
		CollectP99  float64 `gorm:"column:collect_p99"`
		CollectAvg  float64 `gorm:"column:collect_avg"`
		CollectMax  float64 `gorm:"column:collect_max"`
		QueueP50    float64 `gorm:"column:queue_p50"`
		QueueP90    float64 `gorm:"column:queue_p90"`
		QueueP99    float64 `gorm:"column:queue_p99"`
		QueueAvg    float64 `gorm:"column:queue_avg"`
		QueueMax    float64 `gorm:"column:queue_max"`
		GenerateP50 float64 `gorm:"column:generate_p50"`
		GenerateP90 float64 `gorm:"column:generate_p90"`
		GenerateP99 float64 `gorm:"column:generate_p99"`
		GenerateAvg float64 `gorm:"column:generate_avg"`
		GenerateMax float64 `gorm:"column:generate_max"`
		SendP50     float64 `gorm:"column:send_p50"`
		SendP90     float64 `gorm:"column:send_p90"`
		SendP99     float64 `gorm:"column:send_p99"`
		SendAvg     float64 `gorm:"column:send_avg"`
		SendMax     float64 `gorm:"column:send_max"`
		UserP50     float64 `gorm:"column:user_p50"`
		UserP90     float64 `gorm:"column:user_p90"`
		UserP99     float64 `gorm:"column:user_p99"`
		UserAvg     float64 `gorm:"column:user_avg"`
		UserMax     float64 `gorm:"column:user_max"`
	}
	if err := db.WithContext(ctx).Raw(latencyDetailSQL, from, to, mode, freshFrom).Scan(&rows).Error; err != nil {
		return nil, &safeError{"统计分段延迟失败", err}
	}
	out := make(map[string]LatencyDetail, len(rows))
	for _, row := range rows {
		if !latencyChannels[row.Channel] || row.N == 0 {
			continue
		}
		out[row.Channel] = LatencyDetail{
			All: window(row.N, row.P50, row.P90, row.P99, row.Avg, row.Max),
			Segments: LatencySegments{
				Collect:     window(row.CollectN, row.CollectP50, row.CollectP90, row.CollectP99, row.CollectAvg, row.CollectMax),
				Queue:       window(row.QueueN, row.QueueP50, row.QueueP90, row.QueueP99, row.QueueAvg, row.QueueMax),
				Generate:    window(row.GenerateN, row.GenerateP50, row.GenerateP90, row.GenerateP99, row.GenerateAvg, row.GenerateMax),
				Send:        window(row.SendN, row.SendP50, row.SendP90, row.SendP99, row.SendAvg, row.SendMax),
				E2EUser:     window(row.UserN, row.UserP50, row.UserP90, row.UserP99, row.UserAvg, row.UserMax),
				E2EPlatform: window(row.N, row.P50, row.P90, row.P99, row.Avg, row.Max),
			},
		}
	}
	return out, nil
}

func window(n int64, p50, p90, p99, avg, max float64) LatencyWindow {
	return LatencyWindow{
		Count: n, P50MS: roundMS(p50), P90MS: roundMS(p90), P99MS: roundMS(p99),
		AvgMS: roundMS(avg), MaxMS: roundMS(max), SampleSufficient: n >= 30,
	}
}

// roundMS 把毫秒浮点夹到非负整数；NaN/Inf 属于上游算错，按 0 处理。
func roundMS(v float64) int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return int64(math.Round(v))
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
