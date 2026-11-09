//go:build integration

package database

// M05/T07：提炼任务表的真实 PG 验收——建表、入队、领取、完成、失败与租约回收。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/store"
)

// inboundEvent 造一条入向事件（默认 pending，即「还没被回复」）。
func inboundEvent(id string, private bool, at time.Time, text string) event.Event {
	return event.Event{
		ID: id, Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "2002", FromName: "熟客乙", Text: text, Private: private,
		At: at, Status: event.StatusPending,
	}
}

// jobState 读回一条任务的状态（直接查库，避免被上层投影掩盖）。
func jobState(t *testing.T, ctx context.Context, repo *ExtractionJobsRepo, eventID string) (string, int, string) {
	t.Helper()
	var status string
	var attempts int
	var lastError string
	row := repo.db.WithContext(ctx).Raw(
		"SELECT status, attempts, COALESCE(last_error,'') FROM extraction_jobs WHERE event_id = ?", eventID,
	).Row()
	if err := row.Scan(&status, &attempts, &lastError); err != nil {
		t.Fatalf("读取任务状态失败：%v", err)
	}
	return status, attempts, lastError
}

// TestExtractionJobsSchemaReady 迁移 0009 建表后 Ready 必须通过。
func TestExtractionJobsSchemaReady(t *testing.T) {
	db, ctx := repositoryDB(t)
	if err := Ready(ctx, db); err != nil {
		t.Fatalf("Ready 未通过：%v", err)
	}
	var exists bool
	if err := db.WithContext(ctx).Raw(
		"SELECT to_regclass('extraction_jobs') IS NOT NULL").Scan(&exists).Error; err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("extraction_jobs 表未创建")
	}
}

// TestExtractionJobsEnqueueOnlyInbound 「有价值」= 入向且正文非空；不看 status。
func TestExtractionJobsEnqueueOnlyInbound(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	// 未回复的入向消息（status=pending）→ 入队
	if _, err := events.Insert(ctx, inboundEvent("live:room:e1", false, base, "我保证明天带话")); err != nil {
		t.Fatal(err)
	}
	// 私密入向 → 同样入队（是否提炼由 worker 的 AllowPrivate 决定）
	dm := inboundEvent("dm:u1:e2", true, base.Add(time.Second), "我是学生")
	dm.Channel, dm.Session = event.ChDM, "u1"
	if _, err := events.Insert(ctx, dm); err != nil {
		t.Fatal(err)
	}
	// 入向但空正文 → 不入队。
	// EventRepo.Insert 会拒绝空文本，这里直接用 SQL 造出这条脏数据，
	// 验证 Enqueue 自己的 btrim 过滤（防御历史数据/异常写入）。
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO events (id,channel,dir,session,from_id,from_name,text,private,at,status)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"live:room:e3", string(event.ChLive), string(event.DirIn), "room", "", "", "", false, base.Add(2*time.Second), string(event.StatusPending),
	).Error; err != nil {
		t.Fatalf("插入空正文事件失败：%v", err)
	}
	// 出向（鱼自己说的）→ 不入队
	out := inboundEvent("live:out:room:e4", false, base.Add(3*time.Second), "在的")
	out.Dir = event.DirOut
	letOut := out
	letOut.Status = event.StatusDone
	if _, err := events.Insert(ctx, letOut); err != nil {
		t.Fatal(err)
	}

	// 被调度器跳过的入向弹幕（status=skipped，如「没 @ 鱼」）→ 仍要入队。
	// 这类消息从来没被回复过，但内容是记忆的重要来源（T07）。
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO events (id,channel,dir,session,from_id,from_name,text,private,at,status)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"live:room:e5", string(event.ChLive), string(event.DirIn), "room", "2003", "路人丁", "我是大三学生", false, base.Add(4*time.Second), string(event.StatusSkipped),
	).Error; err != nil {
		t.Fatalf("插入 skipped 事件失败：%v", err)
	}

	n, err := repo.Enqueue(ctx, 100)
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	if n != 3 {
		t.Fatalf("入队条数 = %d，期望 3（公开+私密+skipped 入向）", n)
	}

	// 幂等：再入队一次不新增。
	again, err := repo.Enqueue(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("重复入队应为 0，实际 %d", again)
	}

	for _, id := range []string{"live:room:e1", "dm:u1:e2"} {
		if status, _, _ := jobState(t, ctx, repo, id); status != "pending" {
			t.Errorf("%s 状态 = %q，期望 pending", id, status)
		}
	}
}

