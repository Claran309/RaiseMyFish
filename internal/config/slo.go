package config

import "fmt"

// SLO 是展示与告警目标，不阻塞业务；数值单位见字段名。
type SLO struct {
	LiveP99MS         int     `toml:"live_p99_ms"`
	CommentFreshP90MS int     `toml:"comment_fresh_p90_ms"`
	DMFreshP90MS      int     `toml:"dm_fresh_p90_ms"`
	QueueOldestMS     int     `toml:"queue_oldest_ms"`
	CommentDeadRate   float64 `toml:"comment_dead_rate"`
	DMDeadRate        float64 `toml:"dm_dead_rate"`
}

func defaultSLO() SLO {
	return SLO{
		LiveP99MS: 3000, CommentFreshP90MS: 60000, DMFreshP90MS: 90000,
		QueueOldestMS: 300000, CommentDeadRate: 0.05, DMDeadRate: 0.02,
	}
}

// Validate 拒绝负目标与越界死信率；0 表示关闭对应目标。
func (s SLO) Validate() error {
	for name, value := range map[string]int{
		"live_p99_ms": s.LiveP99MS, "comment_fresh_p90_ms": s.CommentFreshP90MS,
		"dm_fresh_p90_ms": s.DMFreshP90MS, "queue_oldest_ms": s.QueueOldestMS,
	} {
		if value < 0 {
			return fmt.Errorf("%s不能为负", name)
		}
	}
	for name, value := range map[string]float64{"comment_dead_rate": s.CommentDeadRate, "dm_dead_rate": s.DMDeadRate} {
		if value < 0 || value > 1 {
			return fmt.Errorf("%s必须在0..1之间", name)
		}
	}
	return nil
}
