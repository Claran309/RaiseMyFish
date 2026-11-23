//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

func TestErrorRepoDeadStatsAndDLQReplay(t *testing.T) {
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
	errorsRepo, err := NewErrorRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	dlq, err := NewDLQRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Minute)
	deadID := "comment:video-err:1"
	insertEvent(t, events, event.Event{
		ID: deadID, Channel: event.ChComment, Dir: event.DirIn, Session: "video-err",
		FromID: "7", Text: "失败评论", At: base, Status: event.StatusPending,
		CollectedAt: base, CollectMode: "poll",
	})
	if err := db.Exec("UPDATE events SET status='dead' WHERE id=?", deadID).Error; err != nil {
		t.Fatal(err)
	}
	if err := errorsRepo.Record(ctx, store.EventError{EventID: deadID, Stage: "generate", ErrorKind: "budget", Message: "提示预算超限"}); err != nil {
		t.Fatal(err)
	}
	stats, err := errorsRepo.DeadStats(ctx, base.Add(-time.Minute), time.Now().UTC().Add(time.Minute), 5)
	if err != nil || len(stats) != 1 || stats[0].Channel != "comment" || stats[0].ErrorKind != "budget" {
		t.Fatalf("死信分布不符：%+v %v", stats, err)
	}

	// 无投递证据：允许重放，dead→pending/schedule。
	if err := db.Exec(`INSERT INTO dlq(event_id,stage,error,payload) VALUES(?,?,?,?::jsonb)`, deadID, "generate", "budget", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	var dlqID int64
	if err := db.Raw("SELECT id FROM dlq WHERE event_id=? ORDER BY id DESC LIMIT 1", deadID).Scan(&dlqID).Error; err != nil {
		t.Fatal(err)
	}
	res, err := dlq.Replay(ctx, dlqID)
	if err != nil || !res.Replayed {
		t.Fatalf("重放失败：%+v %v", res, err)
	}
	var row struct {
		Status string
		Stage  string
	}
	if err := db.Raw("SELECT status,stage FROM events WHERE id=?", deadID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" || row.Stage != "schedule" {
		t.Fatalf("重放后状态=%s/%s", row.Status, row.Stage)
	}

	// 有 send_log 证据：拒绝重放，避免重复回复。
	touchedID := "comment:video-err:2"
	insertEvent(t, events, event.Event{
		ID: touchedID, Channel: event.ChComment, Dir: event.DirIn, Session: "video-err",
		FromID: "8", Text: "已尝试回复", At: base, Status: event.StatusPending,
		CollectedAt: base, CollectMode: "poll",
	})
	if err := db.Exec("UPDATE events SET status='dead' WHERE id=?", touchedID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO send_log(event_id,channel,ok,verified,msg_id,at) VALUES(?,?,?,?,?,?)`,
		touchedID, "comment", true, true, "rpid-1", base.Add(time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO dlq(event_id,stage,error,payload) VALUES(?,?,?,?::jsonb)`, touchedID, "verify", "unknown", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT id FROM dlq WHERE event_id=? ORDER BY id DESC LIMIT 1", touchedID).Scan(&dlqID).Error; err != nil {
		t.Fatal(err)
	}
	res, err = dlq.Replay(ctx, dlqID)
	if err != nil || res.Replayed || res.Reason == "" {
		t.Fatalf("有投递证据仍重放：%+v %v", res, err)
	}
}
