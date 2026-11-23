//go:build integration

package database

import (
	"context"
	"testing"
	"time"
)

// TestMemoryAuditRealPostgres M8：重复、改口、未引用、缺 from_id 四类清单。
func TestMemoryAuditRealPostgres(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insert := func(id, kind, title, factKey, fromID string, created time.Time) {
		t.Helper()
		if err := db.Exec(`INSERT INTO memory_records
(id,source,kind,title,summary,importance,confidence,from_id,fact_key,valid_from,created_at)
VALUES(?,'owner_direct',?,?,?,0.8,0.9,NULLIF(?,''),NULLIF(?,''),?,?)`,
			id, kind, title, "摘要"+title, fromID, factKey, created, created).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 同一 fact_key 两条 active → 重复。
	insert("m-dup-1", "fact", "喜欢鲫鱼", "fact:owner:like", "42", now.Add(-3*time.Hour))
	insert("m-dup-2", "fact", "喜欢鲫鱼", "fact:owner:like", "42", now.Add(-2*time.Hour))
	// 没有 from_id 的偏好 → 缺来源。
	insert("m-nofrom", "preference", "爱吃玉米", "", "", now.Add(-time.Hour))
	// 被引用过一次的记忆。
	insert("m-used", "fact", "住在城东", "fact:owner:city", "42", now.Add(-90*time.Minute))
	if err := db.Exec(`INSERT INTO memory_usage(reply_event_id,record_id,rank,injected)
VALUES('reply-1','m-used',1,TRUE)`).Error; err != nil {
		t.Fatal(err)
	}
	// 一条改口审计。
	if err := db.Exec(`INSERT INTO memory_conflicts(fact_key,old_record_id,new_record_id,old_summary,new_summary,created_at)
VALUES('fact:owner:like','m-dup-1','m-dup-2','旧','新',?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	repo, err := NewMemoryAuditRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	report, err := repo.AuditMemories(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 4 {
		t.Fatalf("active=%d，期望 4", report.Active)
	}
	if report.Since.IsZero() {
		t.Fatal("usage_since 未读到")
	}
	if len(report.Duplicates) != 1 || report.Duplicates[0].Count != 2 {
		t.Fatalf("重复清单错误：%+v", report.Duplicates)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0].ID != "fact:owner:like" {
		t.Fatalf("改口清单错误：%+v", report.Conflicts)
	}
	// 未引用：m-dup-1/m-dup-2/m-nofrom（m-used 有引用记录）。
	if len(report.NeverReferenced) != 3 {
		t.Fatalf("未引用清单错误：%+v", report.NeverReferenced)
	}
	// 缺 from_id：只有 preference 且 from_id 为空。
	if len(report.MissingFrom) != 1 || report.MissingFrom[0].ID != "m-nofrom" {
		t.Fatalf("缺来源清单错误：%+v", report.MissingFrom)
	}
}
