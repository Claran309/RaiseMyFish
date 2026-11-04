package config

import "errors"

// DMCollector 是私信采集配置（`[collector.dm]`）。
//
// 默认关：私信风控极严，必须显式开启。
type DMCollector struct {
	Enabled         bool `toml:"enabled"`
	IntervalSeconds int  `toml:"interval_seconds"`
}

func defaultDMCollector() DMCollector {
	return DMCollector{Enabled: false, IntervalSeconds: 120}
}

// Validate 只检查区间。
func (d DMCollector) Validate() error {
	if !d.Enabled {
		return nil
	}
	if d.IntervalSeconds < 10 || d.IntervalSeconds > 3600 {
		return errors.New("私信轮询间隔必须在10至3600秒之间")
	}
	return nil
}

// DMSender 是私信发送配置（`[sender.dm]`）。
//
// 默认关；白名单是硬护栏（主人 mid 由装配层并入）。
type DMSender struct {
	Enabled     bool     `toml:"enabled"`
	AllowedMIDs []string `toml:"allowed_mids"`
	// StatePath 是「当日收信人」状态文件（跨重启保留），空则用 state/dm-recipient.json。
	StatePath string `toml:"state_path"`
}

// Validate 只检查白名单格式。
func (d DMSender) Validate() error {
	if !d.Enabled {
		return nil
	}
	if len(d.AllowedMIDs) == 0 {
		return errors.New("私信发送开启时必须配置 allowed_mids（至少主人）")
	}
	for _, mid := range d.AllowedMIDs {
		if mid == "" || len(mid) > 32 {
			return errors.New("私信白名单 mid 非法")
		}
		for _, r := range mid {
			if r < '0' || r > '9' {
				return errors.New("私信白名单 mid 必须是纯数字")
			}
		}
	}
	return nil
}
