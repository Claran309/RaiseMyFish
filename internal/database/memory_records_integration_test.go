//go:build integration

package database

// 0.5 补丁：fishctl memory 依赖的只读查询（list/search/count）真实 PG 验证。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// seedMemoryRecords 写入四条记录并用固定 created_at 排定顺序。
func seedMemoryRecords(t *testing.T, ctx context.Context, repo *MemoryRepo) time.Time {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	records := []store.MemoryRecord{
		{
			Record: memory.Record{ID: "mem-new", Channel: event.ChLive, Session: "room", Summary: "猫今天很乖",
				Importance: 0.9, Confidence: 0.8, EventIDs: []string{"live:room:1"}, ValidFrom: now.Add(-time.Hour)},
			Source: "owner_direct", Kind: "fact", Title: "猫", Body: "完整正文",
		},
		{
			Record: memory.Record{ID: "mem-mid", Channel: event.ChLive, Session: "room", Summary: "今天晴天",
				Importance: 0.5, Confidence: 0.5, ValidFrom: now.Add(-2 * time.Hour)},
			Source: "owner_direct", Kind: "note", Title: "天气",
		},
		{
			Record: memory.Record{ID: "mem-old", Channel: event.ChLive, Session: "room", Summary: "明天带话",
				Importance: 0.8, Confidence: 0.9, ValidFrom: now.Add(-3 * time.Hour)},
			Source: "owner_direct", Kind: "promise", Title: "带话",
		},
		{
			Record: memory.Record{ID: "mem-private", Channel: event.ChDM, Session: "owner", Private: true,
				Summary: "只给主人", Importance: 0.7, Confidence: 0.9, ValidFrom: now.Add(-4 * time.Hour)},
			Source: "owner_direct", Kind: "fact", Title: "私密",
		},
	}
	created := map[string]time.Time{
		"mem-new":     now.Add(-time.Minute),
		"mem-mid":     now.Add(-time.Hour),
		"mem-old":     now.Add(-2 * time.Hour),
		"mem-private": now.Add(-3 * time.Hour),
	}
	for _, rec := range records {
		if ok, err := repo.InsertRecord(ctx, rec); err != nil || !ok {
			t.Fatalf("写入记忆 %s 失败：%v", rec.ID, err)
		}
		if err := repo.db.WithContext(ctx).Exec("UPDATE memory_records SET created_at=? WHERE id=?", created[rec.ID], rec.ID).Error; err != nil {
			t.Fatalf("设置创建时间 %s 失败：%v", rec.ID, err)
		}
	}
	return now
}

// TestMemoryListRecords 最近优先、limit 生效、字段映射完整。
func TestMemoryListRecords(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	now := seedMemoryRecords(t, ctx, repo)

	records, err := repo.ListRecords(ctx, 2)
	if err != nil {
		t.Fatalf("ListRecords 失败：%v", err)
	}
	if len(records) != 2 || records[0].ID != "mem-new" || records[1].ID != "mem-mid" {
		t.Fatalf("最近记录顺序不符：%+v", records)
	}
	top := records[0]
	if top.Source != "owner_direct" || top.Kind != "fact" || top.Title != "猫" || top.Body != "完整正文" {
		t.Errorf("字段映射不符：%+v", top)
	}
	if !top.CreatedAt.Equal(now.Add(-time.Minute)) {
		t.Errorf("CreatedAt = %v，期望 %v", top.CreatedAt, now.Add(-time.Minute))
	}
	if len(top.EventIDs) != 1 || top.EventIDs[0] != "live:room:1" {
		t.Errorf("EventIDs = %v", top.EventIDs)
	}

	all, err := repo.ListRecords(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecords 全量失败：%v", err)
	}
	want := []string{"mem-new", "mem-mid", "mem-old", "mem-private"}
	if len(all) != len(want) {
		t.Fatalf("全量条数 = %d，期望 %d", len(all), len(want))
	}
	for i, id := range want {
		if all[i].ID != id {
			t.Errorf("第 %d 条 = %q，期望 %q", i, all[i].ID, id)
		}
	}
	// 运维查询需要看到私密与归档之外的完整存量，这里确认私密行也返回
	if !all[3].Private || all[3].Channel != event.ChDM {
		t.Errorf("私密记录字段丢失：%+v", all[3])
	}
}

// TestMemorySearchRecords title/summary 均可命中，且注入串不会扩大结果。
func TestMemorySearchRecords(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seedMemoryRecords(t, ctx, repo)

	cases := map[string]string{
		"猫":            "mem-new",
		"带话":           "mem-old",
		"晴天":           "mem-mid",
		"只给主人":         "mem-private",
		"' OR true --": "",
	}
	for query, wantID := range cases {
		records, err := repo.SearchRecords(ctx, query, 10)
		if err != nil {
			t.Fatalf("SearchRecords(%q) 失败：%v", query, err)
		}
		if wantID == "" {
			if len(records) != 0 {
				t.Errorf("注入串 %q 返回了 %d 条", query, len(records))
			}
			continue
		}
		if len(records) != 1 || records[0].ID != wantID {
			t.Errorf("SearchRecords(%q) = %+v，期望 %s", query, records, wantID)
		}
	}
}

// TestMemoryCountByKind 按 kind 分组计数，空表返回空 map。
func TestMemoryCountByKind(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	empty, err := repo.CountByKind(ctx)
	if err != nil {
		t.Fatalf("空表统计失败：%v", err)
	}
	if len(empty) != 0 {
		t.Errorf("空表统计 = %v，期望空", empty)
	}

	seedMemoryRecords(t, ctx, repo)
	counts, err := repo.CountByKind(ctx)
	if err != nil {
		t.Fatalf("CountByKind 失败：%v", err)
	}
	want := map[string]int{"fact": 2, "note": 1, "promise": 1}
	if len(counts) != len(want) {
		t.Fatalf("统计 = %v，期望 %v", counts, want)
	}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("%s = %d，期望 %d", kind, counts[kind], n)
		}
	}
}

// TestMemoryReadQueriesRejectBadInput 非法参数在查询前拒绝，取消保留原因。
func TestMemoryReadQueriesRejectBadInput(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	for _, limit := range []int{0, -1, memoryRecordLimitMax + 1} {
		if _, err := repo.ListRecords(ctx, limit); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("ListRecords limit=%d 应返回 ErrInvalid，实际 %v", limit, err)
		}
		if _, err := repo.SearchRecords(ctx, "猫", limit); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("SearchRecords limit=%d 应返回 ErrInvalid，实际 %v", limit, err)
		}
	}
	for _, query := range []string{"", "   ", strings.Repeat("猫", memorySearchMaxRunes+1)} {
		if _, err := repo.SearchRecords(ctx, query, 10); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("SearchRecords(%q) 应返回 ErrInvalid，实际 %v", query, err)
		}
	}

	//nolint:staticcheck // 刻意传 nil 验证防御边界（SA1012）
	if _, err := repo.ListRecords(nil, 10); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("nil ctx 应返回 ErrInvalid，实际 %v", err)
	}
	//nolint:staticcheck // 同上
	if _, err := repo.CountByKind(nil); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("统计 nil ctx 应返回 ErrInvalid，实际 %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.ListRecords(canceled, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("列表取消未保留原因：%v", err)
	}
	if _, err := repo.SearchRecords(canceled, "猫", 10); !errors.Is(err, context.Canceled) {
		t.Errorf("搜索取消未保留原因：%v", err)
	}
	if _, err := repo.CountByKind(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("统计取消未保留原因：%v", err)
	}
}