// TestExtractionJobsClaimAndComplete 领取带出事件正文，完成释放租约。
func TestExtractionJobsClaimAndComplete(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := inboundEvent("live:room:c1", false, base, "我保证明天带话")
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(ctx, 10); err != nil {
		t.Fatal(err)
	}

	jobs, err := repo.Claim(ctx, "worker-a", time.Minute, 10)
	if err != nil {
		t.Fatalf("领取失败：%v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("应领取 1 条，实际 %d", len(jobs))
	}
	job := jobs[0]
	if job.EventID != ev.ID || job.Event.ID != ev.ID || job.Event.Text != ev.Text {
		t.Fatalf("任务未带出事件正文：%+v", job)
	}
	if job.Attempts != 1 || job.Status != extractworker.JobRunning || job.LeaseOwner != "worker-a" {
		t.Fatalf("领取语义不符：%+v", job)
	}

	// 另一个 worker 拿不到被租约锁住的任务。
	other, err := repo.Claim(ctx, "worker-b", time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("运行中的任务不应被重复领取：%+v", other)
	}

	if err := repo.Complete(ctx, job); err != nil {
		t.Fatalf("完成失败：%v", err)
	}
	if status, attempts, _ := jobState(t, ctx, repo, ev.ID); status != "done" || attempts != 1 {
		t.Fatalf("完成状态不符：status=%s attempts=%d", status, attempts)
	}
}

// TestExtractionJobsFailRetryThenExhaust 可重试回 pending，超上限置 failed。
func TestExtractionJobsFailRetryThenExhaust(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := inboundEvent("live:room:f1", false, base, "我保证明天带话")
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(ctx, 10); err != nil {
		t.Fatal(err)
	}

	// 第一次失败：maxAttempts=2，attempts=1 → 回 pending
	first, err := repo.Claim(ctx, "worker-a", time.Minute, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("首次领取失败：%v %+v", err, first)
	}
	if err := repo.Fail(ctx, first[0], "写入记忆记录失败", true, 2); err != nil {
		t.Fatalf("记录失败出错：%v", err)
	}
	status, attempts, lastError := jobState(t, ctx, repo, ev.ID)
	if status != "pending" || attempts != 1 || lastError == "" {
		t.Fatalf("可重试失败应回 pending：status=%s attempts=%d err=%q", status, attempts, lastError)
	}

	// 第二次失败：attempts=2，达到上限 → failed
	second, err := repo.Claim(ctx, "worker-a", time.Minute, 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("二次领取失败：%v %+v", err, second)
	}
	if err := repo.Fail(ctx, second[0], "写入记忆记录失败", true, 2); err != nil {
		t.Fatalf("记录失败出错：%v", err)
	}
	if status, attempts, _ := jobState(t, ctx, repo, ev.ID); status != "failed" || attempts != 2 {
		t.Fatalf("超过上限应置 failed：status=%s attempts=%d", status, attempts)
	}
	// failed 不再被领取。
	if more, err := repo.Claim(ctx, "worker-a", time.Minute, 10); err != nil || len(more) != 0 {
		t.Fatalf("failed 任务不应被领取：%v %+v", err, more)
	}
}

// TestExtractionJobsExpiredLeaseReclaimed 租约过期后可被其他 worker 接手。
func TestExtractionJobsExpiredLeaseReclaimed(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := inboundEvent("live:room:l1", false, base, "我保证明天带话")
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, "worker-a", time.Second, 10); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1100 * time.Millisecond)

	jobs, err := repo.Claim(ctx, "worker-b", time.Minute, 10)
	if err != nil {
		t.Fatalf("回收失败：%v", err)
	}
	if len(jobs) != 1 || jobs[0].LeaseOwner != "worker-b" || jobs[0].Attempts != 2 {
		t.Fatalf("过期租约应被接手：%+v", jobs)
	}

	// 旧持有者的完成请求必须被拒绝（租约栅栏）。
	stale := jobs[0]
	stale.LeaseOwner = "worker-a"
	if err := repo.Complete(ctx, stale); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("旧租约完成应冲突，实际 %v", err)
	}
}

