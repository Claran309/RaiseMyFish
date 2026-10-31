//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	busimpl "RaiseMyFish/internal/event/bus"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

func makeBus(t *testing.T, db *gorm.DB) *busimpl.Bus {
	t.Helper()
	repo, err := NewEventRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	b, err := busimpl.New(repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func publishQueueEvent(t *testing.T, b event.Bus, ctx context.Context, id string) event.Event {
	t.Helper()
	ev := testEvent(id, event.ChLive, "room", false, time.Now().Add(-24*time.Hour))
	if ok, err := b.Publish(ctx, ev); err != nil || !ok {
		t.Fatal(err)
	}
	return ev
}
func oneClaim(t *testing.T, b event.Bus, ctx context.Context) event.Claim {
	t.Helper()
	rows, err := b.ClaimPending(ctx, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("需要1个Claim: %d %v", len(rows), err)
	}
	return rows[0]
}
func expireClaim(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	if err := db.Exec("UPDATE events SET lease_until=clock_timestamp()-interval '1 second' WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
}

func TestQueueClaimDeferFencingAndFilter(t *testing.T) {
	db, ctx := repositoryDB(t)
	a := makeBus(t, db)
	b := makeBus(t, db)
	repo, _ := NewEventRepo(db)
	ev := publishQueueEvent(t, a, ctx, "live:room:lease")
	if inserted, err := a.Publish(ctx, ev); err != nil || inserted {
		t.Fatal("重复Publish错误", err)
	}
	first := oneClaim(t, a, ctx)
	if first.Event.Status != event.StatusQueued || first.Stage != event.StageSchedule || first.Lease.Version < 1 || first.StateAt.Before(ev.At) {
		t.Fatal("领取元数据错误")
	}
	if rows, err := b.ClaimPending(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatal("抢走活跃租约", err)
	}
	if n, err := b.Recover(ctx, 10); err != nil || n != 0 {
		t.Fatal("按旧平台At误恢复", err)
	}
	renewed, err := a.Renew(ctx, first)
	if err != nil || renewed.Lease != first.Lease || !renewed.StateAt.Equal(first.StateAt) {
		t.Fatal("续租改变状态时间/代次", err)
	}
	if err := repo.Transition(ctx, ev.ID, []event.Status{event.StatusQueued}, event.StatusReplying); !errors.Is(err, store.ErrConflict) {
		t.Fatal("旧无租约入口绕过持有权")
	}
	until := time.Now().Add(time.Hour)
	if err := a.Defer(ctx, first, until); err != nil {
		t.Fatal(err)
	}
	if err := repo.Transition(ctx, ev.ID, []event.Status{event.StatusRetry}, event.StatusQueued); !errors.Is(err, store.ErrConflict) {
		t.Fatal("释放后的旧入口绕过延期", err)
	}
	if rows, err := b.ClaimPending(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatal("未来延期被提前取", err)
	}
	if err := a.Mark(ctx, first, []event.Status{event.StatusQueued}, event.StatusSkipped); !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("释放后的旧租约可写")
	}
	if err := db.Exec("UPDATE events SET retry_at=clock_timestamp()-interval '1 second' WHERE id=?", ev.ID).Error; err != nil {
		t.Fatal(err)
	}
	second := oneClaim(t, b, ctx)
	if second.Stage != event.StageSchedule || second.Lease.Version <= first.Lease.Version {
		t.Fatal("延期变阶段或未换代")
	}
	expireClaim(t, db, ev.ID)
	third := oneClaim(t, a, ctx)
	if third.Lease.Version <= second.Lease.Version {
		t.Fatal("过期queued未换代")
	}
	if _, err := b.Renew(ctx, second); !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("旧租约续租成功")
	}
	if err := a.Mark(ctx, third, []event.Status{event.StatusQueued}, event.StatusSkipped); err != nil {
		t.Fatal(err)
	}
	if rows, err := b.ClaimPending(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatal("终态被领取", err)
	}
}

func TestQueueConcurrentClaimSkipLockedAndOrder(t *testing.T) {
	db, ctx := repositoryDB(t)
	a := makeBus(t, db)
	b := makeBus(t, db)
	at := time.Now()
	for i := range 12 {
		ev := testEvent(fmt.Sprintf("live:room:%02d", i), event.ChLive, "room", false, at)
		if _, err := a.Publish(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT id FROM events WHERE id='live:room:00' FOR UPDATE").Error; err != nil {
			return err
		}
		rows, err := b.ClaimPending(ctx, 1)
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Event.ID != "live:room:01" {
			t.Fatal("SKIP LOCKED或排序错误")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claimed := make(chan []event.Claim, 2)
	errs := make(chan error, 2)
	for _, worker := range []event.Bus{a, b} {
		wg.Go(func() { rows, err := worker.ClaimPending(ctx, 100); claimed <- rows; errs <- err })
	}
	wg.Wait()
	close(claimed)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for batch := range claimed {
		for _, row := range batch {
			if seen[row.Event.ID] {
				t.Fatal("重复领取")
			}
			seen[row.Event.ID] = true
		}
	}
	if len(seen) != 11 {
		t.Fatalf("漏领：%d", len(seen))
	}
	out := testEvent("live:room:out", event.ChLive, "room", false, at)
	out.Dir = event.DirOut
	out.Status = event.StatusDone
	if _, err := a.Publish(ctx, out); err != nil {
		t.Fatal(err)
	}
	if rows, err := a.ClaimPending(ctx, 100); err != nil || len(rows) != 0 {
		t.Fatal("out或活跃事件被领取", err)
	}
}

func TestQueueTaskAtomicAndRecoveryStages(t *testing.T) {
	db, ctx := repositoryDB(t)
	a := makeBus(t, db)
	b := makeBus(t, db)
	tasks, _ := NewTaskRepo(db)
	var _ store.ClaimedTaskRepo = tasks
	ev := publishQueueEvent(t, a, ctx, "live:room:recover")
	claim := oneClaim(t, a, ctx)
	task := store.Task{ID: "task-recover", EventID: ev.ID, Channel: ev.Channel, Session: ev.Session, Brain: "openai", Priority: 2, CreatedAt: time.Now()}
	if _, err := tasks.CreateForEvent(ctx, task); !errors.Is(err, store.ErrConflict) {
		t.Fatal("旧任务入口绕过租约")
	}
	bad := task
	bad.Session = "wrong"
	if _, err := tasks.CreateClaimed(ctx, claim, bad); !errors.Is(err, store.ErrConflict) {
		t.Fatal("接受错会话")
	}
	var stage string
	if err := db.Raw("SELECT stage FROM events WHERE id=?", ev.ID).Scan(&stage).Error; err != nil || stage != "schedule" {
		t.Fatal("失败事务改变阶段", err)
	}
	if ok, err := tasks.CreateClaimed(ctx, claim, task); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := tasks.CreateClaimed(ctx, claim, task); err != nil || ok {
		t.Fatal("重复任务错误", err)
	}
	if err := a.Mark(ctx, claim, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
		t.Fatal(err)
	}
	if n, err := b.Recover(ctx, 10); err != nil || n != 0 {
		t.Fatal("活跃replying被恢复", err)
	}
	expireClaim(t, db, ev.ID)
	if n, err := b.Recover(ctx, 10); err != nil || n != 1 {
		t.Fatal("过期replying未恢复", err)
	}
	if err := a.Mark(ctx, claim, []event.Status{event.StatusReplying}, event.StatusSent); !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("旧worker覆盖恢复")
	}
	if _, err := tasks.CreateClaimed(ctx, claim, task); !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("旧worker写任务")
	}
	resumed := oneClaim(t, b, ctx)
	if resumed.Stage != event.StageGenerate || resumed.Event.Status != event.StatusQueued {
		t.Fatal("生成恢复回到了首次调度")
	}
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusQueued}, event.StatusSkipped); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("恢复生成被重新过滤")
	}
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
		t.Fatal(err)
	}
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusReplying}, event.StatusSent); err != nil {
		t.Fatal(err)
	}
	expireClaim(t, db, ev.ID)
	if n, err := a.Recover(ctx, 10); err != nil || n != 1 {
		t.Fatal("sent未补偿恢复", err)
	}
	legacy, _ := NewEventRepo(db)
	if err := legacy.Transition(ctx, ev.ID, []event.Status{event.StatusRetry}, event.StatusQueued); !errors.Is(err, store.ErrConflict) {
		t.Fatal("旧入口绕过核验恢复", err)
	}
	verify := oneClaim(t, a, ctx)
	if verify.Stage != event.StageVerify {
		t.Fatal("sent回到了生成阶段")
	}
	if err := a.Mark(ctx, verify, []event.Status{event.StatusQueued}, event.StatusReplying); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("verify被重新生成")
	}
	if err := a.Mark(ctx, verify, []event.Status{event.StatusQueued}, event.StatusDone); err != nil {
		t.Fatal("核验完成失败", err)
	}
}

