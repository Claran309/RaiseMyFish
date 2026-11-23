//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"gorm.io/gorm"
)

// TestEventTimingStagesAndLatencyDetail 覆盖 L0：七个阶段可查，all/fresh 与五段拆分可算。
func TestEventTimingStagesAndLatencyDetail(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	timing, err := NewTimingRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-10 * time.Second)
	id := "live:timing-room:1"
	insertEvent(t, repo, event.Event{
		ID: id, Channel: event.ChLive, Dir: event.DirIn, Session: "timing-room",
		FromID: "7", Text: "延迟口径测试", At: base, Status: event.StatusPending,
		CollectedAt: base.Add(500 * time.Millisecond), CollectMode: "feed",
	})
	if err := db.Exec("UPDATE events SET stage='generate' WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	claims, err := repo.Claim(ctx, "timing-owner", time.Minute, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("领取失败：%v %+v", err, claims)
	}
	if err := repo.MarkClaim(ctx, claims[0], []event.Status{event.StatusQueued}, event.StatusReplying, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := timing.MarkGenerated(ctx, id, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	armedAt := time.Now().UTC()
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO send_plans(event_id,actor_id,room_id,text,text_sha,current_attempt_id) VALUES(?,?,?,?,?,?)`,
			id, "30308220", "timing-room", "回复", "sha", "attempt-1").Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO send_attempts(id,event_id,actor_id,room_id,text_sha,number,state,armed_at,ok) VALUES(?,?,?,?,?,?,?,?,TRUE)`,
			"attempt-1", id, "30308220", "timing-room", "sha", 1, "armed", armedAt).Error
	}); err != nil {
		t.Fatal(err)
	}
	sentAt := armedAt.Add(200 * time.Millisecond)
	if err := db.Exec(`INSERT INTO send_log(event_id,channel,ok,verified,msg_id,at,attempt_id) VALUES(?,?,?,?,?,?,?)`,
		id, "live", true, true, "123", sentAt, "attempt-1").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE events SET status='done' WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}

	got, err := timing.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]time.Time{
		"collected": got.CollectedAt, "enqueued": got.EnqueuedAt, "claimed": got.ClaimedAt,
		"generated": got.GeneratedAt, "send_started": got.SendStartedAt, "sent": got.SentAt, "verified": got.VerifiedAt,
	} {
		if value.IsZero() {
			t.Fatalf("阶段 %s 未记录：%+v", name, got)
		}
	}

	stats, err := NewStats(db)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := stats.LatencyDetailStats(ctx, base.Add(-time.Minute), time.Now().UTC().Add(time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	live, ok := detail["live"]
	if !ok || live.All.Count != 1 || live.Fresh.Count != 1 {
		t.Fatalf("分段延迟样本不符：%+v", detail)
	}
	for name, window := range map[string]LatencyWindow{
		"collect": live.Segments.Collect, "queue": live.Segments.Queue,
		"generate": live.Segments.Generate, "send": live.Segments.Send,
		"e2e_user": live.Segments.E2EUser, "e2e_platform": live.Segments.E2EPlatform,
	} {
		if window.Count != 1 {
			t.Fatalf("%s 阶段样本数=%d，期望1", name, window.Count)
		}
	}
}
