// Package observe 提供可观测层的纯渲染函数（日报等）。
//
// 本包刻意只做「数据 → 文本」：不查库、不调模型、不写文件、不读配置与环境变量。
// 统计数据的采集由调用方（repository + fishctl 装配）负责，
// 这样渲染逻辑可以用固定快照单测，不会因为环境漂移而变得不可断言。
package observe

// 本文件实现 0.10a 的日报渲染（plans/phase-0.10-workbuddy-dispatch.md）。

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// timeLayout 是日报里唯一的时间格式；固定写死，不做本地时区转换。
const timeLayout = "2006-01-02 15:04"

// Snapshot 是一段时间窗口内的统计快照（由调用方从 repository 查询后传入）。
type Snapshot struct {
	// WindowStart / WindowEnd 统计窗口；按传入的时间字面格式化，不转换时区。
	WindowStart time.Time
	WindowEnd   time.Time
	// Received 收到事件数。
	Received int
	// Replied 已回复数。
	Replied int
	// Skipped 跳过数（噪音/ACL/频控等主动不回复）。
	Skipped int
	// Failed 失败数。
	Failed int
	// KnownCostUSD 是窗口内已计价调用的费用合计。
	// nil 表示没有已计价调用，与 0（明确免费）严格区分。
	KnownCostUSD *float64
	// UnknownCalls 是费用未知（未配置报价）的调用数。
	UnknownCalls int
	// TotalCalls 是窗口内的调用总数；与 Web 面板同一口径。
	TotalCalls int
	// Channels 按通道细分；key 为通道名（live/comment/dm/qq）。
	Channels map[string]ChannelStat
}

// ChannelStat 是单个通道的计数。
type ChannelStat struct {
	Received int
	Replied  int
	Skipped  int
	Failed   int
}

// Render 把统计快照渲染成日报文本。
//
// 性质：纯函数、确定性——同一输入永远得到同一输出（通道按名称排序，不依赖 map 遍历顺序）。
// 输出为 4 行 Markdown，末尾不带换行：
//
//	# 日报 2026-11-01 00:00 ~ 2026-11-01 23:59
//	- 收到 120 / 回复 85 / 跳过 30 / 失败 5
//	- 成功率 94.4%
//	- 费用：$0.0123
//	- 通道：comment 20/15/5/0；live 100/70/25/5
func Render(s Snapshot) string {
	var b strings.Builder
	// 粗略预估容量，避免多次扩容；不影响输出内容
	b.Grow(256)

	b.WriteString("# 日报 ")
	b.WriteString(s.WindowStart.Format(timeLayout))
	b.WriteString(" ~ ")
	b.WriteString(s.WindowEnd.Format(timeLayout))
	b.WriteString("\n")

	fmt.Fprintf(&b, "- 收到 %d / 回复 %d / 跳过 %d / 失败 %d\n", s.Received, s.Replied, s.Skipped, s.Failed)

	b.WriteString("- 成功率 ")
	b.WriteString(renderRate(s))
	b.WriteString("\n")

	b.WriteString("- 费用：")
	b.WriteString(renderCost(s.KnownCostUSD, s.UnknownCalls, s.TotalCalls))
	b.WriteString("\n")

	b.WriteString("- 通道：")
	b.WriteString(renderChannels(s.Channels))

	return b.String()
}

// renderRate 渲染成功率。
//
// 分母是「收到 - 跳过」：跳过是主动不回复（噪音/ACL/频控），不该拉低回复质量。
// 分母 <= 0 时没有可统计样本，显示「—」而不是 0% 或 NaN——0% 会被误读成「全都失败了」。
func renderRate(s Snapshot) string {
	denominator := s.Received - s.Skipped
	if denominator <= 0 {
		return "—"
	}
	rate := float64(s.Replied) / float64(denominator) * 100
	// 防御：回复数的统计口径与「收到-跳过」不完全同源时（跨窗口、历史遗留行），
	// 分子可能大于分母。成功率超过 100% 只会让人怀疑日报本身，这里夹到 [0,100]。
	if rate > 100 {
		rate = 100
	}
	if rate < 0 {
		rate = 0
	}
	return strconv.FormatFloat(rate, 'f', 1, 64) + "%"
}

// renderCost 渲染费用，口径与 Web 面板一致：known_cost / unknown / total。
//
// 必须分清：
//   - total=0        → 窗口内没有调用，费用无意义；
//   - known=nil      → 有调用但全部未计价（未配置报价），不能伪造成 0；
//   - known 非 0/0   → 已计价合计；0 是明确免费（例如本地模型）；
//   - unknown>0      → 混合账本，只能展示已知部分并标注未知条数。
//
// NaN/Inf 属于上游算错，单独标成「报价异常」，避免输出 "$NaN"。
func renderCost(known *float64, unknown, total int) string {
	if unknown < 0 {
		unknown = 0
	}
	if total < unknown {
		total = unknown
	}
	if total == 0 {
		if known == nil {
			return "未知（窗口内无调用）"
		}
		// 上游只给了费用、没给调用数：保留金额，不编造次数。
		return formatCost(known) + "（已计价次数未知）"
	}
	priced := total - unknown
	if known == nil && unknown == 0 {
		// 不一致的数据：没有未知调用却没有合计，按未知处理。
		return fmt.Sprintf("未知（账本合计缺失，共 %d 次）", total)
	}
	if known == nil {
		return fmt.Sprintf("未知（未配置报价，%d 次调用均未计价）", total)
	}
	if value := *known; math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Sprintf("未知（报价异常，已计价 %d/%d 次）", priced, total)
	}
	if unknown == 0 {
		return fmt.Sprintf("%s（已计价 %d/%d 次）", formatCost(known), priced, total)
	}
	return fmt.Sprintf("已知 %s（已计价 %d/%d 次，未知 %d 次）", formatCost(known), priced, total, unknown)
}

// formatCost 输出美元金额：0 记作 "$0"，其余按最少位数输出（0.0123 不补成 0.012300）。
func formatCost(cost *float64) string {
	if cost == nil {
		return "未知"
	}
	value := *cost
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "未知（报价异常）"
	}
	if value == 0 {
		return "$0"
	}
	return "$" + strconv.FormatFloat(value, 'f', -1, 64)
}

// renderChannels 按通道名排序渲染细分。
//
// 排序是**必须**的：Go 的 map 遍历顺序随机，不排序会让「同一输入两次渲染结果不同」，
// 直接违反确定性与日报可比性。
func renderChannels(channels map[string]ChannelStat) string {
	if len(channels) == 0 {
		return "无"
	}

	names := make([]string, 0, len(channels))
	for name := range channels {
		names = append(names, name)
	}
	slices.Sort(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		stat := channels[name]
		parts = append(parts, fmt.Sprintf("%s %d/%d/%d/%d", name, stat.Received, stat.Replied, stat.Skipped, stat.Failed))
	}
	return strings.Join(parts, "；")
}