func TestMigrationV1UpgradeEventLeases(t *testing.T) {
	db := isolatedDB(t)
	ctx := context.Background()
	source, hash, err := migrationSource()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO schema_migrations(version,checksum) VALUES(1,?)", hash).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO events(id,channel,dir,session,text,at,status) VALUES('live:room:old','live','in','room','旧事件',now(),'replying')").Error; err != nil {
		t.Fatal(err)
	}
	if err := Ready(ctx, db); err == nil {
		t.Fatal("v1不应冒充新schema就绪")
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Ready(ctx, db); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := db.Raw("SELECT checksum FROM schema_migrations WHERE version=1").Scan(&original).Error; err != nil || original != hash {
		t.Fatal("改写v1校验值", err)
	}
	b := makeBus(t, db)
	if n, err := b.Recover(ctx, 10); n != 1 || err != nil {
		t.Fatal(err)
	}
	claim := oneClaim(t, b, ctx)
	if claim.Stage != event.StageVerify {
		t.Fatal("旧未知replying被当作可重发")
	}
}

func TestQueueTaskFailureRollsBackAndFences(t *testing.T) {
	db, ctx := repositoryDB(t)
	b := makeBus(t, db)
	tasks, _ := NewTaskRepo(db)
	first := publishQueueEvent(t, b, ctx, "live:room:task-a")
	claimA := oneClaim(t, b, ctx)
	task := store.Task{ID: "collision", EventID: first.ID, Channel: first.Channel, Session: first.Session, Brain: "openai", Priority: 2, CreatedAt: time.Now()}
	if _, err := tasks.CreateClaimed(ctx, claimA, task); err != nil {
		t.Fatal(err)
	}
	second := publishQueueEvent(t, b, ctx, "live:room:task-b")
	claimB := oneClaim(t, b, ctx)
	task.EventID = second.ID
	if _, err := tasks.CreateClaimed(ctx, claimB, task); !errors.Is(err, store.ErrConflict) {
		t.Fatal("任务ID冲突未拒绝", err)
	}
	var stage string
	if err := db.Raw("SELECT stage FROM events WHERE id=?", second.ID).Scan(&stage).Error; err != nil || stage != "schedule" {
		t.Fatal("任务冲突留下半成品阶段", err)
	}
	if _, err := tasks.GetByEvent(ctx, second.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("任务冲突留下半成品任务", err)
	}
	task.ID = "second-task"
	var wg sync.WaitGroup
	results := make(chan bool, 10)
	errs := make(chan error, 10)
	for range 10 {
		wg.Go(func() { ok, err := tasks.CreateClaimed(ctx, claimB, task); results <- ok; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("同租约重复创建了Task")
	}
	// Claim中的快照字段不是授权：即使篡改阶段，也无法在schedule上直接完成核验。
	third := publishQueueEvent(t, b, ctx, "live:room:forged")
	forged := oneClaim(t, b, ctx)
	forged.Stage = event.StageVerify
	forged.Event.Status = event.StatusSent
	if err := b.Mark(ctx, forged, []event.Status{event.StatusQueued}, event.StatusDone); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("信任了伪造阶段")
	}
	if err := b.Defer(ctx, forged, time.Now().Add(-time.Hour)); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受过去期限")
	}
	if err := b.Mark(ctx, forged, []event.Status{event.StatusPending}, event.StatusSkipped); !errors.Is(err, store.ErrConflict) {
		t.Fatal("忽略调用方from条件")
	}
	expireClaim(t, db, third.ID)
	if _, err := b.Renew(ctx, forged); !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("过期但未换租的worker续租成功")
	}
	if err := b.Mark(ctx, forged, []event.Status{event.StatusQueued}, event.StatusSkipped); !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("过期worker仍可写")
	}
}

