package main

// 去公式化：取鱼自己最近说过的话（live 通道 out 回复），用于提醒「别重复同一个说法/梗」。

import (
	"context"
	"time"

	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

// recentSelfSource 返回一个取「自己最近发言」的函数；repo 为 nil 时返回 nil（不注入）。
func recentSelfSource(repo *database.EventRepo) func(ctx context.Context, ch event.Channel, session string, limit int) []string {
	if repo == nil {
		return nil
	}
	return func(ctx context.Context, ch event.Channel, session string, limit int) []string {
		if limit <= 0 || limit > 5 {
			limit = 3
		}
		rows, err := repo.RecentReplies(ctx, ch, session, limit*2)
		if err != nil {
			return nil
		}
		out := make([]string, 0, limit)
		for _, ev := range rows {
			if ev.Dir != event.DirOut || ev.Text == "" {
				continue
			}
			out = append(out, ev.Text)
			if len(out) >= limit {
				break
			}
		}
		return out
	}
}

var _ = memory.Scope{}
var _ = time.Now

// viewerRecentSource 取该观众最近几条公开弹幕，用于实时上下文。
func viewerRecentSource(repo *database.EventRepo) func(ctx context.Context, fromID string, limit int) []string {
	if repo == nil {
		return nil
	}
	return func(ctx context.Context, fromID string, limit int) []string {
		if limit <= 0 || limit > 10 {
			limit = 5
		}
		rows, err := repo.ViewerRecent(ctx, fromID, limit)
		if err != nil {
			return nil
		}
		out := make([]string, 0, limit)
		for _, ev := range rows {
			if ev.Text == "" {
				continue
			}
			out = append(out, ev.Text)
			if len(out) >= limit {
				break
			}
		}
		return out
	}
}
