//go:build integration

package database

// 0.9c：DoneSource 的真实 PG 集成测试（游标推进、limit、只取 done）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/store"
)

// doneEvent 造一条「已完成」的出向事件（ValidateEvent 要求 out 方向配 done）。
func doneEvent(id string, ch event.Channel, session string, private bool, at time.Time) event.Event {
	return event.Event{
		ID:       id,
		Channel:  ch,
		Dir:      event.DirOut,
		Session:  session,
		FromID:   "uid-1",
		FromName: "路人",
		Text:     "已回复过了",
		Private:  private,
		At:       at,
		Status:   event.StatusDone,
	}
}

// seedDoneEvents 写入一批事件（含未完成的对照行）。
func seedDoneEvents(t *testing.T, ctx context.Context, repo *EventRepo) {
	t.Helper()
	base := time.Now().UTC().Truncate(time.Microsecond)

	events := []event.Event{
		doneEvent("live:room:e1", event.ChLive, "room", false, base),
		doneEvent("live:room:e2", event.ChLive, "room", false, base.Add(time.Second)),
		// 与 e2 同一时刻、更大 ID：用来验证 (at,id) 行值比较
		doneEvent("live:room:e3", event.ChLive, "room", false, base.Add(time.Second)),
		doneEvent("dm:u9:e4", event.ChDM, "u9", true, base.Add(2*time.Second)),
		// 未完成的事件，不该被扫到
		{
			ID: "live:room:p1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
			Text: "还没回", At: base.Add(3 * time.Second), Status: event.StatusPending,
		},
	}
	for _, ev := range events {
		if _, err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
		}
	}
}

// TestDoneSourceListsOnlyDone DoneSource 只返回 done 事件，且按 (at,id) 升序。
func TestDoneSourceListsOnlyDone(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seedDoneEvents(t, ctx, events)

	src, err := NewDoneSourceRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	got, err := src.ListDoneAfter(ctx, extractworker.Cursor{}, 10)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if len(got) != 4 {
		t.Fatalf("事件数 = %d，期望 4（只有 done）", len(got))
	}
	wantIDs := []string{"live:room:e1", "live:room:e2", "live:room:e3", "dm:u9:e4"}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Errorf("第 %d 条 = %q，期望 %q（顺序应为 at,id 升序）", i, got[i].ID, want)
		}
		if got[i].Status != event.StatusDone {
			t.Errorf("%q 状态 = %q，期望 done", got[i].ID, got[i].Status)
		}
	}
	// 私密事件也要被扫描到：是否提炼由 worker 授权决定，不在扫描层过滤
	if !got[3].Private || got[3].Channel != event.ChDM {
		t.Errorf("私密事件字段丢失：%+v", got[3])
	}
}

// TestDoneSourceCursorAndLimit 游标推进 + limit 生效，且不重不漏。
func TestDoneSourceCursorAndLimit(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	seedDoneEvents(t, ctx, events)

	src, err := NewDoneSourceRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	// 第一页：2 条
	page1, err := src.ListDoneAfter(ctx, extractworker.Cursor{}, 2)
	if err != nil {
		t.Fatalf("第一页失败：%v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("第一页条数 = %d，期望 2", len(page1))
	}

	// 用最后一条做游标取第二页（同 at 的 e3 不能丢）
	cursor := extractworker.Cursor{At: page1[1].At, ID: page1[1].ID}
	page2, err := src.ListDoneAfter(ctx, cursor, 2)
	if err != nil {
		t.Fatalf("第二页失败：%v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("第二页条数 = %d，期望 2", len(page2))
	}
	if page2[0].ID != "live:room:e3" {
		t.Errorf("第二页首条 = %q，期望 live:room:e3（同 at 靠 id 继续）", page2[0].ID)
	}

	// 第三页：空
	cursor = extractworker.Cursor{At: page2[1].At, ID: page2[1].ID}
	page3, err := src.ListDoneAfter(ctx, cursor, 2)
	if err != nil {
		t.Fatalf("第三页失败：%v", err)
	}
	if len(page3) != 0 {
		t.Errorf("第三页应为空，实际 %d 条", len(page3))
	}

	// 游标以后只返回更新的：用 e2 的游标应拿到 e3、e4
	mid := extractworker.Cursor{At: page1[1].At, ID: page1[1].ID}
	rest, err := src.ListDoneAfter(ctx, mid, 10)
	if err != nil {
		t.Fatalf("续扫失败：%v", err)
	}
	if len(rest) != 2 || rest[0].ID != "live:room:e3" || rest[1].ID != "dm:u9:e4" {
		t.Errorf("续扫结果不符：%+v", rest)
	}
}

// TestDoneSourceRejectsBadInput 非法参数在查询前被拒绝。
func TestDoneSourceRejectsBadInput(t *testing.T) {
	db, ctx := repositoryDB(t)
	src, err := NewDoneSourceRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	for _, limit := range []int{0, -1, doneSourceLimitMax + 1} {
		if _, err := src.ListDoneAfter(ctx, extractworker.Cursor{}, limit); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("limit=%d 应返回 ErrInvalid，实际 %v", limit, err)
		}
	}
	//nolint:staticcheck // 刻意传 nil 验证防御边界（SA1012）
	if _, err := src.ListDoneAfter(nil, extractworker.Cursor{}, 10); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("nil ctx 应返回 ErrInvalid，实际 %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := src.ListDoneAfter(canceled, extractworker.Cursor{}, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("取消应保留 context.Canceled，实际 %v", err)
	}

	if _, err := NewDoneSourceRepo(nil); err == nil {
		t.Error("nil db 应报错")
	}
}
