//go:build integration

package database

// 本文件用真实 PG 测面板记忆排序与异常清理（#71/#72）。

import (
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

func TestWebMemoryOrderRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	mem, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	seed := func(id string, importance float64, createdAt time.Time) {
		t.Helper()
		rec := factRecord(id, "标题"+id, "摘要"+id, event.ChLive, "room", false, createdAt.Add(-time.Hour))
		rec.Importance = importance
		mustInsertRecord(t, ctx, mem, rec)
		if err := db.WithContext(ctx).Exec("UPDATE memory_records SET created_at = ? WHERE id = ?", createdAt, id).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed("web_mem_old", 0.9, base.Add(-2*time.Hour))
	seed("web_mem_new", 0.1, base.Add(-time.Hour))
	seed("web_mem_mid", 0.5, base)

	byTime, err := repo.Memory(ctx, "", "", "time", 10)
	if err != nil {
		t.Fatalf("按时间排序失败：%v", err)
	}
	if len(byTime) != 3 || byTime[0].ID != "web_mem_mid" || byTime[1].ID != "web_mem_new" || byTime[2].ID != "web_mem_old" {
		t.Fatalf("时间排序错误：%+v", byTime)
	}
	byImportance, err := repo.Memory(ctx, "", "", "importance", 10)
	if err != nil {
		t.Fatalf("按重要度排序失败：%v", err)
	}
	if len(byImportance) != 3 || byImportance[0].ID != "web_mem_old" || byImportance[1].ID != "web_mem_mid" || byImportance[2].ID != "web_mem_new" {
		t.Fatalf("重要度排序错误：%+v", byImportance)
	}
	if _, err := repo.Memory(ctx, "", "", "importance DESC", 10); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("未知排序应 invalid：%v", err)
	}
}

func TestWebErrorClearRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	seed := func(id string, status event.Status, at time.Time) {
		t.Helper()
		row := event.Event{
			ID: id, Channel: event.ChLive, Dir: event.DirOut, Session: "room",
			FromID: "u1", FromName: "甲", Text: "失败事件", At: at, Status: status,
		}
		if ok, err := events.Insert(ctx, row); err != nil || !ok {
			t.Fatalf("写事件 %s 失败：%v ok=%v", id, err, ok)
		}
	}
	seed("live:clear:old-dead", event.StatusDead, base.Add(-10*24*time.Hour))
	seed("live:clear:recent-dead", event.StatusDead, base.Add(-2*time.Hour))
	seed("live:clear:recent-retry", event.StatusRetry, base.Add(-time.Hour))
	seed("live:clear:old-done", event.StatusDone, base.Add(-10*24*time.Hour))

	rows, err := repo.Errors(ctx, 10)
	if err != nil {
		t.Fatalf("读异常失败：%v", err)
	}
	if len(rows) != 3 || rows[0].ID != "live:clear:recent-retry" || rows[1].ID != "live:clear:recent-dead" {
		t.Fatalf("异常列表错误：%+v", rows)
	}

	if err := repo.ClearError(ctx, "live:clear:recent-dead"); err != nil {
		t.Fatalf("单条清理失败：%v", err)
	}
	if err := repo.ClearError(ctx, "live:clear:recent-dead"); !errors.Is(err, store.ErrWebNotFound) {
		t.Fatalf("重复清理应 not found：%v", err)
	}
	if err := repo.ClearError(ctx, "live:clear:old-done"); !errors.Is(err, store.ErrWebNotFound) {
		t.Fatalf("非异常事件应 not found：%v", err)
	}
	if err := repo.ClearError(ctx, "  "); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("空 id 应 invalid：%v", err)
	}

	cleared, err := repo.ClearErrorsBefore(ctx, base.Add(-5*24*time.Hour))
	if err != nil || cleared != 1 {
		t.Fatalf("批量清理应只命中 1 条：cleared=%d err=%v", cleared, err)
	}
	if clearedAgain, err := repo.ClearErrorsBefore(ctx, base.Add(-5*24*time.Hour)); err != nil || clearedAgain != 0 {
		t.Fatalf("重复批量清理应为 0：%d %v", clearedAgain, err)
	}
	if _, err := repo.ClearErrorsBefore(ctx, time.Time{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("零时间应 invalid：%v", err)
	}

	left, err := repo.Errors(ctx, 10)
	if err != nil || len(left) != 1 || left[0].ID != "live:clear:recent-retry" {
		t.Fatalf("清理后应只剩近期 retry：%+v %v", left, err)
	}
	// 清理只隐藏面板行，不推进状态机。
	var status string
	if err := db.WithContext(ctx).Raw("SELECT status FROM events WHERE id = ?", "live:clear:old-dead").Scan(&status).Error; err != nil || status != "dead" {
		t.Fatalf("清理改了事件状态：%q %v", status, err)
	}
}
