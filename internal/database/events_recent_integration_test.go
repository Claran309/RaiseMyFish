//go:build integration

package database

// T01：RecentEvents/RecentReplies 在真实 PG 上的房间、方向与状态回归。
// 曾因占位符顺序（channel,session,dir）与实参（ch,dir,session）错位，正常房间查询为空。

import (
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

// TestRecentEventsAndRepliesRealPostgres 插入 in/out/done/retry 四类事件，
// 断言 RecentEvents 只返回本房间入向，RecentReplies 只返回已确认送达的本房间出向。
func TestRecentEventsAndRepliesRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	const room = "30308220"
	base := time.Now().UTC().Truncate(time.Microsecond)
	seeds := []event.Event{
		{ID: "live:" + room + ":in-early", Channel: event.ChLive, Dir: event.DirIn, Session: room, FromID: "1001", FromName: "弓长三益", Text: "鱼在吗", At: base, Status: event.StatusPending},
		{ID: "live:" + room + ":in-late", Channel: event.ChLive, Dir: event.DirIn, Session: room, FromID: "1002", FromName: "夜猫子", Text: "刚下播？", At: base.Add(3 * time.Second), Status: event.StatusPending},
		{ID: "live:out:" + room + ":done-early", Channel: event.ChLive, Dir: event.DirOut, Session: room, FromID: "9", Text: "在的", At: base.Add(time.Second), Status: event.StatusDone},
		{ID: "live:out:" + room + ":retry", Channel: event.ChLive, Dir: event.DirOut, Session: room, FromID: "9", Text: "还没确认的回复", At: base.Add(2 * time.Second), Status: event.StatusRetry},
		{ID: "live:out:" + room + ":done-late", Channel: event.ChLive, Dir: event.DirOut, Session: room, FromID: "9", Text: "刚回来", At: base.Add(4 * time.Second), Status: event.StatusDone},
		{ID: "live:404:in-other", Channel: event.ChLive, Dir: event.DirIn, Session: "404", FromID: "1003", FromName: "别屋观众", Text: "别的房间", At: base.Add(5 * time.Second), Status: event.StatusPending},
		{ID: "live:out:404:done-other", Channel: event.ChLive, Dir: event.DirOut, Session: "404", FromID: "9", Text: "别的房间的回复", At: base.Add(6 * time.Second), Status: event.StatusDone},
	}
	for _, ev := range seeds {
		if _, err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
		}
	}

	events, err := repo.RecentEvents(ctx, memory.Scope{Channel: event.ChLive, Session: room}, 10)
	if err != nil {
		t.Fatalf("RecentEvents 失败：%v", err)
	}
	if len(events) != 2 {
		t.Fatalf("RecentEvents 应只返回本房间 2 条入向，实际 %d 条：%+v", len(events), events)
	}
	if events[0].ID != "live:"+room+":in-late" || events[1].ID != "live:"+room+":in-early" {
		t.Fatalf("RecentEvents 入向或排序不符：%+v", events)
	}
	for _, ev := range events {
		if ev.Dir != event.DirIn || ev.Session != room {
			t.Fatalf("RecentEvents 返回了非本房间入向：%+v", ev)
		}
	}

	replies, err := repo.RecentReplies(ctx, event.ChLive, room, 10)
	if err != nil {
		t.Fatalf("RecentReplies 失败：%v", err)
	}
	if len(replies) != 2 {
		t.Fatalf("RecentReplies 应只返回 2 条已确认出向，实际 %d 条：%+v", len(replies), replies)
	}
	if replies[0].ID != "live:out:"+room+":done-late" || replies[1].ID != "live:out:"+room+":done-early" {
		t.Fatalf("RecentReplies 状态过滤或排序不符：%+v", replies)
	}
	for _, ev := range replies {
		if ev.Dir != event.DirOut || ev.Session != room || ev.Status != event.StatusDone {
			t.Fatalf("RecentReplies 返回了未确认或非本房间出向：%+v", ev)
		}
	}

	limited, err := repo.RecentReplies(ctx, event.ChLive, room, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].ID != "live:out:"+room+":done-late" {
		t.Fatalf("RecentReplies limit 未生效：%+v", limited)
	}

	if got, err := repo.RecentReplies(ctx, event.ChLive, "unknown-room", 10); err != nil || len(got) != 0 {
		t.Fatalf("未知房间应返回空：err=%v rows=%+v", err, got)
	}
}

// TestRecentEventsBeforeUpperBound M07：历史回放只能读到严格早于当前事件的内容。
//
// 回归背景：补提炼一条 10:05 的旧事件时，RecentEvents 会返回 11:00 的发言，
// 把未来的话当成前文，提炼结果不可复现。
func TestRecentEventsBeforeUpperBound(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	const room = "30308220"
	base := time.Now().UTC().Truncate(time.Microsecond)
	early := event.Event{ID: "live:" + room + ":m07-early", Channel: event.ChLive, Dir: event.DirIn, Session: room, FromID: "1001", FromName: "弓长三益", Text: "十点说的话", At: base, Status: event.StatusPending}
	middle := event.Event{ID: "live:" + room + ":m07-mid", Channel: event.ChLive, Dir: event.DirIn, Session: room, FromID: "1002", FromName: "夜猫子", Text: "十点零五说的话", At: base.Add(5 * time.Minute), Status: event.StatusPending}
	late := event.Event{ID: "live:" + room + ":m07-late", Channel: event.ChLive, Dir: event.DirIn, Session: room, FromID: "1003", FromName: "路人丙", Text: "十一点说的话", At: base.Add(time.Hour), Status: event.StatusPending}
	for _, ev := range []event.Event{early, middle, late} {
		if _, err := repo.Insert(ctx, ev); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
		}
	}
	scope := memory.Scope{Channel: event.ChLive, Session: room}

	// 处理 middle：只应看到 early，绝不能看到 late。
	got, err := repo.RecentEventsBefore(ctx, scope, middle.At, middle.ID, 10)
	if err != nil {
		t.Fatalf("RecentEventsBefore 失败：%v", err)
	}
	if len(got) != 1 || got[0].ID != early.ID {
		t.Fatalf("上界查询结果不符：%+v", got)
	}

	// 处理 late：应看到 middle、early（倒序）。
	got, err = repo.RecentEventsBefore(ctx, scope, late.At, late.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != middle.ID || got[1].ID != early.ID {
		t.Fatalf("上界查询（含两条历史）不符：%+v", got)
	}

	// 零值上界直接拒绝：调用方必须显式给出边界，不能退化成「最新 N 条」。
	if _, err := repo.RecentEventsBefore(ctx, scope, time.Time{}, "", 10); err == nil {
		t.Fatal("零值上界应被拒绝")
	}
}
