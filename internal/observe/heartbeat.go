package observe

// 本文件实现 0.10b 的通道心跳检测（纯逻辑）。
//
// 判据：某个通道「超过 SilenceThreshold 没有收到事件」就是静默失败信号——
// 采集器掉线最典型的表现不是报错，而是安静。

import (
	"fmt"
	"slices"
	"time"
)

// SilenceThreshold 是静默告警阈值：超过它没有事件就告警。
//
// 30 分钟来自 plans/phase-0.md「单通道 30 分钟无事件 → warn」。
const SilenceThreshold = 30 * time.Minute

// Warning 是一条通道静默告警。
type Warning struct {
	// Channel 通道名（live / comment / dm / qq）。
	Channel string
	// LastSeen 该通道最后一次事件时间；零值表示从未收到过。
	LastSeen time.Time
	// Silent 静默时长；LastSeen 为零值时恒为 0（无法计算）。
	Silent time.Duration
}

// String 输出一条可直接进日志的告警文本。
func (w Warning) String() string {
	if w.LastSeen.IsZero() {
		return fmt.Sprintf("通道 %s 从未收到事件", w.Channel)
	}
	return fmt.Sprintf("通道 %s 已静默 %s（最后事件 %s）",
		w.Channel, formatSilent(w.Silent), w.LastSeen.Format(timeLayout))
}

// Check 判断哪些通道已经静默超时，返回按通道名升序排列的告警。
//
// 语义约定：
//   - now 由调用方注入，函数内**不读系统时钟**，因此同一输入永远同一输出；
//   - 严格「超过」阈值才告警：正好 30 分钟不告警（边界见测试）；
//   - 零值 LastSeen 视为「从未收到事件」，直接告警，且不参与时长计算
//     （time.Time 零值是公元 1 年，硬套 now.Sub 会溢出成垃圾值）；
//   - 未来时间（对端时钟漂移）按「刚有事件」处理，不告警；
//   - 空 map / nil map 返回 nil。
//
// 不阻塞、不开 goroutine、不 panic。
func Check(now time.Time, lastSeen map[string]time.Time) []Warning {
	if len(lastSeen) == 0 {
		return nil
	}

	// 排序是必须的：map 遍历顺序随机，不排序会让同一输入产出不同顺序的告警列表
	channels := make([]string, 0, len(lastSeen))
	for name := range lastSeen {
		channels = append(channels, name)
	}
	slices.Sort(channels)

	var warnings []Warning
	for _, name := range channels {
		at := lastSeen[name]

		if at.IsZero() {
			warnings = append(warnings, Warning{Channel: name})
			continue
		}

		silent := now.Sub(at)
		if silent <= SilenceThreshold {
			continue
		}
		warnings = append(warnings, Warning{Channel: name, LastSeen: at, Silent: silent})
	}
	return warnings
}

// formatSilent 把静默时长写成人看得懂的形式。
//
// 一律带「≥」：时长按整分钟/整秒**截断**，真实值只会更大（30 分 1 秒显示「≥30 分钟」），
// 加前缀才不会让人误以为「正好 30 分钟、还没超阈值」。
func formatSilent(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("≥%d 秒", int(d.Seconds()))
	}
	return fmt.Sprintf("≥%d 分钟", int(d.Minutes()))
}
