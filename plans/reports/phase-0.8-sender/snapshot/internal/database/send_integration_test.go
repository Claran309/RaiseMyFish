//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

func sendFixture(t *testing.T, db *gorm.DB, ctx context.Context, id, room string) event.Claim {
	t.Helper()
	b := makeBus(t, db)
	ev := testEvent(id, event.ChLive, room, false, time.Now())
	if _, err := b.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}
	c := oneClaim(t, b, ctx)
	tasks, _ := NewTaskRepo(db)
	if _, err := tasks.CreateClaimed(ctx, c, store.Task{ID: "task-" + id, EventID: id, Channel: event.ChLive, Session: room, Brain: "test", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := b.Mark(ctx, c, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
		t.Fatal(err)
	}
	return c
}
func sendReply(room, text string) sender.Reply {
	return sender.Reply{Text: text, Scope: memory.Scope{Channel: event.ChLive, Session: room}}
}
func sendRepo(t *testing.T, db *gorm.DB) *SendRepo {
	t.Helper()
	r, err := NewSendRepo(db, sender.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func sendReclaim(t *testing.T, db *gorm.DB, ctx context.Context, id string) event.Claim {
	t.Helper()
	if err := db.Exec("UPDATE events SET retry_at=clock_timestamp()-interval '1 second' WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	return oneClaim(t, makeBus(t, db), ctx)
}
func sendCounts(t *testing.T, db *gorm.DB, want int64) {
	t.Helper()
	for _, table := range []string{"send_attempts", "send_log"} {
		var n int64
		if err := db.Table(table).Count(&n).Error; err != nil || n != want {
			t.Fatalf("%s=%d want %d: %v", table, n, want, err)
		}
	}
	var n int64
	if err := db.Table("events").Where("dir='out'").Count(&n).Error; err != nil || n != want {
		t.Fatalf("out=%d want %d: %v", n, want, err)
	}
}
func sendState(t *testing.T, db *gorm.DB, id string, status event.Status, stage event.Stage) {
	t.Helper()
	var row queueRow
	if err := db.Table("events").Where("id=?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Event.Status != status || row.Stage != stage {
		t.Fatalf("状态=%s/%s want %s/%s", row.Event.Status, row.Stage, status, stage)
	}
}

func TestSendPrepareArmAuditAndRecovery(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	var _ store.DeliveryRepo = r
	c := sendFixture(t, db, ctx, "live:123:first", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "肥鱼来了"), "456")
	if err != nil || a.State != sender.StateReady || a.Number != 1 {
		t.Fatal(a, err)
	}
	sendCounts(t, db, 1)
	sendState(t, db, c.Event.ID, event.StatusReplying, event.StageDeliver)
	if _, err := r.Prepare(ctx, c, sendReply("123", "换正文"), "456"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("不可变正文被换", err)
	}
	p, err := r.Arm(ctx, c, a.ID)
	if err != nil || p.Nonce == "" || p.AttemptID != a.ID || p.RequestID != a.ID {
		t.Fatal(p, err)
	}
	sendState(t, db, c.Event.ID, event.StatusReplying, event.StageVerify)
	var unknown bool
	if err := db.Raw("SELECT ok IS NULL AND NOT verified FROM send_log WHERE attempt_id=?", a.ID).Scan(&unknown).Error; err != nil || !unknown {
		t.Fatal("arm没有先记未知审计", err)
	}
	expireClaim(t, db, c.Event.ID)
	b := makeBus(t, db)
	if n, err := b.Recover(ctx, 10); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	resumed := oneClaim(t, b, ctx)
	if resumed.Stage != event.StageVerify {
		t.Fatal("崩溃恢复重新投递")
	}
	loaded, err := sendRepo(t, db).Load(ctx, resumed, "456")
	if err != nil || loaded.ID != a.ID || loaded.Text != a.Text {
		t.Fatal(loaded, err)
	}
	if _, err := r.Arm(ctx, resumed, a.ID); err == nil {
		t.Fatal("verify可以再arm")
	}
	v := sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeAccepted, Verified: true, MsgID: "777", Evidence: "history_nonce"}
	if _, err := r.Settle(ctx, c, a.ID, v); !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("旧租约可结算", err)
	}
	result, err := r.Settle(ctx, resumed, a.ID, v)
	if err != nil || !result.Verified || !result.OK {
		t.Fatal(result, err)
	}
	sendCounts(t, db, 1)
	sendState(t, db, c.Event.ID, event.StatusDone, event.StageVerify)
	var out eventRow
	if err := db.Table("events").Where("dir='out'").Take(&out).Error; err != nil {
		t.Fatal(err)
	}
	if out.ID != "live:out:123:"+a.ID || out.FromID != "456" || out.ReplyTo != c.Event.ID || out.Private || out.Status != event.StatusDone {
		t.Fatal("公开审计快照不正确")
	}
}

func TestSendUnknownVerificationExhaustionNeverRearms(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:unknown", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "不确定"), "456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Arm(ctx, c, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeUnknown, ErrorKind: "timeout"}); !sender.Handled(err) {
		t.Fatal(err)
	}
	for i := range 3 {
		c = sendReclaim(t, db, ctx, c.Event.ID)
		a, err = r.Load(ctx, c, "456")
		if err != nil {
			t.Fatal(err)
		}
		if a.Number != 1 {
			t.Fatal("未知创建新尝试")
		}
		if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeNotPosted, ErrorKind: "auth"}); !sender.Handled(err) {
			t.Fatal(err)
		}
		if i < 2 {
			sendState(t, db, c.Event.ID, event.StatusRetry, event.StageVerify)
		}
	}
	sendState(t, db, c.Event.ID, event.StatusDead, event.StageVerify)
	sendCounts(t, db, 1)
	var state string
	if err := db.Raw("SELECT state FROM send_attempts WHERE id=?", a.ID).Scan(&state).Error; err != nil || state != sender.StateManual {
		t.Fatal(state, err)
	}
	var n int64
	db.Table("dlq").Count(&n)
	if n != 1 {
		t.Fatal("未知耗尽未进DLQ")
	}
	var out eventRow
	db.Table("events").Where("dir='out'").Take(&out)
	if out.Status != event.StatusRetry {
		t.Fatal("未知out被伪装成未发出")
	}
}