func TestQueueRecoverBoundedKeepsStateClock(t *testing.T) {
	db, ctx := repositoryDB(t)
	b := makeBus(t, db)
	for _, id := range []string{"live:room:clock-a", "live:room:clock-b"} {
		publishQueueEvent(t, b, ctx, id)
	}
	claims, err := b.ClaimPending(ctx, 2)
	if err != nil || len(claims) != 2 {
		t.Fatal(err)
	}
	for _, claim := range claims {
		expireClaim(t, db, claim.Event.ID)
	}
	if n, err := b.Recover(ctx, 1); err != nil || n != 1 {
		t.Fatal("恢复无界", err)
	}
	if n, err := b.Recover(ctx, 1); err != nil || n != 1 {
		t.Fatal("恢复遗漏", err)
	}
	if n, err := b.Recover(ctx, 1); err != nil || n != 0 {
		t.Fatal("反复恢复未持有行", err)
	}
	again, err := b.ClaimPending(ctx, 2)
	if err != nil || len(again) != 2 {
		t.Fatal(err)
	}
	for i, c := range again {
		if !c.StateAt.Equal(claims[i].StateAt) || !c.Event.At.Equal(claims[i].Event.At) {
			t.Fatal("queued恢复伪造状态起始/平台时间")
		}
	}
}

