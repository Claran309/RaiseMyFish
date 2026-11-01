//go:build integration

package database

// 0.9d：未来时间事件的端到端验证——worker 落库后必须能被真实召回读到。

import (
	"math"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
)

// TestWorkerFutureEventRecordVisible 事件时间在未来时，worker 把 valid_from 夹到 now，
// 记录仍然落在 `valid_from <= now()` 之内，能被 Records 召回。
func TestWorkerFutureEventRecordVisible(t *testing.T) {
	db, ctx := repositoryDB(t)

	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	memRepo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	src, err := NewDoneSourceRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	future := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	ev := doneEvent("live:room:fut1", event.ChLive, "room", false, future)
	ev.Text = "我保证明天带话"
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatalf("插入未来事件失败：%v", err)
	}

	worker, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{})
	if err != nil {
		t.Fatalf("构造 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 1 {
		t.Fatalf("Inserted = %d，期望 1：%+v", stats.Inserted, stats)
	}

	// 应用时钟与数据库时钟可能有微小偏差，等一下再查，避免把抖动当成 bug
	time.Sleep(5 * time.Millisecond)

	records, err := memRepo.Records(ctx, memory.Scope{Channel: event.ChLive, Session: "room"}, 10)
	if err != nil {
		t.Fatalf("召回失败：%v", err)
	}
	if len(records) != 1 {
		t.Fatalf("召回 %d 条，期望 1（valid_from 在未来会被过滤掉）", len(records))
	}
	// importance 在 PG 里是 REAL（float32），不能和 0.9 做精确比较
	if math.Abs(records[0].Importance-0.9) > 1e-6 {
		t.Errorf("Importance = %v，期望约 0.9（承诺规则）", records[0].Importance)
	}
}