func TestSendNegativeCreatesBoundedAttempts(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:negative", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "明确未发"), "456")
	if err != nil {
		t.Fatal(err)
	}
	nonces := map[string]bool{}
	for n := 1; n <= 3; n++ {
		if a.Number != n {
			t.Fatal("尝试号错误")
		}
		p, err := r.Arm(ctx, c, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if nonce, err := strconv.ParseInt(p.Nonce, 10, 64); err != nil || nonce <= 0 || nonces[p.Nonce] {
			t.Fatal("nonce不是唯一正63位整数")
		}
		nonces[p.Nonce] = true
		if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeNotPosted, ErrorKind: "preflight"}); !sender.Handled(err) {
			t.Fatal(err)
		}
		if n < 3 {
			c = sendReclaim(t, db, ctx, c.Event.ID)
			a, err = r.Load(ctx, c, "456")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	sendCounts(t, db, 3)
	sendState(t, db, c.Event.ID, event.StatusDead, event.StageVerify)
}

func TestSendActorRateAndDuplicateAcrossRepositories(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c1 := sendFixture(t, db, ctx, "live:123:a", "123")
	a1, err := r.Prepare(ctx, c1, sendReply("123", "相同"), "456")
	if err != nil {
		t.Fatal(err)
	}
	c2 := sendFixture(t, db, ctx, "live:999:b", "999")
	a2, err := r.Prepare(ctx, c2, sendReply("999", "不同"), "456")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, x := range []struct {
		c event.Claim
		a sender.Attempt
	}{{c1, a1}, {c2, a2}} {
		wg.Go(func() { _, err := sendRepo(t, db).Arm(ctx, x.c, x.a.ID); errs <- err })
	}
	wg.Wait()
	close(errs)
	arms, waits := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			arms++
		case sender.Handled(err):
			waits++
		default:
			t.Fatal(err)
		}
	}
	if arms != 1 || waits != 1 {
		t.Fatalf("许可=%d wait=%d", arms, waits)
	}
	// 同账号/房间/正文即使换实例，也不能发第二次。
	var active sender.Attempt
	db.Raw("SELECT a.*,p.text FROM send_attempts a JOIN send_plans p ON p.event_id=a.event_id WHERE a.state='armed'").Scan(&active)
	c3 := sendFixture(t, db, ctx, "live:"+active.RoomID+":c", active.RoomID)
	a3, err := sendRepo(t, db).Prepare(ctx, c3, sendReply(active.RoomID, active.Text), "456")
	if err != nil || a3.State != sender.StateSkipped {
		t.Fatal("重启防重失效", a3.State, err)
	}
	sendState(t, db, c3.Event.ID, event.StatusSkipped, event.StageDeliver)
}