func TestMigrationV2RollbackAndChecksum(t *testing.T) {
	db := isolatedDB(t)
	ctx := context.Background()
	source, hash, err := migrationSource()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO schema_migrations(version,checksum) VALUES(1,?)", hash).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE events ADD COLUMN stage text").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("迁移冲突未失败")
	}
	if db.Migrator().HasColumn("events", "lease_until") {
		t.Fatal("失败迁移留下半套字段")
	}
	var count int64
	if err := db.Table("schema_migrations").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("失败迁移写入账本", err)
	}
	if err := db.Exec("ALTER TABLE events DROP COLUMN stage").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE schema_migrations SET checksum='tamper' WHERE version=2").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("v2校验被跳过")
	}
	if err := Ready(ctx, db); err == nil {
		t.Fatal("损坏v2仍就绪")
	}
}

func TestQueueInvalidCanceledAndClosedErrors(t *testing.T) {
	db, ctx := repositoryDB(t)
	b := makeBus(t, db)
	repo, _ := NewEventRepo(db)
	publishQueueEvent(t, b, ctx, "live:room:error-canary")
	claim := oneClaim(t, b, ctx)
	for _, n := range []int{0, 101} {
		if _, err := repo.Claim(ctx, "owner", time.Minute, n); !errors.Is(err, store.ErrInvalid) {
			t.Fatal("仓储未检查上限")
		}
		if _, err := repo.RecoverExpired(ctx, n); !errors.Is(err, store.ErrInvalid) {
			t.Fatal("恢复未检查上限")
		}
	}
	if err := repo.MarkClaim(ctx, event.Claim{}, nil, event.StatusDone, time.Minute); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受空租约")
	}
	if err := repo.DeferClaim(ctx, claim, time.Time{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受零期限")
	}
	if _, err := repo.RenewClaim(ctx, claim, time.Millisecond); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受错误租期")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, test := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"claim", func(c context.Context) error { _, err := b.ClaimPending(c, 10); return err }},
		{"mark", func(c context.Context) error {
			return b.Mark(c, claim, []event.Status{event.StatusQueued}, event.StatusSkipped)
		}},
		{"defer", func(c context.Context) error { return b.Defer(c, claim, time.Now().Add(time.Hour)) }},
		{"renew", func(c context.Context) error { _, err := b.Renew(c, claim); return err }},
		{"recover", func(c context.Context) error { _, err := b.Recover(c, 10); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(canceled); !errors.Is(err, context.Canceled) {
				t.Fatal("取消未保留", err)
			}
		})
	}
	if err := Close(db); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ClaimPending(ctx, 10); err == nil || errors.Unwrap(err) == nil {
		t.Fatal("关闭连接错误未保留")
	}
	if err := b.Mark(ctx, claim, []event.Status{event.StatusQueued}, event.StatusSkipped); err == nil || errors.Unwrap(err) == nil {
		t.Fatal("关闭连接错误未保留")
	}
}

func TestQueueRenewCannotReviveLeaseAfterLockWait(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo, _ := NewEventRepo(db)
	b, err := busimpl.New(repo, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	publishQueueEvent(t, b, ctx, "live:room:lock-expiry")
	claim := oneClaim(t, b, ctx)
	blocker := db.WithContext(ctx).Begin()
	if blocker.Error != nil {
		t.Fatal(blocker.Error)
	}
	committed := false
	t.Cleanup(func() {
		if !committed {
			if err := blocker.Rollback().Error; err != nil {
				t.Error(err)
			}
		}
	})
	if err := blocker.Exec("SELECT id FROM events WHERE id=? FOR UPDATE", claim.Event.ID).Error; err != nil {
		t.Fatal(err)
	}
	pidChannel := make(chan int, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			pidChannel <- pid
			r, _ := NewEventRepo(conn)
			_, err := r.RenewClaim(ctx, claim, 2*time.Second)
			return err
		})
	}()
	var pid int
	select {
	case pid = <-pidChannel:
	case err := <-finished:
		t.Fatal("续租连接失败", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// 先确认续租已在等待行锁，避免凭固定sleep猜测并发顺序。
	for {
		var waiting bool
		if err := db.WithContext(ctx).Raw("SELECT COALESCE((SELECT wait_event_type='Lock' FROM pg_stat_activity WHERE pid=?),false)", pid).Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-finished:
			t.Fatal("续租未等待行锁", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// 持锁跨过真实PG租约期限，但不更新元组，复现UPDATE谓词提前求值窗口。
	if err := blocker.Exec("SELECT pg_sleep(GREATEST(EXTRACT(epoch FROM (?::timestamptz-clock_timestamp())),0)+0.05)", claim.LeaseUntil).Error; err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit().Error; err != nil {
		t.Fatal(err)
	}
	committed = true
	select {
	case err := <-finished:
		if !errors.Is(err, event.ErrLeaseLost) {
			t.Fatal("锁等待跨过期后复活了旧租约", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
