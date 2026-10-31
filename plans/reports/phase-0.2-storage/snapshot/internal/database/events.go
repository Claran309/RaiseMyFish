package database

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EventRepo 是PG事件事实及条件迁移实现，连接生命周期归infra。
type EventRepo struct{ db *gorm.DB }

// NewEventRepo 注入现有连接，不自动迁移。
func NewEventRepo(db *gorm.DB) (*EventRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &EventRepo{db: db}, nil
}

type eventRow struct {
	ID       string
	Channel  event.Channel
	Dir      event.Dir
	Session  string
	FromID   string
	FromName string
	Text     string
	Private  bool
	ReplyTo  string
	At       time.Time
	Status   event.Status
	Meta     json.RawMessage `gorm:"type:jsonb"`
}

func rowFromEvent(e event.Event) eventRow {
	return eventRow{e.ID, e.Channel, e.Dir, e.Session, e.FromID, e.FromName, e.Text, e.Private, e.ReplyTo, e.At, e.Status, e.Meta}
}
func (r eventRow) event() event.Event {
	return event.Event{ID: r.ID, Channel: r.Channel, Dir: r.Dir, Session: r.Session, FromID: r.FromID, FromName: r.FromName, Text: r.Text, Private: r.Private, ReplyTo: r.ReplyTo, At: r.At, Status: r.Status, Meta: r.Meta}
}

// Insert 仅按主键去重，重复时不覆盖原始事实或后续状态。
func (r *EventRepo) Insert(ctx context.Context, e event.Event) (bool, error) {
	if ctx == nil {
		return false, store.ErrInvalid
	}
	if err := store.ValidateEvent(e); err != nil {
		return false, err
	}
	row := rowFromEvent(e)
	result := r.db.WithContext(ctx).Table("events").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&row)
	if result.Error != nil {
		return false, &safeError{"写入事件失败", result.Error}
	}
	return result.RowsAffected == 1, nil
}

// Get 是可信内部按ID精确读取；不能替代面向上下文的范围查询。
func (r *EventRepo) Get(ctx context.Context, id string) (event.Event, error) {
	if ctx == nil || strings.TrimSpace(id) == "" {
		return event.Event{}, store.ErrInvalid
	}
	var row eventRow
	err := r.db.WithContext(ctx).Table("events").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return event.Event{}, store.ErrNotFound
	}
	if err != nil {
		return event.Event{}, &safeError{"读取事件失败", err}
	}
	return row.event(), nil
}

// Transition 用单条条件更新做CAS；零行统一为冲突，不先读再写。
func (r *EventRepo) Transition(ctx context.Context, id string, from []event.Status, to event.Status) error {
	if ctx == nil || strings.TrimSpace(id) == "" {
		return store.ErrInvalid
	}
	if err := store.ValidateTransition(from, to); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Exec("UPDATE events SET status=? WHERE id=? AND status IN ?", to, id, from)
	if result.Error != nil {
		return &safeError{"更新事件状态失败", result.Error}
	}
	if result.RowsAffected != 1 {
		return store.ErrConflict
	}
	return nil
}
