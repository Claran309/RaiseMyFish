//go:build integration

package database

// T19：评论/私信四阶段崩溃恢复。
//
// 四个窗口：
//  1. 建计划后（ready，未 POST）→ 恢复按计划原文重投；
//  2. 投递中（armed，请求在途）→ 恢复只核验，不重投；
//  3. 接受后未记录（armed）→ 与窗口 2 同态：平台可能已收下，恢复按未知停止；
//  4. 记录 accepted、尚未标 sent → 恢复核验为已接受，不重投并结清。
//
// 评论/私信没有可回读的幂等键，所以 2/3 只能保守按 uncertain 结束。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
	sendercomment "RaiseMyFish/internal/sender/comment"
	senderdm "RaiseMyFish/internal/sender/dm"
	"RaiseMyFish/internal/sender/durable"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const planAccount = "10001"

// planPost 是平台发送器的替身：只数真实 POST 与正文。
type planPost struct {
	sends  int
	texts  []string
	result sender.Result
	err    error
}

func (p *planPost) Name() string { return "plan-post" }

func (p *planPost) Send(_ context.Context, _ event.Claim, r sender.Reply) (sender.Result, error) {
	p.sends++
	p.texts = append(p.texts, r.Text)
	return p.result, p.err
}

func (p *planPost) Resume(context.Context, event.Claim) (sender.Result, error) {
	return sender.Result{}, errors.New("不应调用底层 Resume")
}

func planKey(ch event.Channel, session string) store.PlanKey {
	return store.PlanKey{Channel: ch, AccountID: planAccount, Destination: session}
}

func planReply(ch event.Channel, session string, private bool, text string) sender.Reply {
	return sender.Reply{Text: text, Scope: memory.Scope{Channel: ch, Session: session, Private: private}}
}

