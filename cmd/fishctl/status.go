package main

// 本文件把仓库统计与通道心跳渲染进 `fishctl status`（0.10c）。
//
// 边界：
//   - 只消费接口，不写 SQL、不改 repository 实现（真实统计实现属 0.10d）；
//   - 渲染完全交给 internal/observe 的纯函数（Render / Check），本文件只做装配；
//   - 未装配统计来源时保持 0.1 的既有输出不变（不新增字段、不改语义）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"RaiseMyFish/internal/observe"
)

// DefaultStatsWindow 是 `fishctl status` 的默认统计窗口。
const DefaultStatsWindow = 24 * time.Hour

// WindowCounts 是统计窗口内的计数汇总。
//
// 字段与 observe.Snapshot 对齐，但刻意独立：repository 不需要知道渲染细节。
type WindowCounts struct {
	// Received / Replied / Skipped / Failed 是窗口内的总量。
	Received int
	Replied  int
	Skipped  int
	Failed   int
	// CostUSD 为 nil 表示费用未知（不能当成 0）。
	CostUSD *float64
	// Channels 是按通道的细分。
	Channels map[string]observe.ChannelStat
}

// StatsSource 是 status 依赖的最小统计接口。
//
// 实现方（repository 层）负责 SQL 统计与隐私口径；本文件只调用。
type StatsSource interface {
	// Counts 返回 [from, to) 窗口内的计数。
	Counts(ctx context.Context, from, to time.Time) (WindowCounts, error)
	// LastSeen 返回各通道最后一次事件时间；从未收到事件的通道给零值。
	LastSeen(ctx context.Context) (map[string]time.Time, error)
}

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

// collectStats 读取统计与心跳并渲染。
//
// src 为 nil 表示「统计来源尚未装配」：返回零值、不报错，
// 这样 status 的输出与 0.1 完全一致（新增字段不出现）。
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
		WindowStart: from,
		WindowEnd:   to,
		Received:    counts.Received,
		Replied:     counts.Replied,
		Skipped:     counts.Skipped,
		Failed:      counts.Failed,
		CostUSD:     counts.CostUSD,
		Channels:    counts.Channels,
	}

	return StatusStats{
		Report:            observe.Render(snapshot),
		HeartbeatWarnings: formatHeartbeat(observe.Check(now, lastSeen)),
	}, nil
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
