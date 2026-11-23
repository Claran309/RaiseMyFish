package database

// 本文件提供「同用户短窗口合并」的只读判断（L3.6）。
//
// 语义：当前事件之后、窗口之内，同一通道/会话/用户还有一条待处理入向事件时，
// 当前事件可以被跳过——后一条回复的上下文中已经能看到前一条（会话 Recent 或
// 直播 viewerRecent），只回一次比连回两次更自然。

import (
	"context"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

// NewerSameUser 判断窗口内是否还有同一用户的更新待处理事件。
func (r *SchedulerRepo) NewerSameUser(ctx context.Context, c event.Claim, window time.Duration) (bool, error) {
	if ctx == nil || !validClaim(c) || window <= 0 || window > time.Hour {
		return false, store.ErrInvalid
	}
	if c.Event.Channel == event.ChLive && c.Event.Private {
		return false, nil
	}
	var exists bool
	err := r.db.WithContext(ctx).Raw(`
SELECT EXISTS (
  SELECT 1 FROM events
   WHERE channel = ? AND session = ? AND from_id = ?
     AND dir = 'in' AND id <> ?
     AND status IN ('pending','queued')
     AND at > ? AND at <= ?
)`, c.Event.Channel, c.Event.Session, c.Event.FromID, c.Event.ID,
		c.Event.At, c.Event.At.Add(window)).Scan(&exists).Error
	if err != nil {
		return false, &safeError{"查询同用户合并窗口失败", err}
	}
	return exists, nil
}
