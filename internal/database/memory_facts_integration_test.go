//go:build integration

package database

// 本文件测记忆补全：from_id 写入、按 uid 查事实、事实时效（相同幂等/不同覆盖）。

import (
	"testing"
	"time"

	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

func newFactRecord(id, fromID, name, summary string) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{
			ID: id, Summary: summary, Importance: 0.6, Confidence: 0.85,
			EventIDs: []string{"live:30308220:x"}, ValidFrom: time.Now(),
		},
		Source: "persona", Kind: "fact", Title: "事实：我是学生",
		FromID: fromID, FromName: name,
	}
}

// TestMemoryRecordFromIDAndFactsByViewer 事实带上 from_id，且能按 uid 查回。
func TestMemoryRecordFromIDAndFactsByViewer(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.InsertRecord(ctx, newFactRecord("fact:2002:a", "2002", "熟客乙", "我是学生")); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if _, err := repo.InsertRecord(ctx, newFactRecord("fact:3003:b", "3003", "老熟人丙", "我爱吃辣")); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	rows, err := repo.FactsByViewer(ctx, "2002", 10)
	if err != nil {
		t.Fatalf("按观众查事实失败：%v", err)
	}
	if len(rows) != 1 || rows[0].FromID != "2002" || rows[0].FromName != "熟客乙" {
		t.Fatalf("事实主体不符：%+v", rows)
	}
	if rows[0].Summary != "我是学生" {
		t.Errorf("摘要不符：%q", rows[0].Summary)
	}

	// 陌生人没有事实
	if rows, err := repo.FactsByViewer(ctx, "9999", 10); err != nil || len(rows) != 0 {
		t.Errorf("陌生人应为空：%+v / %v", rows, err)
	}
}

// TestFactTemporalSemantics 同人同事实：相同摘要幂等、不同摘要覆盖并刷新 valid_from。
func TestFactTemporalSemantics(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	id := "fact:2002:xyz"
	first := newFactRecord(id, "2002", "熟客乙", "我是大二学生")
	first.ValidFrom = time.Now().Add(-time.Hour)
	inserted, err := repo.InsertRecord(ctx, first)
	if err != nil || !inserted {
		t.Fatalf("首次写入应成功：%v / %v", inserted, err)
	}

	// 同摘要：幂等，不新增不更新
	inserted, err = repo.InsertRecord(ctx, newFactRecord(id, "2002", "熟客乙", "我是大二学生"))
	if err != nil {
		t.Fatalf("重复写入报错：%v", err)
	}
	if inserted {
		t.Error("相同摘要应幂等（RowsAffected=0）")
	}

	// 不同摘要：覆盖更新，valid_from 刷新
	changed := newFactRecord(id, "2002", "熟客乙", "我是大三学生")
	inserted, err = repo.InsertRecord(ctx, changed)
	if err != nil {
		t.Fatalf("覆盖写入报错：%v", err)
	}
	if !inserted {
		t.Error("不同摘要应更新（RowsAffected=1）")
	}
	rows, err := repo.FactsByViewer(ctx, "2002", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("应只有一条：%+v / %v", rows, err)
	}
	if rows[0].Summary != "我是大三学生" {
		t.Errorf("摘要未更新：%q", rows[0].Summary)
	}
	if !rows[0].ValidFrom.After(first.ValidFrom) {
		t.Errorf("valid_from 未刷新：%v -> %v", first.ValidFrom, rows[0].ValidFrom)
	}
}
