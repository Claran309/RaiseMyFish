package main

// 本文件装配 #93 重复度触发：别名快照 + 公开事件窗口 + 观众昵称 + 事实仓储。
//
// 别名表缺失/损坏只降级为「没有人工别名」，档案名与 @ 目标照样统计；
// 阈值关闭时完全不装配，worker 不会为此多查一次库。

import (
	"strings"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
)

// repeatAliasAdapter 把 viewer 的热更新别名快照收敛成重复度统计需要的形状。
type repeatAliasAdapter struct {
	source viewer.AliasSource
}

func (a repeatAliasAdapter) ConfirmedAliases() []memory.RepeatAlias {
	if a.source == nil {
		return nil
	}
	index := a.source.AliasIndex()
	out := make([]memory.RepeatAlias, 0, len(index.Confirmed))
	for _, alias := range index.Confirmed {
		out = append(out, memory.RepeatAlias{Alias: alias.Alias, UID: alias.UID, Name: alias.Name})
	}
	return out
}

// buildRepeatPromotion 构造重复度触发依赖；关闭或关键依赖缺失时返回 nil。
func buildRepeatPromotion(cfg config.Config, events extractworker.RepeatEventSource, names extractworker.RepeatNameSource, facts store.RepeatFactStore, aliasSource viewer.AliasSource, logger *zap.Logger) *extractworker.RepeatPromotion {
	if cfg.Memory.RepeatPromoteThreshold < 2 {
		return nil
	}
	if events == nil || facts == nil {
		logger.Warn("重复度触发缺少事件来源或事实仓储，跳过", zap.String("action", "memory_repeat"))
		return nil
	}
	var aliases extractworker.RepeatAliasSource
	if aliasSource != nil {
		aliases = repeatAliasAdapter{source: aliasSource}
	}
	return &extractworker.RepeatPromotion{
		Events:      events,
		Aliases:     aliases,
		Names:       names,
		Facts:       facts,
		Threshold:   cfg.Memory.RepeatPromoteThreshold,
		Window:      memory.RepeatWindowDays * 24 * time.Hour,
		Debounce:    24 * time.Hour,
		IgnoreNames: repeatIgnoreNames(cfg.Scheduler),
	}
}

// repeatIgnoreNames 汇总鱼自己的名字：弹幕里叫鱼不算观众称呼。
func repeatIgnoreNames(scheduler *config.Scheduler) []string {
	if scheduler == nil {
		return nil
	}
	seen := make(map[string]bool, len(scheduler.Channels))
	out := make([]string, 0, len(scheduler.Channels))
	for _, policy := range scheduler.Channels {
		for _, name := range policy.SelfNames {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}