func TestSendScopeActualRowAndAtomicRollback(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:scope", "123")
	forged := c
	forged.Event.Session = "999"
	if _, err := r.Prepare(ctx, forged, sendReply("999", "不可外泄"), "456"); err == nil {
		t.Fatal("信任了Claim伪造房间")
	}
	sendCounts(t, db, 0)
	if err := db.Exec("UPDATE events SET private=true WHERE id=?", c.Event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prepare(ctx, c, sendReply("123", "不可外泄"), "456"); err == nil {
		t.Fatal("私密实际行写公开审计")
	}
	sendCounts(t, db, 0)
	db.Exec("UPDATE events SET private=false WHERE id=?", c.Event.ID)
	if err := db.Exec("ALTER TABLE send_log ADD CONSTRAINT reject_audit CHECK(false) NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prepare(ctx, c, sendReply("123", "原子"), "456"); err == nil {
		t.Fatal("故障未回滚")
	}
	sendCounts(t, db, 0)
	sendState(t, db, c.Event.ID, event.StatusReplying, event.StageGenerate)
}

func TestSendDeferralFailureBudget(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:prep", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "稍后"), "456")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		_, err := r.Defer(ctx, c, a.ID, sender.Deferral{Kind: sender.KindAuth, Code: "auth", CountFailure: true})
		var handled *sender.Error
		if !errors.As(err, &handled) || !handled.Settled {
			t.Fatal("预检没有结清", err)
		}
		if i < 2 {
			if handled.Kind != sender.KindAuth {
				t.Fatal("鉴权分类丢失")
			}
			c = sendReclaim(t, db, ctx, c.Event.ID)
			if _, err := r.Load(ctx, c, "456"); err != nil {
				t.Fatal(err)
			}
		}
	}
	sendState(t, db, c.Event.ID, event.StatusDead, event.StageDeliver)
	sendCounts(t, db, 1)
}

func TestSendTerminalPostClassifications(t *testing.T) {
	for i, code := range []string{"content", "invalid", "unavailable"} {
		t.Run(code, func(t *testing.T) {
			db, ctx := repositoryDB(t)
			r := sendRepo(t, db)
			c := sendFixture(t, db, ctx, fmt.Sprintf("live:123:terminal-%d", i), "123")
			a, err := r.Prepare(ctx, c, sendReply("123", "结束"), "456")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Arm(ctx, c, a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeRejected, ErrorKind: code}); !sender.Handled(err) {
				t.Fatal(err)
			}
			want := event.StatusDead
			if code == "unavailable" {
				want = event.StatusSkipped
			}
			sendState(t, db, c.Event.ID, want, event.StageVerify)
		})
	}
}

func TestSendUnconfirmedReceiptKeepsStableID(t *testing.T) {
	for _, id := range []string{"0", "777"} {
		t.Run(id, func(t *testing.T) {
			db, ctx := repositoryDB(t)
			r := sendRepo(t, db)
			c := sendFixture(t, db, ctx, "live:123:receipt", "123")
			a, err := r.Prepare(ctx, c, sendReply("123", "核验"), "456")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Arm(ctx, c, a.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeAccepted, MsgID: id}); !sender.Handled(err) {
				t.Fatal(err)
			}
			c = sendReclaim(t, db, ctx, c.Event.ID)
			a, err = r.Load(ctx, c, "456")
			if err != nil {
				t.Fatal(err)
			}
			if id == "0" && a.State != sender.StateUnknown {
				t.Fatal("零ID当成可信接受", a.State)
			}
			if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeNotPosted, ErrorKind: "network"}); !sender.Handled(err) {
				t.Fatal(err)
			}
			c = sendReclaim(t, db, ctx, c.Event.ID)
			a, err = r.Load(ctx, c, "456")
			if err != nil {
				t.Fatal(err)
			}
			if a.State != sender.StateUnknown || a.Number != 1 {
				t.Fatal("核验失败没有强制未知")
			}
			if id == "777" && a.MsgID != id {
				t.Fatal("核验网络失败丢失已知稳定ID")
			}
			sendCounts(t, db, 1)
		})
	}
}

