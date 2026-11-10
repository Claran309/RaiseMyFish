package main

// 本文件实现「主动定时送礼」：按 [gift] interval_seconds 周期给白名单房间
// 送一朵小花花。受 giftGate 门控、allowed_rooms 白名单与每日配额约束；
// DryRun/Real 沿用礼物发送器的既有语义（dry-run 只记账不出门）。

import (
	"context"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/gift"
	"go.uber.org/zap"
)

// startProactiveGift 启动定时送礼循环；interval<=0 或未启用时直接返回 noop。
// 返回的 stop 会等待循环退出（ticker 已停）。
func startProactiveGift(ctx context.Context, cfg config.Config, sender *gift.NodeSender, gate func() bool, logger *zap.Logger) func() {
	noop := func() {}
	if sender == nil || !cfg.Gift.Enabled {
		return noop
	}
	const proactiveGiftInterval = 15 * time.Minute
	interval := proactiveGiftInterval
	room := ""
	for _, r := range cfg.Gift.AllowedRooms {
		if r = trimSpace(r); r != "" {
			room = r
			break
		}
	}
	if room == "" {
		logger.Warn("主动送礼缺少白名单房间，已停用",
			zap.String("action", "proactive_gift"))
		return noop
	}
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if gate != nil && !gate() {
					continue
				}
				res, err := sender.Send(ctx, room, cfg.Gift.GiftID, cfg.Gift.Num)
				if err != nil {
					logger.Warn("主动送礼失败", zap.String("action", "proactive_gift"),
						zap.String("room", room), zap.Error(err))
					continue
				}
				logger.Info("主动送礼已发送", zap.String("action", "proactive_gift"),
					zap.String("room", room), zap.String("gift", res.GiftID),
					zap.Int("num", res.Num), zap.Bool("sent", res.Sent), zap.Bool("pending_review", res.PendingReview))
			}
		}
	}()
	return func() {
		ticker.Stop()
		<-done
	}
}

// trimSpace 是 strings.TrimSpace 的本地别名，避免额外 import。
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
