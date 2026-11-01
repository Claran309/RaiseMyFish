//go:build integration

package database

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"testing"
	"time"
)

func TestOnlyVerifiedOutIsConversationMemory(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, _ := NewEventRepo(db)
	repo, _ := NewMemoryRepo(db)
	for _, status := range []event.Status{event.StatusDone, event.StatusRetry, event.StatusDead, event.StatusSkipped} {
		e := testEvent("live:out:99:"+string(status), event.ChLive, "99", false, time.Now())
		e.Dir = event.DirOut
		e.Status = status
		if _, err := events.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	scope := memory.Scope{Channel: event.ChLive, Session: "99"}
	rows, err := repo.Recent(ctx, scope, 10)
	if err != nil || len(rows) != 1 || rows[0].EventID != "live:out:99:done" {
		t.Fatal("未投递审计冒充发言历史", len(rows), err)
	}
	cross, err := repo.CrossChannel(ctx, memory.Scope{Channel: event.ChComment, Session: "video"}, 10)
	if err != nil || len(cross) != 1 {
		t.Fatal("跨通道读到了失败out", len(cross), err)
	}
}
