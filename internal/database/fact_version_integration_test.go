//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// TestFactVersionChainSupersedesOldRow 覆盖 M1：
// 同一 fact_key 改口 → 旧行 valid_to/superseded_by/status 正确，新行 active；
// 冲突审计落库；默认召回只看到新事实。
func TestFactVersionChainSupersedesOldRow(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	factKey := memory.FactKey("public", "2002", "喜欢：吃白饭")
	old := store.MemoryRecord{
		Record: memory.Record{ID: "fact:public:2002:v1", Channel: event.ChLive, Session: "room",
			Summary: "喜欢白饭", Importance: 0.8, Confidence: 0.9, EventIDs: []string{"live:room:1"}, ValidFrom: base},
		Source: "content", Kind: "fact", Title: "喜欢：吃白饭", FromID: "2002", FactKey: factKey,
	}
	if ok, err := repo.InsertRecord(ctx, old); err != nil || !ok {
		t.Fatalf("写入旧事实失败：%v %v", ok, err)
	}
	newer := old
	newer.Record.ID = "fact:public:2002:v2"
	newer.Record.Summary = "不喜欢白饭了"
	newer.Record.EventIDs = []string{"live:room:2"}
	newer.Record.ValidFrom = base.Add(10 * time.Minute)
	if ok, err := repo.InsertRecord(ctx, newer); err != nil || !ok {
		t.Fatalf("写入新事实失败：%v %v", ok, err)
	}

	var oldRow struct {
		ValidTo      *time.Time
		SupersededBy *string
		Status       string
	}
	if err := db.Raw("SELECT valid_to,superseded_by,status FROM memory_records WHERE id=?", old.ID).Scan(&oldRow).Error; err != nil {
		t.Fatal(err)
	}
	if oldRow.ValidTo == nil || oldRow.SupersededBy == nil || *oldRow.SupersededBy != newer.ID || oldRow.Status != "superseded" {
		t.Fatalf("旧行未正确失效：%+v", oldRow)
	}
	var newStatus string
	if err := db.Raw("SELECT status FROM memory_records WHERE id=?", newer.ID).Scan(&newStatus).Error; err != nil {
		t.Fatal(err)
	}
	if newStatus != "active" {
		t.Fatalf("新行状态=%s，期望 active", newStatus)
	}
	var conflicts int64
	if err := db.Table("memory_conflicts").Where("fact_key=?", factKey).Count(&conflicts).Error; err != nil || conflicts != 1 {
		t.Fatalf("冲突审计条数=%d %v", conflicts, err)
	}

	// 默认召回（valid_to IS NULL）只能看到新事实。
	var active []string
	if err := db.Raw("SELECT id FROM memory_records WHERE fact_key=? AND valid_to IS NULL", factKey).Scan(&active).Error; err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0] != newer.ID {
		t.Fatalf("默认召回含旧版本：%v", active)
	}
}