func TestSendPrepareCrashRemainsDeliver(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:ready-crash", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "未投递"), "456")
	if err != nil {
		t.Fatal(err)
	}
	expireClaim(t, db, c.Event.ID)
	b := makeBus(t, db)
	if n, err := b.Recover(ctx, 10); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	c = oneClaim(t, b, ctx)
	if c.Stage != event.StageDeliver {
		t.Fatal("ready崩溃丢失deliver")
	}
	loaded, err := r.Load(ctx, c, "456")
	if err != nil || loaded.ID != a.ID {
		t.Fatal(loaded, err)
	}
	if _, err := r.Arm(ctx, c, a.ID); err != nil {
		t.Fatal(err)
	}
	sendCounts(t, db, 1)
}

func TestSendArmAndSettleFaultRollBackAllRows(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:atomic", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "完整事务"), "456")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE events ADD CONSTRAINT reject_verify_input CHECK(dir<>'in' OR stage<>'verify') NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Arm(ctx, c, a.ID); err == nil {
		t.Fatal("Arm故障没有报错")
	}
	sendState(t, db, c.Event.ID, event.StatusReplying, event.StageDeliver)
	var ready bool
	if err := db.Raw("SELECT state='ready' AND nonce IS NULL AND permit_until IS NULL FROM send_attempts WHERE id=?", a.ID).Scan(&ready).Error; err != nil || !ready {
		t.Fatal("Arm留下半成品", err)
	}
	if err := db.Exec("ALTER TABLE events DROP CONSTRAINT reject_verify_input").Error; err != nil {
		t.Fatal(err)
	}
	p, err := r.Arm(ctx, c, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE events ADD CONSTRAINT reject_done CHECK(status<>'done') NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	v := sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeAccepted, Verified: true, MsgID: "777", Evidence: "receipt_id"}
	if _, err := r.Settle(ctx, c, a.ID, v); err == nil {
		t.Fatal("Settle故障没有报错")
	}
	var armed bool
	if err := db.Raw("SELECT state='armed' AND ok IS NULL AND nonce=? FROM send_attempts WHERE id=?", p.Nonce, a.ID).Scan(&armed).Error; err != nil || !armed {
		t.Fatal("Settle留下半成品", err)
	}
	var auditUnknown bool
	if err := db.Raw("SELECT ok IS NULL AND NOT verified FROM send_log WHERE attempt_id=?", a.ID).Scan(&auditUnknown).Error; err != nil || !auditUnknown {
		t.Fatal("失败结算覆盖审计", err)
	}
	sendState(t, db, c.Event.ID, event.StatusReplying, event.StageVerify)
	sendCounts(t, db, 1)
	if err := db.Exec("ALTER TABLE events DROP CONSTRAINT reject_done").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Settle(ctx, c, a.ID, v); err != nil {
		t.Fatal(err)
	}
	sendCounts(t, db, 1)
}

