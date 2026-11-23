//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/memory"
)

// TestEntityRelationsOneHop 覆盖 M2：实体归一、关系幂等、1 跳邻域。
func TestEntityRelationsOneHop(t *testing.T) {
	db := isolatedDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo, err := NewEntityRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	cat := memory.PersonEntity("1001", "阿猫")
	ming := memory.PersonEntity("1002", "小明")
	if err := repo.Upsert(ctx, cat); err != nil {
		t.Fatal(err)
	}
	if err := repo.Upsert(ctx, ming); err != nil {
		t.Fatal(err)
	}
	rel := memory.Relation{FromID: ming.ID, Predicate: "roommate", ToID: cat.ID, Confidence: 0.8, SourceEventIDs: []string{"live:1:1"}}
	if err := repo.AddRelation(ctx, rel); err != nil {
		t.Fatal(err)
	}
	// 重复写入不产生第二条有效边。
	if err := repo.AddRelation(ctx, rel); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("memory_relations").Where("valid_to IS NULL").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("有效关系数=%d %v", count, err)
	}
	neighbors, err := repo.Neighbors(ctx, ming.ID)
	if err != nil || len(neighbors) != 1 || neighbors[0] != "阿猫(roommate)" {
		t.Fatalf("1跳邻域不符：%v %v", neighbors, err)
	}
	// 反向也能查到。
	back, err := repo.Neighbors(ctx, cat.ID)
	if err != nil || len(back) != 1 || back[0] != "小明(roommate)" {
		t.Fatalf("反向邻域不符：%v %v", back, err)
	}
}
