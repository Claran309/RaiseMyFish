package database

// 时间窗口公开事件查询：供梗白学习聚合候选。
//
// 边界：
//   - 私密事件在 SQL 层排除，学习结果只来自公开入向事件；
//   - 必须有非零起始时间与通道，limit 有上限，避免全表扫描。

import (
	"context"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

// maxSinceEvents 是单次窗口查询上限；超过部分由调用方按「最近」口径截断。
const maxSinceEvents = 2000

// PublicSince 返回时间窗口内某通道的公开入向事件，按时间倒序。
func (r *EventRepo) PublicSince(ctx context.Context, ch event.Channel, since time.Time, limit int) ([]event.Event, error) {
	if ctx == nil || !ch.Valid() || since.IsZero() || limit < 1 || limit > maxSinceEvents {
		return nil, store.ErrInvalid
	}
	var rows []eventRow
	// DM 即使被旧数据错误降密，也不能进入公开学习窗口。
	err := r.db.WithContext(ctx).Table("events").
		Where("channel = ? AND channel <> ? AND dir = ? AND private = ? AND at >= ?", ch, event.ChDM, event.DirIn, false, since).
		Order("at DESC, id DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, &safeError{"读取窗口事件失败", err}
	}
	out := make([]event.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.event())
	}
	return out, nil
}