func TestSendArmRechecksDuplicateAfterReady(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	claims := make([]event.Claim, 3)
	attempts := make([]sender.Attempt, 3)
	for i := range 3 {
		claims[i] = sendFixture(t, db, ctx, fmt.Sprintf("live:123:ready-%d", i), "123")
		var err error
		attempts[i], err = r.Prepare(ctx, claims[i], sendReply("123", "ready同文"), "456")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Arm(ctx, claims[0], attempts[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := sendRepo(t, db).Arm(ctx, claims[1], attempts[1].ID); !sender.Handled(err) {
		t.Fatal("Arm没再次检查防重", err)
	}
	sendState(t, db, claims[1].Event.ID, event.StatusSkipped, event.StageDeliver)
	a, err := sendRepo(t, db).Load(ctx, claims[2], "456")
	if err != nil || a.State != sender.StateSkipped {
		t.Fatal("Load没再次检查防重", a.State, err)
	}
	sendCounts(t, db, 3)
}

func TestSendArmCannotCommitAfterActorLockWaitExpiresLease(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:expired-wait", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "过期不可发"), "456")
	if err != nil {
		t.Fatal(err)
	}
	block := db.WithContext(ctx).Begin()
	if block.Error != nil {
		t.Fatal(block.Error)
	}
	defer block.Rollback()
	if err := sendActorLock(block, "456"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE events SET lease_until=clock_timestamp()+interval '150 milliseconds' WHERE id=?", c.Event.ID).Error; err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := r.Arm(ctx, c, a.ID); result <- err }()
	if err := block.Exec("SELECT pg_sleep(0.3)").Error; err != nil {
		t.Fatal(err)
	}
	if err := block.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, event.ErrLeaseLost) {
		t.Fatal("等待锁后已过期仍可arm", err)
	}
	var ready bool
	if err := db.Raw("SELECT state='ready' AND nonce IS NULL FROM send_attempts WHERE id=?", a.ID).Scan(&ready).Error; err != nil || !ready {
		t.Fatal("过期许可未回滚", err)
	}
	sendCounts(t, db, 1)
}

func TestSendRateUsesPermitLatestStartAcrossRestart(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	first := sendFixture(t, db, ctx, "live:123:permit-start", "123")
	a, err := r.Prepare(ctx, first, sendReply("123", "第一条"), "456")
	if err != nil {
		t.Fatal(err)
	}
	permit, err := r.Arm(ctx, first, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := sendFixture(t, db, ctx, "live:999:permit-start", "999")
	b, err := r.Prepare(ctx, second, sendReply("999", "第二条"), "456")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sendRepo(t, db).Arm(ctx, second, b.ID)
	var rateErr *sender.Error
	if !errors.As(err, &rateErr) || !rateErr.Settled || rateErr.Kind != sender.KindRetry {
		t.Fatal("跨实例绕过限速", err)
	}
	want := permit.ExpiresAt.Add(sender.DefaultPolicy().MinInterval)
	if !rateErr.RetryAt.Equal(want) {
		t.Fatalf("限速没有以许可最晚起点计算: %v want %v", rateErr.RetryAt, want)
	}
	var ready bool
	if err := db.Raw("SELECT state='ready' AND prep_failures=0 AND nonce IS NULL FROM send_attempts WHERE id=?", b.ID).Scan(&ready).Error; err != nil || !ready {
		t.Fatal("普通限流扣了预检预算", err)
	}
	sendState(t, db, second.Event.ID, event.StatusRetry, event.StageDeliver)
}

func TestSendDoesNotPersistReplyMetadataOrOriginalInputMutation(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:safe-meta", "123")
	reply := sendReply("123", "只存正文")
	reply.Meta = []byte(`{"cookie":"SENSITIVE_CANARY","raw_response":"SECRET_CANARY"}`)
	a, err := r.Prepare(ctx, c, reply, "456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Arm(ctx, c, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeUnknown, ErrorKind: "network"}); !sender.Handled(err) {
		t.Fatal(err)
	}
	var facts eventRow
	if err := db.Table("events").Where("id=?", c.Event.ID).Take(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts.Text != c.Event.Text || !facts.At.Equal(c.Event.At) || facts.ReplyTo != "" {
		t.Fatal("输入事实被投递流程修改")
	}
	var rows []struct{ Body string }
	if err := db.Raw("SELECT row_to_json(e)::text AS body FROM events e UNION ALL SELECT row_to_json(l)::text AS body FROM send_log l UNION ALL SELECT row_to_json(p)::text AS body FROM send_plans p UNION ALL SELECT row_to_json(a)::text AS body FROM send_attempts a").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Body, "CANARY") {
			t.Fatal("Reply.Meta进入持久化")
		}
	}
}

func TestSendMigrationV2UpgradePreservesAuditAndChecksums(t *testing.T) {
	db := isolatedDB(t)
	ctx := context.Background()
	steps, err := migrationSteps()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())").Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range steps[:2] {
		if err := db.Exec(step.Source).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO schema_migrations(version,checksum) VALUES(?,?)", step.Version, step.Checksum).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("INSERT INTO events(id,channel,dir,session,text,at,status,stage) VALUES('live:123:legacy','live','in','123','旧输入',now(),'done','verify')").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO send_log(event_id,channel,ok,verified,msg_id) VALUES('live:123:legacy','live',true,true,'888')").Error; err != nil {
		t.Fatal(err)
	}
	if err := Ready(ctx, db); err == nil {
		t.Fatal("v2冒充v3就绪")
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
	var receipts []migrationReceipt
	if err := db.Raw("SELECT version,checksum FROM schema_migrations ORDER BY version").Scan(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 3 {
		t.Fatal("迁移账本错误")
	}
	for i, receipt := range receipts {
		if receipt.Checksum != steps[i].Checksum {
			t.Fatal("改写既有校验值")
		}
	}
	var old bool
	if err := db.Raw("SELECT attempt_id IS NULL AND ok AND verified AND msg_id='888' FROM send_log WHERE event_id='live:123:legacy'").Scan(&old).Error; err != nil || !old {
		t.Fatal("既有审计被迁移改写", err)
	}
	c := sendFixture(t, db, ctx, "live:123:after-upgrade", "123")
	if _, err := sendRepo(t, db).Prepare(ctx, c, sendReply("123", "升级后"), "456"); err != nil {
		t.Fatal(err)
	}
}

func TestSendInvalidCanceledAndSafeErrors(t *testing.T) {
	db, ctx := repositoryDB(t)
	if _, err := NewSendRepo(nil, sender.DefaultPolicy()); err == nil {
		t.Fatal("接受nilDB")
	}
	if _, err := NewSendRepo(db, sender.Policy{}); err == nil {
		t.Fatal("接受非法策略")
	}
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:invalid", "123")
	for _, reply := range []sender.Reply{sendReply("999", "越界"), sendReply("123", strings.Repeat("鱼", 41)), sendReply("123", "")} {
		if _, err := r.Prepare(ctx, c, reply, "456"); err == nil {
			t.Fatal("接受非法回复")
		}
	}
	if _, err := r.Prepare(ctx, c, sendReply("123", "正常"), "bad"); err == nil {
		t.Fatal("接受非法账号")
	}
	sendCounts(t, db, 0)
	a, err := r.Prepare(ctx, c, sendReply("123", "正常"), "456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(ctx, c, "789"); !errors.Is(err, store.ErrConflict) {
		t.Fatal("换账号读取计划", err)
	}
	if _, err := r.Defer(ctx, c, a.ID, sender.Deferral{Kind: sender.KindRetry, Code: "SECRET_CANARY"}); err == nil || strings.Contains(err.Error(), "CANARY") {
		t.Fatal("非法错误码没安全拒绝")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Load(cancelled, c, "456"); !errors.Is(err, context.Canceled) {
		t.Fatal("取消没保留", err)
	}
	if _, err := r.Defer(ctx, c, a.ID, sender.Deferral{Kind: sender.KindRetry, Code: "preflight", RetryAt: time.Now().Add(-time.Hour)}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受过去延期", err)
	}
	sendState(t, db, c.Event.ID, event.StatusReplying, event.StageDeliver)
	sendCounts(t, db, 1)
}

func TestSendKnownReceiptCannotBeReplacedByVerification(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:receipt-fence", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "精确核验"), "456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Arm(ctx, c, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeAccepted, MsgID: "777"}); !sender.Handled(err) {
		t.Fatal(err)
	}
	c = sendReclaim(t, db, ctx, c.Event.ID)
	result, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeAccepted, MsgID: "888", Verified: true, Evidence: "history_id"})
	if !sender.Handled(err) || result.Verified || result.OK {
		t.Fatal("不匹配ID被核验成功", result, err)
	}
	c = sendReclaim(t, db, ctx, c.Event.ID)
	loaded, err := r.Load(ctx, c, "456")
	if err != nil || loaded.State != sender.StateUnknown || loaded.MsgID != "777" || loaded.VerifyCount != 1 {
		t.Fatal("核验不匹配没有保留原ID和消耗核验预算", loaded, err)
	}
	result, err = r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeAccepted, MsgID: "777", Verified: true, Evidence: "history_id"})
	if err != nil || !result.Verified {
		t.Fatal(result, err)
	}
	sendCounts(t, db, 1)
}

