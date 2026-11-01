package database

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type queueRow struct {
	Event   eventRow `gorm:"embedded"`
	Stage   event.Stage
	StateAt time.Time  `gorm:"column:status_changed_at"`
	Owner   *string    `gorm:"column:lease_owner"`
	Version int64      `gorm:"column:lease_version"`
	Until   *time.Time `gorm:"column:lease_until"`
}

func (r queueRow) claim() event.Claim {
	c := event.Claim{Event: r.Event.event(), Stage: r.Stage, StateAt: r.StateAt, Lease: event.Lease{Version: r.Version}}
	if r.Owner != nil {
		c.Lease.Owner = *r.Owner
	}
	if r.Until != nil {
		c.LeaseUntil = *r.Until
	}
	return c
}
func validTTL(ttl time.Duration) bool { return ttl >= time.Second && ttl <= 24*time.Hour }
func validClaim(c event.Claim) bool {
	return strings.TrimSpace(c.Event.ID) != "" && strings.TrimSpace(c.Lease.Owner) != "" && c.Lease.Version > 0
}
func queueFailure(message string, err error) error {
	if err == nil || errors.Is(err, event.ErrLeaseLost) || errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrConflict) {
		return err
	}
	return &safeError{message, err}
}
func leaseQuery(tx *gorm.DB, c event.Claim) *gorm.DB {
	return tx.Table("events").Where("id=? AND lease_owner=? AND lease_version=? AND lease_until>clock_timestamp()", c.Event.ID, c.Lease.Owner, c.Lease.Version)
}
func lockClaim(tx *gorm.DB, c event.Claim) (queueRow, error) {
	var row queueRow
	err := leaseQuery(tx, c).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, event.ErrLeaseLost
	}
	return row, err
}
func updateClaim(tx *gorm.DB, c event.Claim, updates map[string]any) error {
	result := leaseQuery(tx, c).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return event.ErrLeaseLost
	}
	return nil
}
func releaseLease(updates map[string]any) {
	updates["lease_owner"] = nil
	updates["lease_until"] = nil
	updates["lease_version"] = gorm.Expr("lease_version+1")
}

// Claim 一条CTE完成有界SKIP LOCKED抢占与换租，不依赖进程内去重。
func (r *EventRepo) Claim(ctx context.Context, owner string, ttl time.Duration, limit int) ([]event.Claim, error) {
	if ctx == nil || strings.TrimSpace(owner) == "" || len(owner) > 128 || !validTTL(ttl) || limit < 1 || limit > 100 {
		return nil, store.ErrInvalid
	}
	var rows []queueRow
	err := r.db.WithContext(ctx).Raw(`WITH picked AS (
 SELECT id FROM events WHERE dir='in'
 AND (status='queued' OR (status IN ('pending','retry') AND (retry_at IS NULL OR retry_at<=clock_timestamp())))
 AND (lease_owner IS NULL OR lease_until<=clock_timestamp())
 ORDER BY at,id LIMIT ? FOR UPDATE SKIP LOCKED
 ) UPDATE events e SET status='queued',
 status_changed_at=CASE WHEN e.status='queued' THEN e.status_changed_at ELSE clock_timestamp() END,
 retry_at=NULL,lease_owner=?,lease_version=e.lease_version+1,
 lease_until=clock_timestamp()+(? * interval '1 microsecond')
 FROM picked p WHERE e.id=p.id RETURNING e.*`, limit, owner, ttl.Microseconds()).Scan(&rows).Error
	if err != nil {
		return nil, &safeError{"领取事件失败", err}
	}
	claims := make([]event.Claim, 0, len(rows))
	for _, row := range rows {
		claims = append(claims, row.claim())
	}
	sort.Slice(claims, func(i, j int) bool {
		a, b := claims[i].Event, claims[j].Event
		if a.At.Equal(b.At) {
			return a.ID < b.ID
		}
		return a.At.Before(b.At)
	})
	return claims, nil
}

