package main

// 本文件把仓库统计与通道心跳渲染进 `fishctl status`（0.10c/0.10d）。
//
// 边界：
//   - 统计查询在 internal/database（stats.go），本文件只做装配与渲染；
//   - 渲染完全交给 internal/observe 的纯函数（Render / Check）；
//   - 没有数据库时保持 0.1 的既有输出不变（不新增字段、不改语义）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/observe"
)

// DefaultStatsWindow 是 `fishctl status` 的默认统计窗口。
const DefaultStatsWindow = 24 * time.Hour

// StatsSource 是 status 依赖的最小统计接口，由 database.Stats 实现。
type StatsSource interface {
	// Counts 返回 [from, to) 窗口内的计数。
	Counts(ctx context.Context, from, to time.Time) (database.WindowCounts, error)
	// LastSeen 返回各通道最后一次收到事件的时间；从未收到事件的通道不出现。
	LastSeen(ctx context.Context) (map[string]time.Time, error)
}

// 编译期断言：真实聚合实现必须满足本文件依赖的接口。
var _ StatsSource = (*database.Stats)(nil)

// StatusStats 是渲染后的统计片段，作为 `stats` 字段并入 status 的 JSON 输出。
type StatusStats struct {
	// Report 是 observe.Render 的日报文本（多行，JSON 里会转义换行）。
	Report string `json:"report"`
	// HeartbeatWarnings 是 observe.Check 的告警，每条一行、已按通道排序。
	HeartbeatWarnings []string `json:"heartbeat_warnings"`
}

// empty 判断是否没有任何可输出的统计内容。
func (s StatusStats) empty() bool {
	return s.Report == "" && len(s.HeartbeatWarnings) == 0
}

// newStatsSource 从已建立的基础设施装配真实统计来源。
//
// resources 为 nil（数据库没连上）时返回 nil 且不报错：
// 此时 status 的主错误已经由 probeErr 报了，不需要再叠一条。
func newStatsSource(resources *infra.Resources) (StatsSource, error) {
	if resources == nil || resources.DB == nil {
		return nil, nil
	}
	stats, err := database.NewStats(resources.DB)
	if err != nil {
		return nil, fmt.Errorf("装配统计来源失败：%w", err)
	}
	return stats, nil
}

// collectStats 读取统计与心跳并渲染。
//
// src 为 nil 表示「统计来源未装配」：返回零值、不报错，
// 这样 status 的输出与之前完全一致（新增字段不出现）。
// 真正的查询错误必须向上抛，不能吞成「没有数据」。
func collectStats(ctx context.Context, src StatsSource, now time.Time, window time.Duration) (StatusStats, error) {
	if src == nil {
		return StatusStats{}, nil
	}
	if ctx == nil {
		return StatusStats{}, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return StatusStats{}, err
	}
	if window <= 0 {
		window = DefaultStatsWindow
	}

	from, to := now.Add(-window), now
	counts, err := src.Counts(ctx, from, to)
	if err != nil {
		return StatusStats{}, fmt.Errorf("读取统计失败：%w", err)
	}
	lastSeen, err := src.LastSeen(ctx)
	if err != nil {
		return StatusStats{}, fmt.Errorf("读取通道心跳失败：%w", err)
	}

	snapshot := observe.Snapshot{
		WindowStart:  from,
		WindowEnd:    to,
		Received:     counts.Received,
		Replied:      counts.Replied,
		Skipped:      counts.Skipped,
		Failed:       counts.Failed,
		KnownCostUSD: counts.KnownCostUSD,
		UnknownCalls: counts.UnknownCalls,
		TotalCalls:   counts.TotalCalls,
		Channels:     toChannelStats(counts.Channels),
	}

	return StatusStats{
		Report:            observe.Render(snapshot),
		HeartbeatWarnings: formatHeartbeat(observe.Check(now, lastSeen)),
	}, nil
}

// toChannelStats 把存储层的通道计数转成渲染层的类型（两边刻意不共用同一个结构）。
func toChannelStats(in map[string]database.ChannelCounts) map[string]observe.ChannelStat {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]observe.ChannelStat, len(in))
	for name, c := range in {
		out[name] = observe.ChannelStat{
			Received: c.Received,
			Replied:  c.Replied,
			Skipped:  c.Skipped,
			Failed:   c.Failed,
		}
	}
	return out
}

// formatHeartbeat 把告警转成一行一条的文本；没有告警时返回 nil（JSON 里不出现空数组）。
func formatHeartbeat(warnings []observe.Warning) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, w.String())
	}
	return out
}
