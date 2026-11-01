//go:build integration

package database

// 0.9d：共享事实查询的真实 PG 集成测试（公开/私密隔离、有效性过滤）。

import (
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// factRecord 造一条 kind=fact 的记录：事实复用 memory_records 存储。
func factRecord(id, title, value string, ch event.Channel, session string, private bool, validFrom time.Time) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{
			ID: id, Channel: ch, Session: session, Summary: value,
			Importance: 0.7, Confidence: 0.9, Private: private,
			ValidFrom: validFrom, EventIDs: []string{string(ch) + ":fact:" + id},
		},
		Source: "owner_direct", Kind: "fact", Title: title,
	}
}

// TestFactsRealPostgres 事实查询：只给当前有效行，公开/私密严格隔离。
func TestFactsRealPostgres(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	past := base.Add(-time.Minute)

	mustInsertRecord(t, ctx, repo, factRecord("fact_pub", "存粮", "未知", event.ChLive, "room", false, past))
	mustInsertRecord(t, ctx, repo, factRecord("fact_priv", "私密事实", "私密值", event.ChDM, "u9", true, past))
	// 未来生效：不该出现在「当前有效」里
	mustInsertRecord(t, ctx, repo, factRecord("fact_future", "未来事实", "还没发生", event.ChLive, "room", false, base.Add(time.Hour)))
	// 已归档：不该出现
	mustInsertRecord(t, ctx, repo, factRecord("fact_archived", "已归档", "旧值", event.ChLive, "room", false, past))
	// 已失效（valid_to 已过）：不该出现
	mustInsertRecord(t, ctx, repo, factRecord("fact_expired", "过期事实", "旧值", event.ChLive, "room", false, base.Add(-time.Hour)))
	// 非 fact 类型：不该被当成事实
	mustInsertRecord(t, ctx, repo, store.MemoryRecord{
		Record: memory.Record{
			ID: "mem_promise", Channel: event.ChLive, Session: "room",
			Summary: "承诺摘要", Importance: 0.9, Confidence: 0.9, ValidFrom: past,
			EventIDs: []string{"live:room:p1"},
		},
		Source: "owner_direct", Kind: "promise", Title: "承诺",
	})

	if err := db.WithContext(ctx).Exec("UPDATE memory_records SET archived = true WHERE id = 'fact_archived'").Error; err != nil {
		t.Fatalf("归档标记失败：%v", err)
	}
	if err := db.WithContext(ctx).Exec("UPDATE memory_records SET valid_to = ? WHERE id = 'fact_expired'", base.Add(-time.Minute)).Error; err != nil {
		t.Fatalf("失效标记失败：%v", err)
	}

	t.Run("公开范围只给公开事实", func(t *testing.T) {
		got, err := repo.Facts(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, 10)
		if err != nil {
			t.Fatalf("Facts 失败：%v", err)
		}
		if len(got) != 1 {
			t.Fatalf("事实数 = %d，期望 1（只有「存粮」）：%+v", len(got), got)
		}
		f := got[0]
		if f.Key != "存粮" || f.Value != "未知" {
			t.Errorf("事实内容不符：%+v", f)
		}
		if f.Private || f.Channel != event.ChLive || f.Session != "room" {
			t.Errorf("范围字段不符：%+v", f)
		}
		if f.SourceID == "" || f.At.IsZero() {
			t.Errorf("缺少来源或观测时间：%+v", f)
		}
	})

	t.Run("私密范围可读本会话私密事实", func(t *testing.T) {
		got, err := repo.Facts(ctx, memory.Scope{Channel: event.ChDM, Session: "u9", Private: true}, 10)
		if err != nil {
			t.Fatalf("Facts 失败：%v", err)
		}
		var sawPrivate bool
		for _, f := range got {
			if f.Key == "私密事实" {
				sawPrivate = true
				if !f.Private || f.Channel != event.ChDM || f.Session != "u9" {
					t.Errorf("私密事实字段不符：%+v", f)
				}
			}
		}
		if !sawPrivate {
			t.Errorf("私密范围内应能读到「私密事实」，实际：%+v", got)
		}
	})

	t.Run("limit 生效", func(t *testing.T) {
		got, err := repo.Facts(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, 1)
		if err != nil {
			t.Fatalf("Facts 失败：%v", err)
		}
		if len(got) > 1 {
			t.Errorf("事实数 = %d，超过 limit 1", len(got))
		}
	})

	t.Run("非法参数被拒", func(t *testing.T) {
		if _, err := repo.Facts(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, 0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("limit=0 应 ErrInvalid，实际 %v", err)
		}
		if _, err := repo.Facts(ctx, memory.Scope{Channel: "telegram", Session: "x"}, 10); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("非法 scope 应 ErrInvalid，实际 %v", err)
		}
	})

	t.Run("可直接作为 Reader 的事实来源", func(t *testing.T) {
		reader, err := memory.NewReader(repo, repo, memory.ReaderOptions{})
		if err != nil {
			t.Fatalf("构造 Reader 失败：%v", err)
		}
		state, err := reader.Shared(ctx, memory.Scope{Channel: event.ChLive, Session: "room"})
		if err != nil {
			t.Fatalf("Shared 失败：%v", err)
		}
		if len(state.Facts) != 1 || state.Facts[0].Key != "存粮" {
			t.Errorf("Shared 事实不符：%+v", state.Facts)
		}
	})
}
