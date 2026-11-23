package scheduler

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

type fakeErrorRepo struct{ items []store.EventError }

func (f *fakeErrorRepo) Record(_ context.Context, item store.EventError) error {
	f.items = append(f.items, item)
	return nil
}

func (f *fakeErrorRepo) DeadStats(context.Context, time.Time, time.Time, int) ([]store.DeadStat, error) {
	return nil, nil
}

func TestRecordSkippedWritesReason(t *testing.T) {
	repo := &fakeErrorRepo{}
	s := &Scheduler{options: Options{ErrorRepo: repo}, log: zap.NewNop()}
	s.recordSkipped(event.Claim{Event: event.Event{ID: "comment:1:1"}}, "stale")
	if len(repo.items) != 1 || repo.items[0].ErrorKind != "stale" || repo.items[0].Stage != "schedule" {
		t.Fatalf("跳过原因未落库：%+v", repo.items)
	}
}