func TestSendOnlyVerifiedOutEntersMemoryQueries(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	memoryRepo, _ := NewMemoryRepo(db)
	for i, outcome := range []string{"unknown", "dead", "verified"} {
		c := sendFixture(t, db, ctx, fmt.Sprintf("live:123:memory-%d", i), "123")
		a, err := r.Prepare(ctx, c, sendReply("123", outcome), fmt.Sprint(456+i))
		if err != nil {
			t.Fatal(err)
		}
		recent, err := memoryRepo.Recent(ctx, memory.Scope{Channel: event.ChLive, Session: "123"}, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range recent {
			if m.Dir == event.DirOut {
				t.Fatal("ready审计提前成为记忆")
			}
		}
		if _, err := r.Arm(ctx, c, a.ID); err != nil {
			t.Fatal(err)
		}
		v := sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeUnknown, ErrorKind: "timeout"}
		if outcome == "dead" {
			v.Outcome = sender.OutcomeRejected
			v.ErrorKind = "content"
		}
		if outcome == "verified" {
			v.Outcome = sender.OutcomeAccepted
			v.ErrorKind = ""
			v.Verified = true
			v.MsgID = "777"
			v.Evidence = "receipt_id"
		}
		_, err = r.Settle(ctx, c, a.ID, v)
		if outcome == "verified" {
			if err != nil {
				t.Fatal(err)
			}
		} else if !sender.Handled(err) {
			t.Fatal(err)
		}
	}
	recent, err := memoryRepo.Recent(ctx, memory.Scope{Channel: event.ChLive, Session: "123"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	cross, err := memoryRepo.CrossChannel(ctx, memory.Scope{Channel: event.ChQQ, Session: "999"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, rows := range [][]memory.Message{recent, cross} {
		outs := 0
		for _, m := range rows {
			if m.Dir == event.DirOut {
				outs++
				if m.Text != "verified" {
					t.Fatal("失败out混入记忆")
				}
			}
		}
		if len(rows) != 4 || outs != 1 {
			t.Fatalf("召回数量错误:%d out=%d", len(rows), outs)
		}
	}
}

func TestSendMigrationV3FailureRollsBackStageConstraint(t *testing.T) {
	db := isolatedDB(t)
	ctx := context.Background()
	steps, err := migrationSteps()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE schema_migrations(version bigint PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())").Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range steps[:2] {
		if err := db.Exec(step.Source).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO schema_migrations(version,checksum) VALUES(?,?)", step.Version, step.Checksum).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("CREATE TABLE send_plans(collision text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("v3冲突没报错")
	}
	if db.Migrator().HasTable("send_attempts") || db.Migrator().HasColumn("send_log", "attempt_id") {
		t.Fatal("v3失败留下半套发送schema")
	}
	var count int64
	if err := db.Table("schema_migrations").Count(&count).Error; err != nil || count != 2 {
		t.Fatal("v3失败改写迁移账本", err)
	}
	if err := db.Exec("INSERT INTO events(id,channel,dir,session,text,at,stage) VALUES('live:123:rollback','live','in','123','旧约束',now(),'deliver')").Error; err == nil {
		t.Fatal("失败迁移放宽旧stage约束")
	}
	if err := db.Exec("DROP TABLE send_plans").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Ready(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func TestSendRecoveredLeaseCannotImpersonatePostReceipt(t *testing.T) {
	for _, priorPost := range []bool{false, true} {
		t.Run(fmt.Sprint(priorPost), func(t *testing.T) {
			db, ctx := repositoryDB(t)
			r := sendRepo(t, db)
			c := sendFixture(t, db, ctx, "live:123:post-fence", "123")
			a, err := r.Prepare(ctx, c, sendReply("123", "许可归属"), "456")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Arm(ctx, c, a.ID); err != nil {
				t.Fatal(err)
			}
			if priorPost {
				if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeAccepted, MsgID: "777"}); !sender.Handled(err) {
					t.Fatal(err)
				}
				c = sendReclaim(t, db, ctx, c.Event.ID)
			} else {
				expireClaim(t, db, c.Event.ID)
				b := makeBus(t, db)
				if n, err := b.Recover(ctx, 10); err != nil || n != 1 {
					t.Fatal(n, err)
				}
				// 故意复用Owner，只增加Version，证明许可围栏不能只比较Owner。
				events, _ := NewEventRepo(db)
				claims, err := events.Claim(ctx, c.Lease.Owner, time.Minute, 1)
				if err != nil || len(claims) != 1 || claims[0].Lease.Version <= c.Lease.Version {
					t.Fatal("未能取得同Owner的新代次", err)
				}
				c = claims[0]
			}
			if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeAccepted, Verified: true, MsgID: "888", Evidence: "receipt_id"}); !errors.Is(err, store.ErrConflict) {
				t.Fatal("新租约冒充原POST回执", err)
			}
			loaded, err := r.Load(ctx, c, "456")
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Verified || loaded.MsgID == "888" || loaded.VerifyCount != 0 {
				t.Fatal("拒绝冒充时修改了既有尝试", loaded)
			}
			id, evidence := "888", "history_nonce"
			if priorPost {
				id, evidence = "777", "history_id"
			}
			result, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeAccepted, Verified: true, MsgID: id, Evidence: evidence})
			if err != nil || !result.Verified {
				t.Fatal("合法恢复核验被挡住", result, err)
			}
			sendCounts(t, db, 1)
		})
	}
}

