//go:build integration

package database

// 0.9d：未来时间事件的端到端验证——worker 落库后必须能被真实召回读到。

import (
	"math"
	"strings"
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

// TestWorkerPublicFactRevisionAcrossLiveAndComment 公开事实的规范键跨来源共享：
// 直播先自述「大二」，评论后自述「大三」，必须修订同一条记录并保留新来源；
// 旧事件晚到重扫不能把事实改回旧值。
func TestWorkerPublicFactRevisionAcrossLiveAndComment(t *testing.T) {
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

	// 标题截断到 57 字，差异放在 60 字之后：两条候选共享同一规范键。
	common := "我是学生" + strings.Repeat("甲", 60)
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Microsecond)
	live := doneEvent("live:room:upd2", event.ChLive, "room", false, base)
	live.FromID, live.FromName = "2002", "熟客乙"
	live.Text = common + "大二"
	comment := doneEvent("comment:oid-777:upd2", event.ChComment, "oid-777", false, base.Add(time.Hour))
	comment.FromID, comment.FromName = "2002", "熟客乙"
	comment.Text = common + "大三"
	for _, ev := range []event.Event{live, comment} {
		if _, err := events.Insert(ctx, ev); err != nil {
			t.Fatalf("插入事件 %s 失败：%v", ev.ID, err)
		}
	}

	worker, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{})
	if err != nil {
		t.Fatalf("构造 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 2 || stats.Duplicated != 0 {
		t.Fatalf("统计 = %+v，期望 Inserted=2 Duplicated=0（首写+跨来源修订）", stats)
	}

	rows, err := memRepo.ListRecords(ctx, 10)
	if err != nil {
		t.Fatalf("读取记忆记录失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("记录数 = %d，期望 1（公开规范键跨来源共享）：%+v", len(rows), rows)
	}
	got := rows[0]
	if got.Summary != comment.Text {
		t.Fatalf("公开事实没有更新到评论来源：%q", got.Summary)
	}
	if got.Channel != event.ChComment || got.Session != "oid-777" {
		t.Fatalf("修订后的范围不是新来源：%+v", got)
	}
	if len(got.EventIDs) != 1 || got.EventIDs[0] != comment.ID {
		t.Fatalf("修订未保留新来源事件：%+v", got.EventIDs)
	}
	if !got.ValidFrom.Equal(comment.At) {
		t.Fatalf("valid_from = %v，期望 %v", got.ValidFrom, comment.At)
	}

	// 逆序回填：补一条更早的直播事件，零水位重扫不得把事实改回旧值。
	older := doneEvent("live:room:older2", event.ChLive, "room", false, base.Add(30*time.Minute))
	older.FromID, older.FromName = "2002", "熟客乙"
	older.Text = common + "大一"
	if _, err := events.Insert(ctx, older); err != nil {
		t.Fatalf("插入旧事件失败：%v", err)
	}
	backfill, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{})
	if err != nil {
		t.Fatalf("构造补提炼 worker 失败：%v", err)
	}
	stats, err = backfill.RunOnce(ctx)
	if err != nil {
		t.Fatalf("补提炼 RunOnce 失败：%v", err)
	}
	if stats.Inserted != 0 || stats.Duplicated != 3 {
		t.Fatalf("补提炼统计 = %+v，期望 Inserted=0 Duplicated=3（旧被拒、新幂等）", stats)
	}
	rows, err = memRepo.ListRecords(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("补提炼后记录异常：%+v / %v", rows, err)
	}
	if rows[0].Summary != comment.Text || rows[0].Channel != event.ChComment || !rows[0].ValidFrom.Equal(comment.At) {
		t.Fatalf("旧事件覆盖了跨来源新事实：%+v", rows[0])
	}
}
