//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

// TestConversationStateAndTurns C1：会话状态可读写、过期不注入、轮次按会话隔离。
func TestConversationStateAndTurns(t *testing.T) {
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
	repo, err := NewConversationRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Minute)
	conv := "dm:1001:2002"
	for i, text := range []string{"在吗", "记得我吗", "那上次说的鱼呢"} {
		insertEvent(t, events, event.Event{
			ID: "dm:1001:2002:" + string(rune('1'+i)), Channel: event.ChDM, Dir: event.DirIn,
			Session: "1001:2002", FromID: "2002", Text: text, Private: true,
			At: base.Add(time.Duration(i) * time.Second), Status: event.StatusPending,
			CollectedAt: base, CollectMode: "poll", ConversationID: conv,
		})
	}
	other := event.Event{
		ID: "dm:1001:3003:1", Channel: event.ChDM, Dir: event.DirIn, Session: "1001:3003",
		FromID: "3003", Text: "别人的私信", Private: true, At: base, Status: event.StatusPending,
		CollectedAt: base, CollectMode: "poll", ConversationID: "dm:1001:3003",
	}
	insertEvent(t, events, other)

	state := store.ConversationState{
		ConversationID: conv, Channel: "dm", AccountID: "1001", UserID: "2002",
		Summary: "在聊鱼", TurnCount: 3, LastEventID: "dm:1001:2002:3",
		LastTurnAt: base.Add(2 * time.Second), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := repo.Touch(ctx, state); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.LoadState(ctx, conv, time.Now().UTC())
	if err != nil || !ok || got.Summary != "在聊鱼" || got.TurnCount != 3 {
		t.Fatalf("会话状态不符：%+v %v %v", got, ok, err)
	}
	// 轮数只增：回写更小值不覆盖。
	state.TurnCount = 1
	state.Summary = ""
	if err := repo.Touch(ctx, state); err != nil {
		t.Fatal(err)
	}
	got, _, _ = repo.LoadState(ctx, conv, time.Now().UTC())
	if got.TurnCount != 3 || got.Summary != "在聊鱼" {
		t.Fatalf("回退覆盖了会话：%+v", got)
	}
	turns, err := repo.RecentTurns(ctx, conv, 10)
	if err != nil || len(turns) != 3 {
		t.Fatalf("轮次不符：%d %v", len(turns), err)
	}
	for _, turn := range turns {
		if turn.Text == "别人的私信" {
			t.Fatal("串会话")
		}
	}
	// 过期会话不再注入。
	state.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	state.TurnCount = 3
	if err := repo.Touch(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := repo.LoadState(ctx, conv, time.Now().UTC()); ok {
		t.Fatal("过期会话仍被加载")
	}
}
