//go:build integration

package database

// 本文件用真实 PG 测 N07：记忆向量化候选的选择矩阵。
//
// 口径：
//   - 默认（未授权）只返回公开且当前有效的行；
//   - 显式授权后加入私密有效行；
//   - 归档、valid_to 已失效、valid_from 未生效的行在任何授权下都不出现。

import (
	"slices"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// TestMemoryEmbeddingInputsSelection 验证 SQL 层的外发选择矩阵。
func TestMemoryEmbeddingInputsSelection(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	records := []store.MemoryRecord{
		{Record: memory.Record{ID: "mem-public", Channel: event.ChLive, Session: "room", Summary: "公开有效",
			Importance: 0.5, Confidence: 0.8, ValidFrom: now}, Source: "owner_direct", Kind: "note", Title: "公开"},
		{Record: memory.Record{ID: "mem-private", Channel: event.ChDM, Session: "owner", Private: true, Summary: "私密有效",
			Importance: 0.5, Confidence: 0.8, ValidFrom: now}, Source: "owner_direct", Kind: "note", Title: "私密"},
		{Record: memory.Record{ID: "mem-archived", Channel: event.ChLive, Session: "room", Summary: "已归档",
			Importance: 0.5, Confidence: 0.8, ValidFrom: now}, Source: "owner_direct", Kind: "note", Title: "归档"},
		{Record: memory.Record{ID: "mem-expired", Channel: event.ChLive, Session: "room", Summary: "已失效",
			Importance: 0.5, Confidence: 0.8, ValidFrom: now}, Source: "owner_direct", Kind: "note", Title: "失效"},
		{Record: memory.Record{ID: "mem-future", Channel: event.ChLive, Session: "room", Summary: "尚未生效",
			Importance: 0.5, Confidence: 0.8, ValidFrom: now.Add(time.Hour)}, Source: "owner_direct", Kind: "note", Title: "未来"},
	}
	for _, record := range records {
		if ok, err := repo.InsertRecord(ctx, record); err != nil || !ok {
			t.Fatalf("写入记忆 %s 失败：%v", record.ID, err)
		}
	}
	// 归档与失效没有仓储写入入口，直接改列模拟状态推进。
	if err := db.WithContext(ctx).Exec("UPDATE memory_records SET archived=TRUE WHERE id=?", "mem-archived").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Exec("UPDATE memory_records SET valid_to=? WHERE id=?", now.Add(-time.Minute), "mem-expired").Error; err != nil {
		t.Fatal(err)
	}

	ids := func(inputs []memory.EmbeddingInput) []string {
		out := make([]string, 0, len(inputs))
		for _, input := range inputs {
			out = append(out, input.ID)
		}
		return out
	}

	defaults, err := repo.MemoryEmbeddingInputs(ctx, false)
	if err != nil {
		t.Fatalf("默认读取候选失败：%v", err)
	}
	if got := ids(defaults); !slices.Equal(got, []string{"mem-public"}) {
		t.Fatalf("默认候选 = %v，期望只有公开有效行", got)
	}

	authorized, err := repo.MemoryEmbeddingInputs(ctx, true)
	if err != nil {
		t.Fatalf("授权读取候选失败：%v", err)
	}
	if got := ids(authorized); !slices.Equal(got, []string{"mem-private", "mem-public"}) {
		t.Fatalf("授权候选 = %v，期望公开+私密有效行", got)
	}
	privateOK := false
	for _, input := range authorized {
		if input.ID == "mem-private" {
			privateOK = input.Private && !input.Archived && input.ValidTo == nil
		}
	}
	if !privateOK {
		t.Fatalf("授权候选未带出私密标记：%+v", authorized)
	}
}