// MarkClaim 只开放契约指定阶段边，数据库当前行才是阶段真源。
func (r *EventRepo) MarkClaim(ctx context.Context, c event.Claim, from []event.Status, to event.Status, ttl time.Duration) error {
	if ctx == nil || !validClaim(c) || !validTTL(ttl) {
		return store.ErrInvalid
	}
	if err := store.ValidateTransition(from, to); err != nil {
		return err
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		status := row.Event.Status
		if !slices.Contains(from, status) {
			return store.ErrConflict
		}
		allowed := false
		switch to {
		case event.StatusSkipped:
			allowed = status == event.StatusQueued && row.Stage == event.StageSchedule
		case event.StatusReplying:
			allowed = status == event.StatusQueued && row.Stage == event.StageGenerate
		case event.StatusSent:
			allowed = status == event.StatusReplying && row.Stage == event.StageGenerate
		case event.StatusDone:
			allowed = row.Stage == event.StageVerify && (status == event.StatusSent || status == event.StatusQueued)
		case event.StatusDead:
			allowed = status == event.StatusQueued || status == event.StatusReplying
		}
		if !allowed {
			return store.ErrInvalid
		}
		changes := map[string]any{"status": to, "status_changed_at": gorm.Expr("clock_timestamp()")}
		if to == event.StatusSent {
			changes["stage"] = event.StageVerify
		}
		if to == event.StatusDone || to == event.StatusSkipped || to == event.StatusDead {
			releaseLease(changes)
		} else {
			changes["lease_until"] = gorm.Expr("clock_timestamp()+(? * interval '1 microsecond')", ttl.Microseconds())
		}
		return updateClaim(tx, c, changes)
	})
	return queueFailure("更新持有事件失败", err)
}

// DeferClaim 以PG时间校验未来期限，延期不改变执行阶段。
func (r *EventRepo) DeferClaim(ctx context.Context, c event.Claim, until time.Time) error {
	if ctx == nil || !validClaim(c) || until.IsZero() {
		return store.ErrInvalid
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockClaim(tx, c)
		if err != nil {
			return err
		}
		if row.Event.Status != event.StatusQueued && row.Event.Status != event.StatusReplying && row.Event.Status != event.StatusSent {
			return store.ErrConflict
		}
		var future bool
		if err := tx.Raw("SELECT ? > clock_timestamp()", until).Scan(&future).Error; err != nil {
			return err
		}
		if !future {
			return store.ErrInvalid
		}
		changes := map[string]any{"status": event.StatusRetry, "status_changed_at": gorm.Expr("clock_timestamp()"), "retry_at": until}
		releaseLease(changes)
		return updateClaim(tx, c, changes)
	})
	return queueFailure("延期事件失败", err)
}

// RenewClaim 过期后不能复活旧租约；状态时间与代次保持不变。
func (r *EventRepo) RenewClaim(ctx context.Context, c event.Claim, ttl time.Duration) (event.Claim, error) {
	if ctx == nil || !validClaim(c) || !validTTL(ttl) {
		return event.Claim{}, store.ErrInvalid
	}
	var rows []queueRow
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// UPDATE的谓词可能在等待纯行锁前求值；先拿锁，再用独立语句复检PG时间。
		if _, err := lockClaim(tx, c); err != nil {
			return err
		}
		err := tx.Raw(`UPDATE events SET lease_until=clock_timestamp()+(? * interval '1 microsecond')
 WHERE id=? AND lease_owner=? AND lease_version=? AND lease_until>clock_timestamp() RETURNING *`, ttl.Microseconds(), c.Event.ID, c.Lease.Owner, c.Lease.Version).Scan(&rows).Error
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return event.ErrLeaseLost
		}
		return nil
	})
	if err != nil {
		return event.Claim{}, queueFailure("续租事件失败", err)
	}
	return rows[0].claim(), nil
}

// RecoverExpired 只处理已过期租约；sent与未知投递阶段只进核验补偿。
func (r *EventRepo) RecoverExpired(ctx context.Context, limit int) (int, error) {
	if ctx == nil || limit < 1 || limit > 100 {
		return 0, store.ErrInvalid
	}
	result := r.db.WithContext(ctx).Exec(`WITH picked AS (
 SELECT id FROM events WHERE dir='in' AND status IN ('queued','replying','sent')
 AND lease_owner IS NOT NULL AND lease_until<=clock_timestamp()
 ORDER BY lease_until,id LIMIT ? FOR UPDATE SKIP LOCKED
 ) UPDATE events e SET
 status=CASE WHEN e.status='sent' OR (e.status='replying' AND e.stage<>'generate') THEN 'retry' ELSE 'queued' END,
 stage=CASE WHEN e.status='sent' OR (e.status='replying' AND e.stage<>'generate') THEN 'verify' ELSE e.stage END,
 status_changed_at=CASE WHEN e.status='queued' THEN e.status_changed_at ELSE clock_timestamp() END,
 retry_at=CASE WHEN e.status='sent' OR (e.status='replying' AND e.stage<>'generate') THEN clock_timestamp() ELSE NULL END,
 lease_owner=NULL,lease_until=NULL,lease_version=e.lease_version+1
 FROM picked p WHERE e.id=p.id`, limit)
	if result.Error != nil {
		return 0, &safeError{"恢复过期事件失败", result.Error}
	}
	return int(result.RowsAffected), nil
}
