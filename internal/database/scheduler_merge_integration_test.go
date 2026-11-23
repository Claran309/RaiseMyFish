//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

// TestNewerSameUserRealPostgres L3.6：窗口内同用户更新事件才合并。
func TestNewerSameUserRealPostgres(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewSchedulerRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Minute)
	insertEvent(t, events, event.Event{
		ID: "dm:1:2:first", Channel: event.ChDM, Dir: event.DirIn, Session: "1:2",
		FromID: "2", Text: "第一条", Private: true, At: base, Status: event.StatusPending,
	})
	insertEvent(t, events, event.Event{
		ID: "dm:1:2:second", Channel: event.ChDM, Dir: event.DirIn, Session: "1:2",
		FromID: "2", Text: "第二条", Private: true, At: base.Add(5 * time.Second), Status: event.StatusPending,
	})
	claim := event.Claim{
		Event: event.Event{
			ID: "dm:1:2:first", Channel: event.ChDM, Dir: event.DirIn, Session: "1:2",
			FromID: "2", Text: "第一条", Private: true, At: base, Status: event.StatusQueued,
		},
		Lease: event.Lease{Owner: "w", Version: 1},
	}
	newer, err := repo.NewerSameUser(ctx, claim, 30*time.Second)
	if err != nil || !newer {
		t.Fatalf("窗口内应命中：%v %v", newer, err)
	}
	if newer, err := repo.NewerSameUser(ctx, claim, 2*time.Second); err != nil || newer {
		t.Fatalf("窗口外不应命中：%v %v", newer, err)
	}
	// 不同用户不合并。
	insertEvent(t, events, event.Event{
		ID: "dm:1:3:other", Channel: event.ChDM, Dir: event.DirIn, Session: "1:2",
		FromID: "3", Text: "别人", Private: true, At: base.Add(time.Second), Status: event.StatusPending,
	})
	if newer, err := repo.NewerSameUser(ctx, claim, 30*time.Second); err != nil || !newer {
		t.Fatalf("同用户第二条仍应命中：%v %v", newer, err)
	}
}
