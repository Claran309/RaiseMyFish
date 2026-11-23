package database

import (
	"context"
	"errors"
	"strings"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// TimingRepo 是 event_timings 的最小读写实现。
type TimingRepo struct{ db *gorm.DB }

func NewTimingRepo(db *gorm.DB) (*TimingRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &TimingRepo{db: db}, nil
}

var _ store.TimingRepo = (*TimingRepo)(nil)

// MarkGenerated 只写生成完成时间；重复调用保留最早一次。
func (r *TimingRepo) MarkGenerated(ctx context.Context, eventID string, at time.Time) error {
	if ctx == nil || strings.TrimSpace(eventID) == "" || at.IsZero() {
		return store.ErrInvalid
	}
	err := r.db.WithContext(ctx).Exec(`INSERT INTO event_timings(event_id, generated_at) VALUES(?,?)
ON CONFLICT (event_id) DO UPDATE
SET generated_at=COALESCE(event_timings.generated_at, EXCLUDED.generated_at), updated_at=now()`, eventID, at.UTC()).Error
	if err != nil {
		return &safeError{"写入生成计时失败", err}
	}
	return nil
}

// Get 返回七个阶段时间；缺失阶段保持零值，不伪造 0。
func (r *TimingRepo) Get(ctx context.Context, eventID string) (event.Timing, error) {
	if ctx == nil || strings.TrimSpace(eventID) == "" {
		return event.Timing{}, store.ErrInvalid
	}
	var row struct {
		EventID       string
		CollectedAt   *time.Time
		EnqueuedAt    *time.Time
		ClaimedAt     *time.Time
		GeneratedAt   *time.Time
		SendStartedAt *time.Time
		SentAt        *time.Time
		VerifiedAt    *time.Time
	}
	err := r.db.WithContext(ctx).Table("event_timings").Where("event_id=?", eventID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return event.Timing{}, store.ErrNotFound
	}
	if err != nil {
		return event.Timing{}, &safeError{"读取阶段计时失败", err}
	}
	pick := func(v *time.Time) time.Time {
		if v == nil {
			return time.Time{}
		}
		return *v
	}
	return event.Timing{
		EventID: row.EventID, CollectedAt: pick(row.CollectedAt), EnqueuedAt: pick(row.EnqueuedAt),
		ClaimedAt: pick(row.ClaimedAt), GeneratedAt: pick(row.GeneratedAt),
		SendStartedAt: pick(row.SendStartedAt), SentAt: pick(row.SentAt), VerifiedAt: pick(row.VerifiedAt),
	}, nil
}
