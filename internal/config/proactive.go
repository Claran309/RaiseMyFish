package config

import "errors"

// Proactive 是「每 interval_seconds 看画面说一句」的旁路配置（`[proactive]`）。
//
// 默认关闭：主动发弹幕有频控/封号风险，必须先显式开启。
// 0.19 重构后这里只剩间隔类参数：像素变化检测与 max_per_hour 固定窗口都已删除，
// 频率由 send_gap_seconds 单点控制；use_react 决定是否允许走 ReAct 多步查证。
type Proactive struct {
	Enabled         bool `toml:"enabled"`
	IntervalSeconds int  `toml:"interval_seconds"`
	SendGapSeconds  int  `toml:"send_gap_seconds"`
	// UseReact 控制主动弹幕是否可以用 ReAct 多步查证（默认 true）。
	// 关掉时永远走单步 Brain，行为等同 0.19 第一批。
	UseReact bool `toml:"use_react"`
}

func defaultProactive() Proactive {
	return Proactive{IntervalSeconds: 20, SendGapSeconds: 30, UseReact: true}
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
	return nil
}