// planFixture 造一条已进入 Generate 的事件（status=replying，stage=generate）。
func planFixture(t *testing.T, db *gorm.DB, ctx context.Context, id string, ch event.Channel, session string, private bool) event.Claim {
	t.Helper()
	b := makeBus(t, db)
	ev := testEvent(id, ch, session, private, time.Now().UTC().Truncate(time.Microsecond))
	if _, err := b.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}
	c := oneClaim(t, b, ctx)
	tasks, err := NewTaskRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.CreateClaimed(ctx, c, store.Task{ID: "task-" + id, EventID: id, Channel: ch, Session: session, Brain: "test", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := b.Mark(ctx, c, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
		t.Fatal(err)
	}
	return c
}

func planSetup(t *testing.T, db *gorm.DB, ch event.Channel) (*PlannedRepo, *durable.Sender, *planPost) {
	t.Helper()
	repo, err := NewPlannedRepo(db)
	if err != nil {
		t.Fatal(err)
	}
	post := &planPost{result: sender.Result{OK: true, MsgID: "receipt-1", Verified: true}}
	var validate func(event.Claim, sender.Reply) (sender.Reply, error)
	switch ch {
	case event.ChComment:
		validate = sendercomment.Normalize
	case event.ChDM:
		validate = senderdm.Normalize
	default:
		t.Fatalf("不支持的通道 %s", ch)
	}
	snd, err := durable.New(repo, post, durable.Options{
		Channel: ch, AccountID: planAccount, Validate: validate, MaxAttempts: 3,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return repo, snd, post
}

// planRecover 模拟进程崩溃：租约到期后恢复并重新领取。
func planRecover(t *testing.T, db *gorm.DB, ctx context.Context, id string) event.Claim {
	t.Helper()
	expireClaim(t, db, id)
	b := makeBus(t, db)
	if n, err := b.Recover(ctx, 10); err != nil || n != 1 {
		t.Fatalf("恢复过期事件=%d %v", n, err)
	}
	c := oneClaim(t, b, ctx)
	if err := b.Mark(ctx, c, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
		t.Fatal(err)
	}
	return c
}

func planCurrent(t *testing.T, db *gorm.DB, eventID string) (deliveryPlanRow, deliveryAttemptRow) {
	t.Helper()
	var p deliveryPlanRow
	if err := db.Table("delivery_plans").Where("event_id=?", eventID).Take(&p).Error; err != nil {
		t.Fatal(err)
	}
	var a deliveryAttemptRow
	if err := db.Table("delivery_attempts").Where("id=?", p.CurrentAttemptID).Take(&a).Error; err != nil {
		t.Fatal(err)
	}
	return p, a
}

func planAttemptCount(t *testing.T, db *gorm.DB, eventID string) int64 {
	t.Helper()
	var n int64
	if err := db.Table("delivery_attempts").Where("event_id=?", eventID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func planKind(t *testing.T, err error) sender.Kind {
	t.Helper()
	var typed *sender.Error
	if !errors.As(err, &typed) {
		t.Fatalf("错误类型不是 *sender.Error：%v", err)
	}
	return typed.Kind
}

// TestDeliveryPlanCrashAfterPlanReusesOriginalText 对应窗口 1：计划已落、未 POST。
func TestDeliveryPlanCrashAfterPlanReusesOriginalText(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:555:666", "555"
	c := planFixture(t, db, ctx, id, ch, session, false)
	repo, snd, post := planSetup(t, db, ch)
	key := planKey(ch, session)

	a, err := repo.PreparePlan(ctx, c, key, "计划原文")
	if err != nil || a.State != sender.StateReady || a.Number != 1 || a.Text != "计划原文" {
		t.Fatalf("准备计划=%+v %v", a, err)
	}
	// 计划正文不可变：模型重建出的新文字不会覆盖原快照。
	again, err := repo.PreparePlan(ctx, c, key, "模型重新生成")
	if err != nil || again.ID != a.ID || again.Text != "计划原文" {
		t.Fatalf("计划被改写：%+v %v", again, err)
	}
	if n := planAttemptCount(t, db, id); n != 1 {
		t.Fatalf("重复准备产生了新尝试：%d", n)
	}

	resumed := planRecover(t, db, ctx, id)
	if resumed.Stage != event.StageGenerate {
		t.Fatalf("窗口1恢复阶段=%s want generate", resumed.Stage)
	}
	res, err := snd.Send(ctx, resumed, planReply(ch, session, false, "模型重新生成"))
	if err != nil || !res.OK || !res.Verified {
		t.Fatalf("恢复投递=%+v %v", res, err)
	}
	if post.sends != 1 || post.texts[0] != "计划原文" {
		t.Fatalf("应按计划原文重投：sends=%d texts=%v", post.sends, post.texts)
	}

	b := makeBus(t, db)
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusReplying}, event.StatusSent); err != nil {
		t.Fatal(err)
	}
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusSent}, event.StatusDone); err != nil {
		t.Fatal(err)
	}
	sendState(t, db, id, event.StatusDone, event.StageVerify)
	p, cur := planCurrent(t, db, id)
	if p.AttemptCount != 1 || cur.State != sender.StateAccepted || cur.Number != 1 {
		t.Fatalf("计划/尝试=%+v/%+v", p, cur)
	}
}

// TestDeliveryPlanCrashAfterArmNeverReposts 对应窗口 2/3：请求可能已到平台。
func TestDeliveryPlanCrashAfterArmNeverReposts(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChDM, "dm:20002:900", "20002"
	c := planFixture(t, db, ctx, id, ch, session, true)
	repo, snd, post := planSetup(t, db, ch)
	key := planKey(ch, session)

	a, err := repo.PreparePlan(ctx, c, key, "私信原文")
	if err != nil {
		t.Fatal(err)
	}
	armed, err := repo.ArmPlan(ctx, c, key, a.ID)
	if err != nil || armed.State != sender.StateArmed {
		t.Fatalf("许可=%+v %v", armed, err)
	}

	resumed := planRecover(t, db, ctx, id)
	if resumed.Stage != event.StageGenerate {
		t.Fatalf("armed 恢复阶段=%s", resumed.Stage)
	}
	_, err = snd.Send(ctx, resumed, planReply(ch, session, true, "模型重新生成"))
	if planKind(t, err) != sender.KindUncertain {
		t.Fatalf("kind=%s want uncertain", planKind(t, err))
	}
	if post.sends != 0 {
		t.Fatal("armed 计划被重新投递")
	}
	if n := planAttemptCount(t, db, id); n != 1 {
		t.Fatalf("armed 恢复创建了新尝试：%d", n)
	}
	b := makeBus(t, db)
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusReplying}, event.StatusDead); err != nil {
		t.Fatal(err)
	}
	sendState(t, db, id, event.StatusDead, event.StageGenerate)
}

