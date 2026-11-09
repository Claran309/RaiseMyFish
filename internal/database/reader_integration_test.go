//go:build integration

package database

// 0.9c：Reader 装配的真实 PG 集成测试，重点验「公开范围读不到私密行」。

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// inEvent 造一条入向事件（未回复，用于 Recent/CrossChannel）。
func inEvent(id string, ch event.Channel, session string, private bool, at time.Time, text string) event.Event {
	return event.Event{
		ID: id, Channel: ch, Dir: event.DirIn, Session: session,
		FromID: "uid-1", FromName: "路人", Text: text, Private: private,
		At: at, Status: event.StatusPending,
	}
}

// mustInsertRecord 写一条记忆摘要，失败直接终止用例。
func mustInsertRecord(t *testing.T, ctx context.Context, repo *MemoryRepo, rec store.MemoryRecord) {
	t.Helper()
	ok, err := repo.InsertRecord(ctx, rec)
	if err != nil {
		t.Fatalf("写入记忆 %s 失败：%v", rec.ID, err)
	}
	if !ok {
		t.Fatalf("记忆 %s 未写入（主键冲突？）", rec.ID)
	}
}

// TestReaderRecallRealPostgres 验证装配结果，以及公开/私密隔离在真实 SQL 上成立。
func TestReaderRecallRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)

	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	memRepo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	seeds := []event.Event{
		inEvent("live:room:m1", event.ChLive, "room", false, base, "公开弹幕"),
		inEvent("dm:u9:m2", event.ChDM, "u9", true, base.Add(time.Second), "私信内容"),
		inEvent("comment:c1:m3", event.ChComment, "c1", false, base.Add(2*time.Second), "公开评论"),
	}
	for _, ev := range seeds {
		if _, err := events.Insert(ctx, ev); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
		}
	}

	mustInsertRecord(t, ctx, memRepo, store.MemoryRecord{
		Record: memory.Record{
			ID: "mem_pub", Channel: event.ChLive, Session: "room",
			Summary: "公开摘要", Importance: 0.9, Confidence: 0.9,
			ValidFrom: base, EventIDs: []string{"live:room:m1"},
		},
		Source: "owner_direct", Kind: "promise", Title: "公开标题",
	})
	mustInsertRecord(t, ctx, memRepo, store.MemoryRecord{
		Record: memory.Record{
			ID: "mem_priv", Channel: event.ChDM, Session: "u9",
			Summary: "私密摘要", Importance: 0.8, Confidence: 0.9,
			// 必须早于 now()，否则会被 valid_from <= now() 的有效性过滤挡掉
			Private: true, ValidFrom: base.Add(-time.Second), EventIDs: []string{"dm:u9:m2"},
		},
		Source: "owner_direct", Kind: "preference", Title: "私密标题",
	})

	reader, err := memory.NewReader(memRepo, nil, memory.ReaderOptions{})
	if err != nil {
		t.Fatalf("构造 Reader 失败：%v", err)
	}

	t.Run("公开范围读不到私密", func(t *testing.T) {
		got, err := reader.Recall(ctx, memory.Scope{Channel: event.ChLive, Session: "room"})
		if err != nil {
			t.Fatalf("Recall 失败：%v", err)
		}

		for _, m := range got.Recent {
			if m.Private {
				t.Errorf("Recent 混入私密消息：%+v", m)
			}
			if m.Channel != event.ChLive || m.Session != "room" {
				t.Errorf("Recent 混入其他会话：%+v", m)
			}
		}
		if len(got.Recent) != 1 {
			t.Errorf("Recent 条数 = %d，期望 1", len(got.Recent))
		}

		for _, m := range got.CrossChannel {
			if m.Private || m.Channel == event.ChLive {
				t.Errorf("CrossChannel 混入私密或同通道消息：%+v", m)
			}
		}
		if len(got.CrossChannel) != 1 || got.CrossChannel[0].Channel != event.ChComment {
			t.Errorf("CrossChannel 不符：%+v", got.CrossChannel)
		}

		if len(got.Records) != 1 {
			t.Fatalf("Records 条数 = %d，期望 1（只有公开摘要）", len(got.Records))
		}
		if got.Records[0].ID != "mem_pub" {
			t.Errorf("Records[0] = %q，期望 mem_pub", got.Records[0].ID)
		}
		for _, r := range got.Records {
			if r.Private {
				t.Errorf("公开范围读到私密摘要：%+v", r)
			}
		}
	})

	t.Run("私密范围可读本会话", func(t *testing.T) {
		got, err := reader.Recall(ctx, memory.Scope{Channel: event.ChDM, Session: "u9", Private: true})
		if err != nil {
			t.Fatalf("Recall 失败：%v", err)
		}

		seen := false
		for _, r := range got.Records {
			if r.ID == "mem_priv" {
				seen = true
				if !r.Private || r.Channel != event.ChDM || r.Session != "u9" {
					t.Errorf("私密摘要字段不符：%+v", r)
				}
			}
		}
		if !seen {
			t.Errorf("私密范围内应能读到 mem_priv，实际：%+v", got.Records)
		}

		for _, m := range got.Recent {
			if m.Channel != event.ChDM || m.Session != "u9" {
				t.Errorf("私密 Recent 混入其他会话：%+v", m)
			}
		}
	})

	t.Run("窗口上限生效", func(t *testing.T) {
		small, err := memory.NewReader(memRepo, nil, memory.ReaderOptions{RecordsLimit: 1})
		if err != nil {
			t.Fatalf("构造 Reader 失败：%v", err)
		}
		got, err := small.Recall(ctx, memory.Scope{Channel: event.ChLive, Session: "room"})
		if err != nil {
			t.Fatalf("Recall 失败：%v", err)
		}
		if len(got.Records) > 1 {
			t.Errorf("Records 条数 = %d，超过窗口上限 1", len(got.Records))
		}
	})
}