func TestSendKnownReceiptCannotUseNonceEvidence(t *testing.T) {
	db, ctx := repositoryDB(t)
	r := sendRepo(t, db)
	c := sendFixture(t, db, ctx, "live:123:nonce-evidence", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "不能用nonce替代精确ID"), "456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Arm(ctx, c, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeAccepted, MsgID: "777"}); !sender.Handled(err) {
		t.Fatal(err)
	}
	c = sendReclaim(t, db, ctx, c.Event.ID)
	result, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeAccepted, MsgID: "777", Verified: true, Evidence: "history_nonce"})
	if !sender.Handled(err) || result.Verified || result.OK {
		t.Fatal("已知ID通过nonce证据绕过精确核验", result, err)
	}
	c = sendReclaim(t, db, ctx, c.Event.ID)
	a, err = r.Load(ctx, c, "456")
	if err != nil || a.MsgID != "777" || a.VerifyCount != 1 || a.State != sender.StateUnknown {
		t.Fatal(a, err)
	}
	result, err = r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeAccepted, MsgID: "777", Verified: true, Evidence: "history_id"})
	if err != nil || !result.Verified {
		t.Fatal(result, err)
	}
	sendCounts(t, db, 1)
}

func TestSendManualUnknownStillBlocksDuplicateWithinWindow(t *testing.T) {
	db, ctx := repositoryDB(t)
	policy := sender.DefaultPolicy()
	policy.MinInterval = time.Millisecond
	policy.PermitTTL = time.Millisecond
	policy.RetryDelay = time.Millisecond
	policy.DuplicateWindow = time.Hour
	policy.MaxVerifications = 1
	r, err := NewSendRepo(db, policy)
	if err != nil {
		t.Fatal(err)
	}
	c := sendFixture(t, db, ctx, "live:123:manual-duplicate-a", "123")
	a, err := r.Prepare(ctx, c, sendReply("123", "可能已经发过"), "456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Arm(ctx, c, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginPost, Outcome: sender.OutcomeUnknown, ErrorKind: "timeout"}); !sender.Handled(err) {
		t.Fatal(err)
	}
	c = sendReclaim(t, db, ctx, c.Event.ID)
	if _, err := r.Settle(ctx, c, a.ID, sender.Verdict{Origin: sender.OriginVerify, Outcome: sender.OutcomeUnknown, ErrorKind: "network"}); !sender.Handled(err) {
		t.Fatal(err)
	}
	sendState(t, db, c.Event.ID, event.StatusDead, event.StageVerify)
	var manual bool
	if err := db.Raw("SELECT state='manual' AND error_kind='unknown_manual' AND ok IS NULL FROM send_attempts WHERE id=?", a.ID).Scan(&manual).Error; err != nil || !manual {
		t.Fatal("未进入manual未知状态", err)
	}
	var out eventRow
	if err := db.Table("events").Where("id=?", "live:out:123:"+a.ID).Take(&out).Error; err != nil || out.Status != event.StatusRetry {
		t.Fatal("manual被当作明确未投递", err)
	}
	// 等到普通间隔结束，防重仍须独立保护潜在已发出的manual尝试。
	if err := db.Exec("SELECT pg_sleep(0.01)").Error; err != nil {
		t.Fatal(err)
	}
	var intervalEnded bool
	if err := db.Raw("SELECT permit_until+interval '1 millisecond'<clock_timestamp() FROM send_attempts WHERE id=?", a.ID).Scan(&intervalEnded).Error; err != nil || !intervalEnded {
		t.Fatal("普通间隔尚未结束", err)
	}
	restarted, err := NewSendRepo(db, policy)
	if err != nil {
		t.Fatal(err)
	}
	second := sendFixture(t, db, ctx, "live:123:manual-duplicate-b", "123")
	duplicate, err := restarted.Prepare(ctx, second, sendReply("123", "可能已经发过"), "456")
	if err != nil || duplicate.State != sender.StateSkipped {
		t.Fatal("manual未知结果绕过防重窗口", duplicate.State, err)
	}
	sendState(t, db, second.Event.ID, event.StatusSkipped, event.StageDeliver)
	sendCounts(t, db, 2)
}
