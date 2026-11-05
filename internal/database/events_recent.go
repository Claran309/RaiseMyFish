package database

// 最近事件查询：只读，供 ReAct 的 history 工具使用。
//
// 边界：
//   - 只返回 dir='in' 的公开入向事件（自己发出去的不算「弹幕」）；
//   - 必须按 channel + session 限定范围，不接受空会话的全表扫描；
//   - limit 有上限，避免一次把整场直播拉进 prompt。

import (
	"context"
	"strings"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// maxRecentEvents 是单次查询上限。
const maxRecentEvents = 50

// RecentEvents 返回某会话最近的入向事件，按时间倒序。
//
// 可见性由 scope 决定：公开范围只读 public 行；私密范围只读本会话的 private 行。
// 调用方不能只凭 channel/session 推断私密权限，否则同房间的历史工具会把私密内容
// 带进公开回复。
func (r *EventRepo) RecentEvents(ctx context.Context, s memory.Scope, limit int) ([]event.Event, error) {
	if ctx == nil || limit < 1 || limit > maxRecentEvents {
		return nil, store.ErrInvalid
	}
	if err := memory.ValidateScope(s); err != nil {
		return nil, store.ErrInvalid
	}
	query := r.db.WithContext(ctx).Table("events").
		Where("channel = ? AND session = ? AND dir = ?", s.Channel, s.Session, event.DirIn)
	if s.Private {
		query = query.Where("private")
	} else {
		// 即使旧数据把 DM 错误降密，也不允许它进入公开视角。
		query = query.Where("NOT private AND channel <> ?", event.ChDM)
	}
	var rows []eventRow
	err := query.Order("at DESC, id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取最近事件失败", err}
	}
	out := make([]event.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.event())
	}
	return out, nil
}

// ViewerRecent 返回某观众最近 N 条公开入向消息（只读，供 viewer_memory 工具）。
//
// 观众历史是公开投影：DM、私密会话和错误降密的 DM 一律不返回。
// 私密认人需要显式范围授权，不能用「只传 uid」代替。
func (r *EventRepo) ViewerRecent(ctx context.Context, fromID string, limit int) ([]event.Event, error) {
	if ctx == nil || strings.TrimSpace(fromID) == "" || len(fromID) > 64 || limit < 1 || limit > maxRecentEvents {
		return nil, store.ErrInvalid
	}
	var rows []eventRow
	err := r.db.WithContext(ctx).Table("events").
		Where("from_id = ? AND dir = ? AND NOT private AND channel <> ?", fromID, event.DirIn, event.ChDM).
		Order("at DESC, id DESC").
		Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取观众历史失败", err}
	}
	out := make([]event.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.event())
	}
	return out, nil
}

// RecentReplies 返回某会话最近 N 条已确认发送的公开出向事件，供 reply_history 工具。
// retry/dead/skipped 的出事件只代表尝试过程，不算「回过」；
// 私密回复属于原私密会话，不能经这个公开工具读出。
func (r *EventRepo) RecentReplies(ctx context.Context, ch event.Channel, session string, limit int) ([]event.Event, error) {
	if ctx == nil || !ch.Valid() || strings.TrimSpace(session) == "" || limit < 1 || limit > maxRecentEvents {
		return nil, store.ErrInvalid
	}
	var rows []eventRow
	err := r.db.WithContext(ctx).Table("events").
		Where("channel = ? AND session = ? AND dir = ? AND status = ? AND NOT private", ch, session, event.DirOut, event.StatusDone).
		Order("at DESC, id DESC").
		Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取回复历史失败", err}
	}
	out := make([]event.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.event())
	}
	return out, nil
}
