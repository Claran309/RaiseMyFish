//go:build integration

package database

// 本文件用真实 PG 测日报仓储：按天半开区间取事件、日报 upsert 与列表顺序。

import (
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

func TestDiaryRepositoryRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewWebRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)
	insert := func(id, text string, at time.Time) {
		t.Helper()
		row := testEvent(id, event.ChLive, "room", false, at)
		row.FromName, row.Text = "观众甲", text
		if ok, err := events.Insert(ctx, row); err != nil || !ok {
			t.Fatalf("写事件 %s 失败：%v ok=%v", id, err, ok)
		}
	}
	insert("live:diary:before", "边界前", from.Add(-time.Second))
	insert("live:diary:first", "当天最早", from)
	insert("live:diary:second", "当天较晚", from.Add(8*time.Hour))
	insert("live:diary:after", "边界后", to)

	rows, err := repo.DayEvents(ctx, from, to, 10)
	if err != nil {
		t.Fatalf("读当天事件失败：%v", err)
	}
	if len(rows) != 2 || rows[0].ID != "live:diary:second" || rows[1].ID != "live:diary:first" {
		t.Fatalf("半开区间或排序错误：%+v", rows)
	}
	if rows[0].FromName != "观众甲" || rows[0].Text != "当天较晚" {
		t.Fatalf("事件字段错误：%+v", rows[0])
	}
	limited, err := repo.DayEvents(ctx, from, to, 1)
	if err != nil || len(limited) != 1 || limited[0].ID != "live:diary:second" {
		t.Fatalf("limit 未生效：%+v %v", limited, err)
	}
	if _, err := repo.DayEvents(ctx, from, from, 10); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("空区间应 invalid：%v", err)
	}
	if _, err := repo.DayEvents(ctx, from, to, 0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("limit=0 应 invalid：%v", err)
	}

	if err := repo.SaveDailySummary(ctx, store.DailySummary{Day: "2026-11-10", Content: "第一版", Model: "m1", EventCount: 2}); err != nil {
		t.Fatalf("保存日报失败：%v", err)
	}
	first, err := repo.DailySummary(ctx, "2026-11-10")
	if err != nil {
		t.Fatalf("读取日报失败：%v", err)
	}
	if first.Day != "2026-11-10" || first.Content != "第一版" || first.Model != "m1" || first.EventCount != 2 || first.CreatedAt.IsZero() {
		t.Fatalf("日报字段错误：%+v", first)
	}
	// 同一天重复生成必须覆盖，不能报冲突或插入第二行。
	if err := repo.SaveDailySummary(ctx, store.DailySummary{Day: "2026-11-10", Content: "第二版", Model: "m2", EventCount: 5}); err != nil {
		t.Fatalf("覆盖日报失败：%v", err)
	}
	second, err := repo.DailySummary(ctx, "2026-11-10")
	if err != nil || second.Content != "第二版" || second.Model != "m2" || second.EventCount != 5 {
		t.Fatalf("覆盖结果错误：%+v %v", second, err)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) || second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("覆盖不应改创建时间：first=%+v second=%+v", first, second)
	}
	if err := repo.SaveDailySummary(ctx, store.DailySummary{Day: "2026-11-11", Content: "次日", Model: "m2", EventCount: 1}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.DailySummary(ctx, "2026-01-01"); !errors.Is(err, store.ErrDiaryNotFound) {
		t.Fatalf("不存在应返回 not found：%v", err)
	}
	if _, err := repo.DailySummary(ctx, "2026/11/10"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("非法日期应 invalid：%v", err)
	}
	if err := repo.SaveDailySummary(ctx, store.DailySummary{Day: "2026-11-12", Content: "  "}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("空内容应 invalid：%v", err)
	}

	list, err := repo.DailySummaries(ctx, 10)
	if err != nil {
		t.Fatalf("读列表失败：%v", err)
	}
	if len(list) != 2 || list[0].Day != "2026-11-11" || list[1].Day != "2026-11-10" {
		t.Fatalf("列表应按日期倒序：%+v", list)
	}
	one, err := repo.DailySummaries(ctx, 1)
	if err != nil || len(one) != 1 || one[0].Day != "2026-11-11" {
		t.Fatalf("列表 limit 未生效：%+v %v", one, err)
	}
	if _, err := repo.DailySummaries(ctx, 0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("列表 limit=0 应 invalid：%v", err)
	}
}
