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
	"RaiseMyFish/internal/store"
)

// maxRecentEvents 是单次查询上限。
const maxRecentEvents = 50

// RecentEvents 返回某会话最近的事件，按时间倒序。
func (r *EventRepo) RecentEvents(ctx context.Context, ch event.Channel, session string, limit int) ([]event.Event, error) {
	if ctx == nil || !ch.Valid() || strings.TrimSpace(session) == "" || limit < 1 || limit > maxRecentEvents {
		return nil, store.ErrInvalid
	}
	var rows []eventRow
	err := r.db.WithContext(ctx).Table("events").
		Where("channel = ? AND session = ? AND dir = ?", ch, event.DirIn, session).
		Order("at DESC, id DESC").
		Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取最近事件失败", err}
	}
	out := make([]event.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.event())
	}
	return out, nil
}
