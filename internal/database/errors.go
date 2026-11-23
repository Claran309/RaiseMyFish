package database

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

const maxErrorMessageRunes = 500

// ErrorRepo 实现 event_errors 的写入与死信聚合。
type ErrorRepo struct{ db *gorm.DB }

func NewErrorRepo(db *gorm.DB) (*ErrorRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &ErrorRepo{db: db}, nil
}

var _ store.ErrorRepo = (*ErrorRepo)(nil)

// Record 只写安全摘要；超长消息按 rune 截断，避免把完整错误链或正文写爆。
func (r *ErrorRepo) Record(ctx context.Context, item store.EventError) error {
	if ctx == nil || strings.TrimSpace(item.EventID) == "" || strings.TrimSpace(item.Stage) == "" {
		return store.ErrInvalid
	}
	kind := strings.TrimSpace(item.ErrorKind)
	if kind == "" {
		kind = "internal"
	}
	message := strings.TrimSpace(item.Message)
	if utf8.RuneCountInString(message) > maxErrorMessageRunes {
		message = string([]rune(message)[:maxErrorMessageRunes])
	}
	err := r.db.WithContext(ctx).Exec(`INSERT INTO event_errors(event_id,stage,error_kind,message) VALUES(?,?,?,?)`,
		item.EventID, strings.TrimSpace(item.Stage), kind, message).Error
	if err != nil {
		return &safeError{"写入事件错误失败", err}
	}
	return nil
}

// DeadStats 按窗口聚合 db 中已进入 dead 的事件；limit 控制分桶数量。
func (r *ErrorRepo) DeadStats(ctx context.Context, from, to time.Time, limit int) ([]store.DeadStat, error) {
	if ctx == nil || to.Before(from) || limit < 1 || limit > 100 {
		return nil, store.ErrInvalid
	}
	return deadStats(ctx, r.db, from, to, limit)
}

func deadStats(ctx context.Context, db *gorm.DB, from, to time.Time, limit int) ([]store.DeadStat, error) {
	var rows []struct {
		Channel   string
		Stage     string
		ErrorKind string `gorm:"column:error_kind"`
		Count     int64
	}
	err := db.WithContext(ctx).Raw(`
SELECT e.channel,
       x.stage,
       x.error_kind,
       COUNT(*) AS count
  FROM event_errors x
  JOIN events e ON e.id = x.event_id
 WHERE e.status = 'dead' AND e.dir = 'in'
   AND x.created_at >= ? AND x.created_at < ?
 GROUP BY e.channel, x.stage, x.error_kind
 ORDER BY count DESC, e.channel, x.stage, x.error_kind
 LIMIT ?`, from, to, limit).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"统计死信分布失败", err}
	}
	out := make([]store.DeadStat, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.DeadStat{Channel: row.Channel, Stage: row.Stage, ErrorKind: row.ErrorKind, Count: row.Count})
	}
	return out, nil
}