// TestExtractionJobsLateDoneIgnoredByTime 晚完成/时间很早的事件照样入队并领取。
func TestExtractionJobsLateDoneIgnoredByTime(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	// 事件时间是 6 小时前：旧的「事件时间水位」会把这类事件跳过。
	veryOld := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Microsecond)
	ev := inboundEvent("live:room:late1", false, veryOld, "我是学生")
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}

	n, err := repo.Enqueue(ctx, 10)
	if err != nil || n != 1 {
		t.Fatalf("晚事件应入队：n=%d err=%v", n, err)
	}
	jobs, err := repo.Claim(ctx, "worker-a", time.Minute, 10)
	if err != nil || len(jobs) != 1 || jobs[0].EventID != ev.ID {
		t.Fatalf("晚事件应可领取：%v %+v", err, jobs)
	}
}

// TestExtractionWorkerJobQueueEndToEnd T07 端到端：
// 未回复的入向弹幕 → 入队 → 领取 → 提炼 → 落库 → 任务完成；再跑一轮不重复。
func TestExtractionWorkerJobQueueEndToEnd(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	src, err := NewDoneSourceRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	memRepo, err := NewMemoryRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	jobsRepo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	// status=pending：这条弹幕从没被回复过，旧实现永远不会提炼它。
	ev := inboundEvent("live:room:job1", false, base, "我保证明天带话")
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}

	worker, err := extractworker.New(src, memRepo, extractworker.NewMemoryWatermark(), extractworker.Options{
		Jobs:     jobsRepo,
		JobOwner: "integration-test",
	})
	if err != nil {
		t.Fatalf("构造 worker 失败：%v", err)
	}
	stats, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Enqueued != 1 || stats.Inserted != 1 {
		t.Fatalf("统计不符：%+v", stats)
	}

	rows, err := memRepo.ListRecords(ctx, 10)
	if err != nil {
		t.Fatalf("读取记录失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("记录数 = %d，期望 1", len(rows))
	}
	var fromID string
	row := db.WithContext(ctx).Raw(
		"SELECT COALESCE(from_id,'') FROM memory_records WHERE id = ?", rows[0].ID).Row()
	if err := row.Scan(&fromID); err != nil {
		t.Fatalf("读取主体失败：%v", err)
	}
	if fromID != "2002" {
		t.Fatalf("记录主体 = %q，期望 2002", fromID)
	}

	// 第二轮：任务已完成，不会重复入队/重复写库。
	again, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if again.Enqueued != 0 || again.Scanned != 0 || again.Inserted != 0 {
		t.Fatalf("第二轮不应重复处理：%+v", again)
	}
}

// jobRecord 造一条能通过持久化校验的记忆记录（任务原子提交用）。
func jobRecord(id string, ev event.Event) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{
			ID: id, Channel: ev.Channel, Session: ev.Session,
			Summary: "我保证明天带话", Importance: 0.7, Confidence: 0.9,
			EventIDs: []string{ev.ID}, ValidFrom: ev.At,
		},
		Source: "owner_direct", Kind: "promise", Title: "承诺：带话",
	}
}

// memoryRecordRows 统计指定主键的记录条数（直接查库，绕过仓库投影）。
func memoryRecordRows(t *testing.T, ctx context.Context, repo *ExtractionJobsRepo, ids ...string) int {
	t.Helper()
	var count int64
	if err := repo.db.WithContext(ctx).Raw(
		"SELECT count(*) FROM memory_records WHERE id IN ?", ids).Scan(&count).Error; err != nil {
		t.Fatalf("统计记忆记录失败：%v", err)
	}
	return int(count)
}

