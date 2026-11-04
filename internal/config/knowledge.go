package config

import (
	"errors"
	"strings"
)

// Knowledge 启用只加载不可变Reader快照；不代表事件消费链路已接通。
type Knowledge struct {
	Enabled       bool   `toml:"enabled"`
	Dir           string `toml:"dir" json:"-"`
	BudgetPercent int    `toml:"budget_percent"`
	// MemeLearn 开启后从公开事件里发现新梗并追加到 memes/learned.md；默认关闭。
	MemeLearn bool `toml:"meme_learn"`
	// MemeLearnMaxPerDay 是每个自然日（UTC）最多学习的条数，持久化在 learned.md 的 ID 前缀里。
	MemeLearnMaxPerDay int `toml:"meme_learn_max_per_day"`
	// MemeLearnWindowHours 是回看窗口（小时，默认 24）。
	MemeLearnWindowHours int `toml:"meme_learn_window_hours"`
	// MemeLearnMinOccurrences 是候选最低出现次数（默认 2）。
	MemeLearnMinOccurrences int `toml:"meme_learn_min_occurrences"`
	// HotReload 开启目录轮询热重载（梗白学习写入后不必重启）。
	HotReload bool `toml:"hot_reload"`
	// ReloadIntervalSeconds 是轮询间隔（默认 30s）。
	ReloadIntervalSeconds int `toml:"reload_interval_seconds"`
}

func defaultKnowledge() Knowledge {
	return Knowledge{Dir: "personas/dafeiyu/knowledge", BudgetPercent: 10, MemeLearnMaxPerDay: 10, MemeLearnWindowHours: 24, MemeLearnMinOccurrences: 2, HotReload: true, ReloadIntervalSeconds: 30}
}

// Validate 不访问文件，目录检查由显式启用后的装配执行。
func (k Knowledge) Validate() error {
	if k.BudgetPercent < 0 || k.BudgetPercent > 10 {
		return errors.New("知识预算比例必须为0至10的整数")
	}
	if k.Enabled && strings.TrimSpace(k.Dir) == "" {
		return errors.New("知识启用需要非空目录")
	}
	if k.MemeLearn {
		if !k.Enabled {
			return errors.New("梗白学习需要先启用知识库")
		}
		if k.MemeLearnMaxPerDay < 1 || k.MemeLearnMaxPerDay > 100 {
			return errors.New("梗白学习每日上限必须在1至100之间")
		}
	}
	return nil
}