// TestDeliveryPlanUnknownResultStopsAutomatically 未知结果同样停止自动处理。
func TestDeliveryPlanUnknownResultStopsAutomatically(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:777:888", "777"
	c := planFixture(t, db, ctx, id, ch, session, false)
	repo, snd, post := planSetup(t, db, ch)
	key := planKey(ch, session)

	a, err := repo.PreparePlan(ctx, c, key, "原文")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ArmPlan(ctx, c, key, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordPlan(ctx, c, key, a.ID, store.PlanOutcome{State: sender.StateUnknown, ErrorKind: "unknown_manual"}); err != nil {
		t.Fatal(err)
	}

	resumed := planRecover(t, db, ctx, id)
	_, err = snd.Send(ctx, resumed, planReply(ch, session, false, "模型重新生成"))
	if planKind(t, err) != sender.KindUncertain {
		t.Fatalf("kind=%s want uncertain", planKind(t, err))
	}
	if post.sends != 0 {
		t.Fatal("unknown 结果被重新投递")
	}
}

// TestDeliveryPlanCrashAfterAcceptBeforeSent 对应窗口 4：记录已 accepted，尚未标 sent。
func TestDeliveryPlanCrashAfterAcceptBeforeSent(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:666:999", "666"
	c := planFixture(t, db, ctx, id, ch, session, false)
	repo, snd, post := planSetup(t, db, ch)
	key := planKey(ch, session)

	res, err := snd.Send(ctx, c, planReply(ch, session, false, "原文"))
	if err != nil || !res.OK {
		t.Fatalf("首次投递=%+v %v", res, err)
	}
	if post.sends != 1 {
		t.Fatalf("首次投递次数=%d", post.sends)
	}
	// 模拟「接受后、标 sent 前」崩溃：事件仍停在 replying/generate。
	sendState(t, db, id, event.StatusReplying, event.StageGenerate)
	_, accepted := planCurrent(t, db, id)
	if accepted.State != sender.StateAccepted {
		t.Fatalf("attempt=%s want accepted", accepted.State)
	}

	resumed := planRecover(t, db, ctx, id)
	res, err = snd.Send(ctx, resumed, planReply(ch, session, false, "模型重新生成"))
	if err != nil || !res.OK || !res.Verified {
		t.Fatalf("恢复核验=%+v %v", res, err)
	}
	if post.sends != 1 {
		t.Fatalf("已接受结果被重复投递：%d", post.sends)
	}
	// 重复结算只保留一份尝试记录。
	if _, err := repo.RecordPlan(ctx, resumed, key, accepted.ID, store.PlanOutcome{
		State: sender.StateAccepted, OK: true, Verified: true, MsgID: accepted.MsgID,
	}); err != nil {
		t.Fatal(err)
	}
	if n := planAttemptCount(t, db, id); n != 1 {
		t.Fatalf("重复结算产生新尝试：%d", n)
	}

	b := makeBus(t, db)
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusReplying}, event.StatusSent); err != nil {
		t.Fatal(err)
	}
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusSent}, event.StatusDone); err != nil {
		t.Fatal(err)
	}
	sendState(t, db, id, event.StatusDone, event.StageVerify)
}

// TestDeliveryPlanTrustedNotPostedIsBounded 可信未投递按既定次数重试，用尽后停止。
func TestDeliveryPlanTrustedNotPostedIsBounded(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:888:111", "888"
	c := planFixture(t, db, ctx, id, ch, session, false)
	_, snd, post := planSetup(t, db, ch)
	post.result = sender.Result{}
	post.err = &sender.Error{Kind: sender.KindRetry, Cause: errors.New("平台限流")}

	b := makeBus(t, db)
	for i := 1; i <= 3; i++ {
		_, err := snd.Send(ctx, c, planReply(ch, session, false, "原文"))
		if planKind(t, err) != sender.KindRetry {
			t.Fatalf("第%d次 kind=%s want retry", i, planKind(t, err))
		}
		p, a := planCurrent(t, db, id)
		if p.AttemptCount != i || a.Number != i || a.State != sender.StateNotPosted {
			t.Fatalf("第%d次尝试=%+v/%+v", i, p, a)
		}
		if err := b.Defer(ctx, c, time.Now().UTC().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("UPDATE events SET retry_at=clock_timestamp()-interval '1 second' WHERE id=?", id).Error; err != nil {
			t.Fatal(err)
		}
		c = oneClaim(t, b, ctx)
		if err := b.Mark(ctx, c, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
			t.Fatal(err)
		}
	}
	if post.sends != 3 {
		t.Fatalf("重试实际 POST=%d want 3", post.sends)
	}

	_, err := snd.Send(ctx, c, planReply(ch, session, false, "原文"))
	if planKind(t, err) != sender.KindDead {
		t.Fatalf("尝试用尽 kind=%s want dead", planKind(t, err))
	}
	if post.sends != 3 {
		t.Fatalf("尝试用尽后仍 POST：%d", post.sends)
	}
	if n := planAttemptCount(t, db, id); n != 3 {
		t.Fatalf("尝试总数=%d want 3", n)
	}
	if p, a := planCurrent(t, db, id); p.AttemptCount != 3 || a.State != sender.StateNotPosted {
		t.Fatalf("最终计划/尝试=%+v/%+v", p, a)
	}
}
