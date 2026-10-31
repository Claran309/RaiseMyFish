package database

import (
	"context"
	"errors"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// SchedulerRepo 将每日准入和会话顺序检查放在PG，不增加新的事实表。
type SchedulerRepo struct{ db *gorm.DB }

// NewSchedulerRepo 复用已迁移的infra连接。
func NewSchedulerRepo(db *gorm.DB) (*SchedulerRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &SchedulerRepo{db: db}, nil
}

// Task 返回既有唯一任务，不为恢复重新造任务。
func (r *SchedulerRepo) Task(ctx context.Context, id string) (store.Task, error) {
	repo, _ := NewTaskRepo(r.db)
	return repo.GetByEvent(ctx, id)
}

// Snapshot包含结清代次，只用于内部执行回执，不用于上下文召回。
func (r *SchedulerRepo) Snapshot(ctx context.Context, id string) (event.Claim, error) {
	if ctx == nil || id == "" {
		return event.Claim{}, store.ErrInvalid
	}
	var row queueRow
	err := r.db.WithContext(ctx).Table("events").Where("id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return event.Claim{}, store.ErrNotFound
	}
	if err != nil {
		return event.Claim{}, &safeError{"读取调度结清代次失败", err}
	}
	return row.claim(), nil
}

func earlierEvent(tx *gorm.DB, row queueRow) (*time.Time, error) {
	var rows []struct{ Until time.Time }
	err := tx.Raw(`SELECT COALESCE(retry_at,clock_timestamp()) AS until FROM events
 WHERE channel=? AND session=? AND dir='in' AND status NOT IN ('done','skipped','dead')
 AND (at,id)<(?,?) ORDER BY at,id LIMIT 1`, row.Event.Channel, row.Event.Session, row.Event.At, row.Event.ID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0].Until, nil
}

// Earlier 依据实际行而非Claim快照，检查已入库的更早未结束事件。
func (r *SchedulerRepo) Earlier(ctx context.Context, c event.Claim) (*time.Time, error) {
	if ctx == nil || !validClaim(c) {
		return nil, store.ErrInvalid
	}
	var until *time.Time
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if row.Event.Status != event.StatusQueued {
			return store.ErrConflict
		}
		until, err = earlierEvent(tx, row)
		return err
	})
	return until, queueFailure("查询调度会话顺序失败", err)
}

// Admit 以通道/日事务锁串行准入，配额检查、唯一Task和阶段切换同事务提交。
func (r *SchedulerRepo) Admit(ctx context.Context, c event.Claim, t store.Task, q store.ScheduleQuota) (store.ScheduleAdmission, error) {
	if ctx == nil || !validClaim(c) || q.Limit <= 0 || q.From.IsZero() || !q.From.Before(q.Until) || t.CreatedAt.Before(q.From) || !t.CreatedAt.Before(q.Until) || c.Event.ID != t.EventID {
		return store.ScheduleAdmission{}, store.ErrInvalid
	}
	if err := t.Validate(); err != nil {
		return store.ScheduleAdmission{}, err
	}
	out := store.ScheduleAdmission{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// hash碰撞只会增加串行范围；输入始终为SQL参数。
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(current_database()||':'||current_schema()||':schedule-quota'),hashtext(?))", string(t.Channel)+":"+q.From.UTC().Format(time.RFC3339Nano)).Error; err != nil {
			return err
		}
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if row.Event.Status != event.StatusQueued || row.Event.Channel != t.Channel || row.Event.Session != t.Session || row.Event.Dir != event.DirIn || (row.Stage != event.StageSchedule && row.Stage != event.StageGenerate) {
			return store.ErrConflict
		}
		tasks, _ := NewTaskRepo(tx)
		if _, err := tasks.GetByEvent(ctx, t.EventID); err == nil {
			_, err = tasks.CreateClaimed(ctx, c, t)
			return err
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		until, err := earlierEvent(tx, row)
		if err != nil {
			return err
		}
		if until != nil {
			out.WaitUntil = until
			return nil
		}
		var count int64
		if err := tx.Table("tasks").Where("channel=? AND created_at>=? AND created_at<?", t.Channel, q.From, q.Until).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(q.Limit) {
			until := q.Until
			out.WaitUntil = &until
			return nil
		}
		out.Inserted, err = tasks.CreateClaimed(ctx, c, t)
		return err
	})
	return out, queueFailure("调度任务准入失败", err)
}
