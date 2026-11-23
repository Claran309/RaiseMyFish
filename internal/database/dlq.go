package database

import (
	"context"
	"errors"
	"time"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// DLQRepo 提供死信查看与受保护重放。
type DLQRepo struct{ db *gorm.DB }

func NewDLQRepo(db *gorm.DB) (*DLQRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &DLQRepo{db: db}, nil
}

// List 返回最近的死信；unreplayed=true 时只返回未重放行。
func (r *DLQRepo) List(ctx context.Context, limit int, unreplayed bool) ([]store.DLQItem, error) {
	if ctx == nil || limit < 1 || limit > 500 {
		return nil, store.ErrInvalid
	}
	q := r.db.WithContext(ctx).Table("dlq")
	if unreplayed {
		q = q.Where("replayed IS FALSE")
	}
	var rows []struct {
		ID       int64
		EventID  string `gorm:"column:event_id"`
		Stage    string
		Error    string
		Payload  string
		At       time.Time
		Replayed bool
	}
	if err := q.Order("id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, &safeError{"读取死信失败", err}
	}
	out := make([]store.DLQItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.DLQItem{ID: row.ID, EventID: row.EventID, Stage: row.Stage, Error: row.Error, Payload: row.Payload, At: row.At, Replayed: row.Replayed})
	}
	return out, nil
}

// Replay 在确认没有投递证据后把 dead 事件重置为 pending/schedule。
//
// 幂等保护：任何 send_log 行（含失败/未知）或 armed/unknown/accepted/manual
// 尝试都视为「已碰过平台」，拒绝重放，避免重复回复。
func (r *DLQRepo) Replay(ctx context.Context, id int64) (store.DLQReplay, error) {
	if ctx == nil || id < 1 {
		return store.DLQReplay{}, store.ErrInvalid
	}
	var out store.DLQReplay
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID       int64
			EventID  string `gorm:"column:event_id"`
			Replayed bool
		}
		if err := tx.Table("dlq").Where("id=?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return store.ErrNotFound
			}
			return err
		}
		out.ID, out.EventID = row.ID, row.EventID
		if row.Replayed {
			out.Reason = "已重放"
			return nil
		}
		var ev struct{ Status string }
		if err := tx.Table("events").Select("status").Where("id=?", row.EventID).Take(&ev).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				out.Reason = "事件不存在"
				return nil
			}
			return err
		}
		if ev.Status != "dead" {
			out.Reason = "事件不是 dead 状态"
			return nil
		}
		var touched bool
		if err := tx.Raw(`SELECT
 EXISTS(SELECT 1 FROM send_log WHERE event_id=?)
 OR EXISTS(SELECT 1 FROM send_attempts WHERE event_id=? AND state IN ('armed','unknown','accepted','manual'))
 OR EXISTS(SELECT 1 FROM delivery_attempts WHERE event_id=? AND state IN ('armed','unknown','accepted','manual'))`,
			row.EventID, row.EventID, row.EventID).Scan(&touched).Error; err != nil {
			return err
		}
		if touched {
			out.Reason = "已有投递尝试，需人工核验"
			return nil
		}
		res := tx.Exec(`UPDATE events
   SET status='pending', stage='schedule', retry_at=NULL, status_changed_at=clock_timestamp(),
       lease_owner=NULL, lease_until=NULL, lease_version=lease_version+1
 WHERE id=? AND status='dead' AND dir='in'`, row.EventID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			out.Reason = "事件状态已变化"
			return nil
		}
		if err := tx.Table("dlq").Where("id=?", id).Update("replayed", true).Error; err != nil {
			return err
		}
		out.Replayed = true
		out.Reason = "已重置为 pending"
		return nil
	})
	if err != nil {
		return store.DLQReplay{}, queueFailure("重放死信失败", err)
	}
	return out, nil
}