// TestExtractionJobsCompleteWithRecordsFence C1/N06：旧 worker 失租返回时一条都不能写；
// 新持有者（模拟第二个进程/断电重启）原子提交后才落库。
func TestExtractionJobsCompleteWithRecordsFence(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := inboundEvent("live:room:fence1", false, base, "我保证明天带话")
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(ctx, 10); err != nil {
		t.Fatal(err)
	}

	// 旧 worker 领取后失联（相当于进程断电/被挂起），租约到期。
	old, err := repo.Claim(ctx, "worker-old", time.Second, 10)
	if err != nil || len(old) != 1 {
		t.Fatalf("旧 worker 领取失败：%v %+v", err, old)
	}
	time.Sleep(1100 * time.Millisecond)

	// 新 worker 接手同一条任务。
	fresh, err := repo.Claim(ctx, "worker-new", time.Minute, 10)
	if err != nil || len(fresh) != 1 {
		t.Fatalf("新 worker 接手失败：%v %+v", err, fresh)
	}
	if fresh[0].LeaseOwner != "worker-new" || fresh[0].Attempts != 2 {
		t.Fatalf("接手语义不符：%+v", fresh[0])
	}

	record := jobRecord("mem_fence_1", ev)
	// 旧 worker 恢复后提交：栅栏先失败，记录不得写入。
	outcomes, err := repo.CompleteWithRecords(ctx, old[0], []store.MemoryRecord{record})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("旧租约提交应冲突，实际 err=%v outcomes=%+v", err, outcomes)
	}
	if n := memoryRecordRows(t, ctx, repo, record.ID); n != 0 {
		t.Fatalf("旧 worker 不得写入记录，实际 %d 条", n)
	}
	if status, attempts, _ := jobState(t, ctx, repo, ev.ID); status != "running" || attempts != 2 {
		t.Fatalf("冲突后任务应仍归新 worker：status=%s attempts=%d", status, attempts)
	}

	// 新 worker 原子提交：记录与 done 同时生效。
	outcomes, err = repo.CompleteWithRecords(ctx, fresh[0], []store.MemoryRecord{record})
	if err != nil {
		t.Fatalf("新 worker 提交失败：%v", err)
	}
	if len(outcomes) != 1 || !outcomes[0].Inserted {
		t.Fatalf("提交结果不符：%+v", outcomes)
	}
	if n := memoryRecordRows(t, ctx, repo, record.ID); n != 1 {
		t.Fatalf("新 worker 应写入 1 条，实际 %d 条", n)
	}
	if status, attempts, _ := jobState(t, ctx, repo, ev.ID); status != "done" || attempts != 2 {
		t.Fatalf("完成状态不符：status=%s attempts=%d", status, attempts)
	}
}

// TestExtractionJobsCompleteWithRecordsAtomic 任一记录失败整体回滚，任务保持 running。
func TestExtractionJobsCompleteWithRecordsAtomic(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := inboundEvent("live:room:atomic1", false, base, "我保证明天带话")
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(ctx, 10); err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.Claim(ctx, "worker-a", time.Minute, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("领取失败：%v %+v", err, jobs)
	}

	good := jobRecord("mem_atomic_good", ev)
	bad := jobRecord("mem_atomic_bad", ev)
	bad.Kind = "bogus" // 持久化校验必然失败
	if _, err := repo.CompleteWithRecords(ctx, jobs[0], []store.MemoryRecord{good, bad}); err == nil {
		t.Fatal("含非法记录时应整体失败")
	}
	if n := memoryRecordRows(t, ctx, repo, good.ID, bad.ID); n != 0 {
		t.Fatalf("事务回滚后不应留下记录，实际 %d 条", n)
	}
	if status, _, _ := jobState(t, ctx, repo, ev.ID); status != "running" {
		t.Fatalf("回滚后任务应保持 running，实际 %s", status)
	}

	// 修正输入后同一租约可以正常提交。
	outcomes, err := repo.CompleteWithRecords(ctx, jobs[0], []store.MemoryRecord{good})
	if err != nil || len(outcomes) != 1 || !outcomes[0].Inserted {
		t.Fatalf("修正后提交失败：err=%v outcomes=%+v", err, outcomes)
	}
	if status, _, _ := jobState(t, ctx, repo, ev.ID); status != "done" {
		t.Fatalf("提交后任务应为 done，实际 %s", status)
	}
}

// TestExtractionJobsRenewExtendsLease 续租确实延长租约：原 TTL 过后仍归原 worker。
func TestExtractionJobsRenewExtendsLease(t *testing.T) {
	db, ctx := repositoryDB(t)
	events, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewExtractionJobsRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ev := inboundEvent("live:room:renew1", false, base, "我保证明天带话")
	if _, err := events.Insert(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Enqueue(ctx, 10); err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.Claim(ctx, "worker-a", time.Second, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("领取失败：%v %+v", err, jobs)
	}
	renewed, err := repo.Renew(ctx, jobs[0], time.Minute)
	if err != nil {
		t.Fatalf("续租失败：%v", err)
	}
	if !renewed.LeaseUntil.After(jobs[0].LeaseUntil.Add(time.Second)) {
		t.Fatalf("续租未延长：原 %v 新 %v", jobs[0].LeaseUntil, renewed.LeaseUntil)
	}
	time.Sleep(1100 * time.Millisecond)
	if other, err := repo.Claim(ctx, "worker-b", time.Minute, 10); err != nil || len(other) != 0 {
		t.Fatalf("续租后不应被其他 worker 领取：%v %+v", err, other)
	}
	if err := repo.Complete(ctx, renewed); err != nil {
		t.Fatalf("续租后完成失败：%v", err)
	}
}
