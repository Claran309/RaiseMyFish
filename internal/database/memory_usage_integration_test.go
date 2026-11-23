//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

// TestMemoryUsageRecordsAndMarks 覆盖 M5：回复注入的记忆可查、可标记 used。
func TestMemoryUsageRecordsAndMarks(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := NewMemoryUsageRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	score := 0.87
	if err := repo.RecordMemoryUsage(ctx, []store.MemoryUsage{
		{ReplyEventID: "live:room:out1", RecordID: "fact:public:2002:1", Rank: 0, Score: &score, Injected: true},
		{ReplyEventID: "live:room:out1", RecordID: "fact:public:2002:2", Rank: 1, Injected: true},
	}); err != nil {
		t.Fatal(err)
	}
	// 重复写入不产生重复行。
	if err := repo.RecordMemoryUsage(ctx, []store.MemoryUsage{
		{ReplyEventID: "live:room:out1", RecordID: "fact:public:2002:1", Rank: 0, Injected: true},
	}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("memory_usage").Where("reply_event_id=?", "live:room:out1").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("记忆使用行数=%d %v", count, err)
	}
	if err := repo.MarkMemoryUsed(ctx, "live:room:out1", []string{"fact:public:2002:1"}, true); err != nil {
		t.Fatal(err)
	}
	var used *bool
	if err := db.Raw("SELECT used FROM memory_usage WHERE record_id=?", "fact:public:2002:1").Scan(&used).Error; err != nil {
		t.Fatal(err)
	}
	if used == nil || !*used {
		t.Fatalf("used 标记未写入：%v", used)
	}
}
