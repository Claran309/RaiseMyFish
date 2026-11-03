package config

import "errors"

// Viewer 是观众档案（`[viewer]`）配置。
type Viewer struct {
	Enabled bool `toml:"enabled"`
	// MinHits 是建档门槛：说过多少句才建档（一次路过不建）。
	MinHits int `toml:"min_hits"`
	// RecentTexts 是档案里保留的最近发言条数。
	RecentTexts int `toml:"recent_texts"`
	// RecentReplies 是档案里保留的最近回复条数。
	RecentReplies int `toml:"recent_replies"`
	// RebuildIntervalSeconds 是重建节流间隔（默认 1 小时）。
	RebuildIntervalSeconds int `toml:"rebuild_interval_seconds"`
}

func defaultViewer() Viewer {
	return Viewer{Enabled: true, MinHits: 2, RecentTexts: 15, RecentReplies: 10, RebuildIntervalSeconds: 3600}
}

// Validate 只检查区间；不检查库表是否存在。
func (v Viewer) Validate() error {
	if !v.Enabled {
		return nil
	}
	if v.MinHits < 1 || v.MinHits > 100 {
		return errors.New("观众建档门槛必须在1至100句之间")
	}
	if v.RecentTexts < 1 || v.RecentTexts > 50 {
		return errors.New("档案保留发言条数必须在1至50之间")
	}
	if v.RecentReplies < 1 || v.RecentReplies > 50 {
		return errors.New("档案保留回复条数必须在1至50之间")
	}
	if v.RebuildIntervalSeconds < 60 || v.RebuildIntervalSeconds > 86400 {
		return errors.New("档案重建间隔必须在60至86400秒之间")
	}
	return nil
}
