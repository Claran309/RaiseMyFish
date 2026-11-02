package config

import (
	"errors"
	"math"
)

// Proactive 是「每 interval_seconds 看画面说一句」的旁路配置（`[proactive]`）。
//
// 默认关闭：主动发弹幕有频控/封号风险，必须先显式开启。
// 画面变化检测与去重发生在 internal/proactive，这里只做静态校验。
type Proactive struct {
	Enabled          bool    `toml:"enabled"`
	IntervalSeconds  int     `toml:"interval_seconds"`
	SendGapSeconds   int     `toml:"send_gap_seconds"`
	MaxPerHour       int     `toml:"max_per_hour"`
	MinChangePercent float64 `toml:"min_change_percent"`
}

func defaultProactive() Proactive {
	return Proactive{IntervalSeconds: 20, SendGapSeconds: 30, MaxPerHour: 6, MinChangePercent: 5}
}

// Validate 只检查参数范围；凭据、直播流与发送通道在装配阶段检查。
func (p Proactive) Validate() error {
	if !p.Enabled {
		return nil
	}
	if p.IntervalSeconds < 5 || p.IntervalSeconds > 3600 {
		return errors.New("主动弹幕间隔必须在5至3600秒之间")
	}
	if p.SendGapSeconds < 1 || p.SendGapSeconds > 3600 {
		return errors.New("主动弹幕发送间隔必须在1至3600秒之间")
	}
	if p.MaxPerHour < 1 || p.MaxPerHour > 60 {
		return errors.New("主动弹幕每小时上限必须在1至60之间")
	}
	if math.IsNaN(p.MinChangePercent) || math.IsInf(p.MinChangePercent, 0) || p.MinChangePercent < 0 || p.MinChangePercent > 100 {
		return errors.New("主动弹幕变化阈值必须在0至100之间")
	}
	return nil
}
