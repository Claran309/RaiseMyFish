package database

import (
	"context"
	"errors"
	"strings"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TaskRepo 只管理初次排队，任务执行/发送协议不在0.2中实现。
type TaskRepo struct{ db *gorm.DB }

// NewTaskRepo 注入infra管理的连接。
func NewTaskRepo(db *gorm.DB) (*TaskRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &TaskRepo{db: db}, nil
}

// CreateForEvent 锁定事实行，核对归属后利用event_id唯一索引写入任务。
func (r *TaskRepo) CreateForEvent(ctx context.Context, t store.Task) (bool, error) {
	return r.createTask(ctx, t, nil)
}

// CreateClaimed 在同一事务内检查未过期租约、写唯一任务并切换generate阶段。
func (r *TaskRepo) CreateClaimed(ctx context.Context, c event.Claim, t store.Task) (bool, error) {
	if !validClaim(c) || c.Event.ID != t.EventID {
		return false, store.ErrInvalid
	}
	return r.createTask(ctx, t, &c)
}

func (r *TaskRepo) createTask(ctx context.Context, t store.Task, claim *event.Claim) (bool, error) {
	if ctx == nil {
		return false, store.ErrInvalid
	}
	if err := t.Validate(); err != nil {
		return false, err
	}
	inserted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row queueRow
		var err error
		if claim == nil {
			err = tx.Table("events").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", t.EventID).Take(&row).Error
		} else {
			row, err = lockClaim(tx, *claim)
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.Event.Dir != event.DirIn || row.Event.Status != event.StatusQueued || row.Event.Channel != t.Channel || row.Event.Session != t.Session || (claim == nil && (row.Owner != nil || row.Version != 0)) || (claim != nil && row.Stage != event.StageSchedule && row.Stage != event.StageGenerate) {
			return store.ErrConflict
		}
		result := tx.Table("tasks").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "event_id"}}, DoNothing: true}).Create(&t)
		inserted = result.RowsAffected == 1
		if result.Error != nil {
			return result.Error
		}
		changes := map[string]any{"stage": event.StageGenerate}
		if claim != nil {
			return updateClaim(tx, *claim, changes)
		}
		return tx.Table("events").Where("id=? AND lease_owner IS NULL AND lease_version=0", t.EventID).Updates(changes).Error
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) || errors.Is(err, event.ErrLeaseLost) {
			return false, err
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return false, store.ErrConflict
		}
		return false, &safeError{"创建任务失败", err}
	}
	return inserted, nil
}

// GetByEvent 按唯一event_id取初始任务投影。
func (r *TaskRepo) GetByEvent(ctx context.Context, id string) (store.Task, error) {
	if ctx == nil || strings.TrimSpace(id) == "" {
		return store.Task{}, store.ErrInvalid
	}
	var t store.Task
	err := r.db.WithContext(ctx).Table("tasks").Where("event_id=?", id).Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return t, store.ErrNotFound
	}
	if err != nil {
		return store.Task{}, &safeError{"读取任务失败", err}
	}
	return t, nil
}