// TestFocusMessagesPairTurnsRealPostgres 验收 B1/N02：
// A 问、鱼回、B 插 30 条、A 追问之后，Recent(20) 已挤掉 A 的原问题，
// 但焦点窗口仍按 reply_to 给出「原问题 + 鱼的回复 + 本轮追问」。
func TestFocusMessagesPairTurnsRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	memRepo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(ev event.Event) {
		t.Helper()
		if _, err := events.Insert(ctx, ev); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
		}
	}
	liveScope := memory.Scope{Channel: event.ChLive, Session: "room"}
	outEvent := func(id, replyTo string, status event.Status, at time.Time) event.Event {
		return event.Event{
			ID: id, Channel: event.ChLive, Dir: event.DirOut, Session: "room",
			FromID: "9", FromName: "本鱼", Text: "鱼的回复 " + id, ReplyTo: replyTo,
			At: at, Status: status,
		}
	}

	// A 问 → 鱼回 → B 插 30 条 → A 追问。
	insert(event.Event{ID: "live:room:q1", Channel: event.ChLive, Dir: event.DirIn, Session: "room", FromID: "uid-a", FromName: "阿问", Text: "第一问：我是谁", At: base, Status: event.StatusPending})
	insert(outEvent("live:out:room:r1", "live:room:q1", event.StatusDone, base.Add(time.Second)))
	// 未确认成功的出向不能成为配对证据。
	insert(outEvent("live:out:room:r2", "live:room:q1", event.StatusRetry, base.Add(2*time.Second)))
	for i := 0; i < 30; i++ {
		insert(event.Event{
			ID: "live:room:b" + string(rune('a'+i/10)) + string(rune('0'+i%10)), Channel: event.ChLive,
			Dir: event.DirIn, Session: "room", FromID: "uid-b", FromName: "插话的",
			Text: "B 插话", At: base.Add(time.Duration(3+i) * time.Second), Status: event.StatusPending,
		})
	}
	insert(event.Event{ID: "live:room:q2", Channel: event.ChLive, Dir: event.DirIn, Session: "room", FromID: "uid-a", FromName: "阿问", Text: "第二问：还记得吗", At: base.Add(40 * time.Second), Status: event.StatusPending})

	recent, err := memRepo.Recent(ctx, liveScope, 20)
	if err != nil {
		t.Fatalf("Recent 失败：%v", err)
	}
	containsMessage := func(rows []memory.Message, text string) bool {
		for _, row := range rows {
			if row.Text == text {
				return true
			}
		}
		return false
	}
	if containsMessage(recent, "第一问：我是谁") {
		t.Fatalf("Recent(20) 本应挤掉 A 的原问题：%+v", recent)
	}
	if !containsMessage(recent, "第二问：还记得吗") {
		t.Fatalf("Recent(20) 应包含本轮追问：%+v", recent)
	}

	focus, err := memRepo.FocusMessages(ctx, memory.FocusQuery{Scope: liveScope, Speaker: "  uid-a  ", Limit: 10})
	if err != nil {
		t.Fatalf("FocusMessages 失败：%v", err)
	}
	if len(focus) != 3 {
		t.Fatalf("焦点轮次 = %d，期望 3（原问题/已确认回复/本轮追问）：%+v", len(focus), focus)
	}
	if focus[0].EventID != "live:room:q1" || focus[0].Text != "第一问：我是谁" {
		t.Fatalf("原问题丢失：%+v", focus[0])
	}
	if focus[1].EventID != "live:out:room:r1" || focus[1].ReplyTo != "live:room:q1" || focus[1].Dir != event.DirOut {
		t.Fatalf("鱼的回复或 reply_to 配对丢失：%+v", focus[1])
	}
	if focus[2].EventID != "live:room:q2" {
		t.Fatalf("本轮追问不在焦点末尾：%+v", focus)
	}
	for _, m := range focus {
		if m.Channel != event.ChLive || m.Session != "room" || m.Private {
			t.Fatalf("焦点轮次越界：%+v", m)
		}
	}

	// 只取该说话者：B 的插话不会混入 A 的焦点轮次。
	other, err := memRepo.FocusMessages(ctx, memory.FocusQuery{Scope: liveScope, Speaker: "uid-b", Limit: 10})
	if err != nil {
		t.Fatalf("FocusMessages 失败：%v", err)
	}
	if len(other) != 5 {
		t.Fatalf("limit=10 时入向配额应为 5，实际 %d：%+v", len(other), other)
	}
	for _, m := range other {
		if m.FromID != "uid-b" || m.Text == "第一问：我是谁" {
			t.Fatalf("焦点轮次混入其他说话者：%+v", m)
		}
	}

	// 窗口上限生效：limit=2 → 只取最新 1 条入向，且没有可配对回复。
	small, err := memRepo.FocusMessages(ctx, memory.FocusQuery{Scope: liveScope, Speaker: "uid-a", Limit: 2})
	if err != nil {
		t.Fatalf("FocusMessages 失败：%v", err)
	}
	if len(small) != 1 || small[0].EventID != "live:room:q2" {
		t.Fatalf("limit=2 结果不符：%+v", small)
	}

	// DM 私密会话按同样规则配对，且只在本会话内可见。
	dmScope := memory.Scope{Channel: event.ChDM, Session: "dm-a", Private: true}
	insert(event.Event{ID: "dm:dm-a:q1", Channel: event.ChDM, Dir: event.DirIn, Session: "dm-a", FromID: "uid-a", FromName: "阿问", Text: "私信问题", Private: true, At: base.Add(50 * time.Second), Status: event.StatusPending})
	insert(event.Event{ID: "dm:out:dm-a:r1", Channel: event.ChDM, Dir: event.DirOut, Session: "dm-a", FromID: "9", FromName: "本鱼", Text: "私信回复", Private: true, ReplyTo: "dm:dm-a:q1", At: base.Add(51 * time.Second), Status: event.StatusDone})
	dmFocus, err := memRepo.FocusMessages(ctx, memory.FocusQuery{Scope: dmScope, Speaker: "uid-a", Limit: 10})
	if err != nil {
		t.Fatalf("DM FocusMessages 失败：%v", err)
	}
	if len(dmFocus) != 2 || dmFocus[1].ReplyTo != "dm:dm-a:q1" {
		t.Fatalf("DM 焦点轮次不符：%+v", dmFocus)
	}
	for _, m := range dmFocus {
		if !m.Private || m.Channel != event.ChDM || m.Session != "dm-a" {
			t.Fatalf("DM 焦点轮次越界：%+v", m)
		}
	}
	if _, err := memRepo.FocusMessages(ctx, memory.FocusQuery{Scope: liveScope, Speaker: "   ", Limit: 10}); err == nil {
		t.Fatal("空 speaker 应报错")
	}
}
