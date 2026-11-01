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
