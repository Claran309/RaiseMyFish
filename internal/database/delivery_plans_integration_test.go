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
	"encoding/json"
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
//
// 计划一旦落库，阶段已切到 deliver/verify；恢复不会再进 generate，
// 所以这里不伪造 queued→replying，交给 Deliver/Verify 的 Resume 路径结清。
func planRecover(t *testing.T, db *gorm.DB, ctx context.Context, id string) event.Claim {
	t.Helper()
	expireClaim(t, db, id)
	b := makeBus(t, db)
	if n, err := b.Recover(ctx, 10); err != nil || n != 1 {
		t.Fatalf("恢复过期事件=%d %v", n, err)
	}
	return oneClaim(t, b, ctx)
}

func planStage(t *testing.T, db *gorm.DB, eventID string) event.Stage {
	t.Helper()
	var row queueRow
	if err := db.Table("events").Where("id=?", eventID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.Stage
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
	// 计划与阶段同事务：落计划即可投递，恢复不需要再进 generate。
	if stage := planStage(t, db, id); stage != event.StageDeliver {
		t.Fatalf("建计划后阶段=%s want deliver", stage)
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
	if resumed.Stage != event.StageDeliver {
		t.Fatalf("窗口1恢复阶段=%s want deliver", resumed.Stage)
	}
	res, err := snd.Resume(ctx, resumed)
	if err != nil || !res.OK || !res.Verified {
		t.Fatalf("恢复投递=%+v %v", res, err)
	}
	if post.sends != 1 || post.texts[0] != "计划原文" {
		t.Fatalf("应按计划原文重投：sends=%d texts=%v", post.sends, post.texts)
	}

	// S05：accepted 记账与输入结算同事务，Send 返回时输入已 done。
	sendState(t, db, id, event.StatusDone, event.StageVerify)
	p, cur := planCurrent(t, db, id)
	if p.AttemptCount != 1 || cur.State != sender.StateAccepted || cur.Number != 1 {
		t.Fatalf("计划/尝试=%+v/%+v", p, cur)
	}
}

// TestDeliveryPlanDMActorSurvivesQueue 接收账号随事件落库，领取时仍能还原。
func TestDeliveryPlanDMActorSurvivesQueue(t *testing.T) {
	db, ctx := repositoryDB(t)
	id, session := "dm:10001:20002:903", "10001:20002"
	b := makeBus(t, db)
	ev := testEvent(id, event.ChDM, session, true, time.Now().UTC().Truncate(time.Microsecond))
	ev.FromID = "20002"
	ev.Meta = []byte(`{"recv_actor":"10001"}`)
	if _, err := b.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}
	c := oneClaim(t, b, ctx)
	if c.Event.Session != session {
		t.Fatalf("领取后的 Session=%q want %q", c.Event.Session, session)
	}
	actor, ok := event.DMRecvActor(c.Event)
	if !ok || actor != planAccount {
		t.Fatalf("接收账号未随事件持久化：actor=%q ok=%v meta=%s", actor, ok, c.Event.Meta)
	}
	peer, ok := event.DMPeer(c.Event)
	if !ok || peer != "20002" {
		t.Fatalf("收信人解析=%q ok=%v", peer, ok)
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
	// 取得许可即进入 verify：崩溃恢复只能核验，不能重投。
	if stage := planStage(t, db, id); stage != event.StageVerify {
		t.Fatalf("arm 后阶段=%s want verify", stage)
	}

	resumed := planRecover(t, db, ctx, id)
	if resumed.Stage != event.StageVerify {
		t.Fatalf("armed 恢复阶段=%s want verify", resumed.Stage)
	}
	_, err = snd.Resume(ctx, resumed)
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
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusQueued}, event.StatusDead); err != nil {
		t.Fatal(err)
	}
	sendState(t, db, id, event.StatusDead, event.StageVerify)
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
	// 恢复只走 Resume：模型不重新生成，已记录的未知结果也不会重发。
	_, err = snd.Resume(ctx, resumed)
	if planKind(t, err) != sender.KindUncertain {
		t.Fatalf("kind=%s want uncertain", planKind(t, err))
	}
	if post.sends != 0 {
		t.Fatal("unknown 结果被重新投递")
	}
}

// TestDeliveryPlanCrashAfterAcceptBeforeSent 对应窗口 4：记录已 accepted，尚未标 sent。
//
// S05 起 accepted 记账与输入结算同事务，正常路径不再留下这个窗口；
// 这里用旧数据形态（accepted 尝试 + 未结算输入）验证恢复只核验不重发。
func TestDeliveryPlanCrashAfterAcceptBeforeSent(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:666:999", "666"
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
	// 模拟 S05 之前的旧形态：尝试已 accepted，但输入事件与审计尚未结清。
	if err := db.Exec("UPDATE delivery_attempts SET state='accepted',ok=TRUE,verified=TRUE,msg_id='legacy-rpid' WHERE id=?", a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE delivery_plans SET state='accepted' WHERE event_id=?", id).Error; err != nil {
		t.Fatal(err)
	}

	resumed := planRecover(t, db, ctx, id)
	if resumed.Stage != event.StageVerify {
		t.Fatalf("已接受结果恢复阶段=%s want verify", resumed.Stage)
	}
	res, err := snd.Resume(ctx, resumed)
	if err != nil || !res.OK || !res.Verified || res.MsgID != "legacy-rpid" {
		t.Fatalf("恢复核验=%+v %v", res, err)
	}
	if post.sends != 0 {
		t.Fatalf("已接受结果被重复投递：%d", post.sends)
	}
	if n := planAttemptCount(t, db, id); n != 1 {
		t.Fatalf("重复结算产生新尝试：%d", n)
	}

	// 旧数据没有 S05 审计，恢复领取后仍是 queued；按既有结清路径直接标 done。
	b := makeBus(t, db)
	if err := b.Mark(ctx, resumed, []event.Status{event.StatusQueued}, event.StatusDone); err != nil {
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
		// 首轮由生成路径建计划；后续轮次走投递恢复，不再重新生成。
		var err error
		if i == 1 {
			_, err = snd.Send(ctx, c, planReply(ch, session, false, "原文"))
		} else {
			_, err = snd.Resume(ctx, c)
		}
		if planKind(t, err) != sender.KindRetry {
			t.Fatalf("第%d次 kind=%s want retry", i, planKind(t, err))
		}
		p, a := planCurrent(t, db, id)
		if p.AttemptCount != i || a.Number != i || a.State != sender.StateNotPosted {
			t.Fatalf("第%d次尝试=%+v/%+v", i, p, a)
		}
		// 可信未投递必须把阶段退回 deliver，恢复/重试才会继续投递而不是误走核验。
		if stage := planStage(t, db, id); stage != event.StageDeliver {
			t.Fatalf("第%d次尝试后阶段=%s want deliver", i, stage)
		}
		if err := b.Defer(ctx, c, time.Now().UTC().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("UPDATE events SET retry_at=clock_timestamp()-interval '1 second' WHERE id=?", id).Error; err != nil {
			t.Fatal(err)
		}
		c = oneClaim(t, b, ctx)
	}
	if post.sends != 3 {
		t.Fatalf("重试实际 POST=%d want 3", post.sends)
	}

	_, err := snd.Resume(ctx, c)
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

// TestDeliveryPlanAcceptedWritesSendLogAndOut S05：评论/私信成功投递写 send_log + out，
// 并在同一事务把输入事件结清为 done。
func TestDeliveryPlanAcceptedWritesSendLogAndOut(t *testing.T) {
	cases := []struct {
		name    string
		ch      event.Channel
		id      string
		session string
		private bool
	}{
		{"comment", event.ChComment, "comment:321:654", "321", false},
		{"dm", event.ChDM, "dm:20002:901", "20002", true},
		// 新格式：Session/计划目标带接收账号，同一对方在不同账号下互相隔离。
		{"dm_actor_scoped", event.ChDM, "dm:10001:20002:902", "10001:20002", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, ctx := repositoryDB(t)
			c := planFixture(t, db, ctx, tc.id, tc.ch, tc.session, tc.private)
			repo, snd, post := planSetup(t, db, tc.ch)
			post.result = sender.Result{OK: true, MsgID: "platform-" + tc.name, Verified: true}

			res, err := snd.Send(ctx, c, planReply(tc.ch, tc.session, tc.private, "平台实际正文"))
			if err != nil || !res.OK || !res.Verified || res.MsgID != "platform-"+tc.name {
				t.Fatalf("投递=%+v %v", res, err)
			}
			p, a := planCurrent(t, db, tc.id)
			if p.State != sender.StateAccepted || a.State != sender.StateAccepted || a.MsgID != "platform-"+tc.name {
				t.Fatalf("计划/尝试=%+v/%+v", p, a)
			}

			// 输入结算与两条输出事实同一事务：Send 返回时全部可见。
			var row queueRow
			if err := db.Table("events").Where("id=?", tc.id).Take(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.Event.Status != event.StatusDone || row.Stage != event.StageVerify || row.Owner != nil || row.Version != c.Lease.Version+1 {
				t.Fatalf("输入结算=%+v owner=%v version=%d", row, row.Owner, row.Version)
			}

			var logRow struct {
				EventID   string `gorm:"column:event_id"`
				Channel   string
				OK        bool
				Verified  bool
				MsgID     string `gorm:"column:msg_id"`
				Error     string
				AttemptID string `gorm:"column:attempt_id"`
			}
			if err := db.Table("send_log").Where("attempt_id=?", a.ID).Take(&logRow).Error; err != nil {
				t.Fatalf("缺少 send_log：%v", err)
			}
			if logRow.EventID != tc.id || logRow.Channel != string(tc.ch) || !logRow.OK || !logRow.Verified ||
				logRow.MsgID != "platform-"+tc.name || logRow.Error != "" || logRow.AttemptID != a.ID {
				t.Fatalf("send_log=%+v", logRow)
			}

			type outRow struct {
				ID      string
				Channel string
				Dir     string
				Session string
				FromID  string `gorm:"column:from_id"`
				Text    string
				Private bool
				ReplyTo string `gorm:"column:reply_to"`
				Status  string
				Stage   string
				Meta    json.RawMessage
			}
			outID := string(tc.ch) + ":out:" + tc.session + ":" + a.ID
			var out outRow
			if err := db.Table("events").Where("id=?", outID).Take(&out).Error; err != nil {
				t.Fatalf("缺少 out 事件：%v", err)
			}
			if out.Channel != string(tc.ch) || out.Dir != string(event.DirOut) || out.Session != tc.session ||
				out.FromID != planAccount || out.Text != "平台实际正文" || out.Private != tc.private ||
				out.ReplyTo != tc.id || out.Status != string(event.StatusDone) || out.Stage != string(event.StageVerify) {
				t.Fatalf("out=%+v", out)
			}
			var meta map[string]any
			if err := json.Unmarshal(out.Meta, &meta); err != nil || meta["attempt_id"] != a.ID || meta["input_id"] != tc.id {
				t.Fatalf("out meta=%s %v", out.Meta, err)
			}

			// 重复记录（恢复/重复回调）不重复插账，也不二次递增租约。
			if _, err := repo.RecordPlan(ctx, c, planKey(tc.ch, tc.session), a.ID, store.PlanOutcome{
				State: sender.StateAccepted, OK: true, Verified: true, MsgID: a.MsgID,
			}); err != nil {
				t.Fatal(err)
			}
			var logs, outs int64
			if err := db.Table("send_log").Where("attempt_id=?", a.ID).Count(&logs).Error; err != nil || logs != 1 {
				t.Fatalf("send_log 行数=%d %v", logs, err)
			}
			if err := db.Table("events").Where("id=?", outID).Count(&outs).Error; err != nil || outs != 1 {
				t.Fatalf("out 行数=%d %v", outs, err)
			}
			var version int64
			if err := db.Table("events").Select("lease_version").Where("id=?", tc.id).Scan(&version).Error; err != nil || version != c.Lease.Version+1 {
				t.Fatalf("重复记录改写了租约：%d %v", version, err)
			}
		})
	}
}

// TestDeliveryPlanAccountingIsAtomic 记账中任一写入失败时整体回滚，不留下半套事实。
func TestDeliveryPlanAccountingIsAtomic(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:444:555", "444"
	c := planFixture(t, db, ctx, id, ch, session, false)
	_, snd, post := planSetup(t, db, ch)

	if err := db.Exec("ALTER TABLE events ADD CONSTRAINT reject_plan_out CHECK(id NOT LIKE 'comment:out:%') NOT VALID").Error; err != nil {
		t.Fatalf("注入 out 事件失败：%v", err)
	}
	defer func() { _ = db.Exec("ALTER TABLE events DROP CONSTRAINT IF EXISTS reject_plan_out").Error }()

	_, err := snd.Send(ctx, c, planReply(ch, session, false, "原文"))
	if planKind(t, err) != sender.KindUncertain {
		t.Fatalf("kind=%s want uncertain", planKind(t, err))
	}
	if post.sends != 1 {
		t.Fatalf("POST 次数=%d", post.sends)
	}

	var logs, outs int64
	if err := db.Table("send_log").Where("event_id=?", id).Count(&logs).Error; err != nil || logs != 0 {
		t.Fatalf("回滚后仍有 send_log=%d %v", logs, err)
	}
	if err := db.Table("events").Where("reply_to=?", id).Count(&outs).Error; err != nil || outs != 0 {
		t.Fatalf("回滚后仍有 out 事件=%d %v", outs, err)
	}
	p, a := planCurrent(t, db, id)
	if p.State != sender.StateArmed || a.State != sender.StateArmed {
		t.Fatalf("计划/尝试未回滚=%+v/%+v", p, a)
	}
	var row queueRow
	if err := db.Table("events").Where("id=?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Event.Status != event.StatusReplying || row.Stage != event.StageVerify ||
		row.Owner == nil || *row.Owner != c.Lease.Owner || row.Version != c.Lease.Version {
		t.Fatalf("输入被半套记账改写=%+v owner=%v version=%d", row, row.Owner, row.Version)
	}
}

// TestDeliveryPlanAcceptedWithoutReceiptIsUnconfirmed 平台接受到但没有稳定回执：
// 记 accepted/verified=0，send_log ok=1 verified=0 且 msg_id 为空，out 保持 retry
// 并标 unknown_manual，输入 dead（不是 done）；恢复只返回同一事实，不重投。
func TestDeliveryPlanAcceptedWithoutReceiptIsUnconfirmed(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:777:888", "777"
	c := planFixture(t, db, ctx, id, ch, session, false)
	_, snd, post := planSetup(t, db, ch)
	post.result = sender.Result{OK: true}

	res, err := snd.Send(ctx, c, planReply(ch, session, false, "平台已收但无回执"))
	if err == nil || planKind(t, err) != sender.KindUncertain || !sender.Handled(err) {
		t.Fatalf("投递=%+v err=%v", res, err)
	}
	if !res.OK || res.Verified {
		t.Fatalf("结果=%+v", res)
	}
	if post.sends != 1 {
		t.Fatalf("POST 次数=%d", post.sends)
	}
	p, a := planCurrent(t, db, id)
	if p.State != sender.StateAccepted || a.State != sender.StateAccepted ||
		a.Verified || a.MsgID != "" || a.ErrorKind != "unconfirmed" {
		t.Fatalf("计划/尝试=%+v/%+v", p, a)
	}

	// 输入结清为 dead：平台可能已收下，不能重投，也不冒充 done。
	var row queueRow
	if err := db.Table("events").Where("id=?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Event.Status != event.StatusDead || row.Stage != event.StageVerify ||
		row.Owner != nil || row.Version != c.Lease.Version+1 {
		t.Fatalf("输入结算=%+v owner=%v version=%d", row, row.Owner, row.Version)
	}

	var logRow struct {
		OK        bool
		Verified  bool
		MsgID     string `gorm:"column:msg_id"`
		Error     string
		AttemptID string `gorm:"column:attempt_id"`
	}
	if err := db.Table("send_log").Where("attempt_id=?", a.ID).Take(&logRow).Error; err != nil {
		t.Fatalf("缺少 send_log：%v", err)
	}
	if !logRow.OK || logRow.Verified || logRow.MsgID != "" || logRow.Error != "unconfirmed" {
		t.Fatalf("send_log=%+v", logRow)
	}

	outID := string(ch) + ":out:" + session + ":" + a.ID
	var out struct {
		Status string
		Meta   json.RawMessage
	}
	if err := db.Table("events").Where("id=?", outID).Take(&out).Error; err != nil {
		t.Fatalf("缺少 out 事件：%v", err)
	}
	if out.Status != string(event.StatusRetry) {
		t.Fatalf("out 状态=%s want retry", out.Status)
	}
	var meta map[string]any
	if err := json.Unmarshal(out.Meta, &meta); err != nil || meta["unknown_manual"] != true || meta["verified"] != false || meta["outcome"] != string(sender.StateAccepted) {
		t.Fatalf("out meta=%s %v", out.Meta, err)
	}

	// 模拟旧数据窗口：尝试已 accepted 但输入还没结清、租约仍在。
	// 恢复必须只按「接受但未验证」停止，不再 POST。
	if err := db.Table("events").Where("id=?", id).Updates(map[string]any{
		"status": event.StatusReplying, "stage": event.StageVerify,
		"lease_owner": c.Lease.Owner, "lease_version": c.Lease.Version,
		"lease_until": time.Now().UTC().Add(time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	res, err = snd.Resume(ctx, c)
	if err == nil || planKind(t, err) != sender.KindUncertain {
		t.Fatalf("恢复 err=%v", err)
	}
	if !res.OK || res.Verified || post.sends != 1 {
		t.Fatalf("恢复 res=%+v sends=%d", res, post.sends)
	}
}

// TestDeliveryPlanAcceptedWithReceiptIsSentUnconfirmed 平台接受且回执稳定、
// 只是没完成核验：按「已发送（未确认）」记账（out + send_log 可见），
// 输入 done 且不再重投；与真正无回执的 unknown_manual 区分。
func TestDeliveryPlanAcceptedWithReceiptIsSentUnconfirmed(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:778:889", "778"
	c := planFixture(t, db, ctx, id, ch, session, false)
	_, snd, post := planSetup(t, db, ch)
	post.result = sender.Result{OK: true, MsgID: "platform-unconfirmed", Verified: false}

	res, err := snd.Send(ctx, c, planReply(ch, session, false, "平台已收但未核验"))
	if err == nil || planKind(t, err) != sender.KindUncertain || !sender.Handled(err) {
		t.Fatalf("投递=%+v err=%v", res, err)
	}
	if !res.OK || res.Verified || res.MsgID != "platform-unconfirmed" {
		t.Fatalf("结果=%+v", res)
	}
	if post.sends != 1 {
		t.Fatalf("POST 次数=%d", post.sends)
	}
	p, a := planCurrent(t, db, id)
	if p.State != sender.StateAccepted || a.State != sender.StateAccepted ||
		a.Verified || a.MsgID != "platform-unconfirmed" || a.ErrorKind != "unconfirmed" {
		t.Fatalf("计划/尝试=%+v/%+v", p, a)
	}

	// 有稳定回执就是已发送：输入按 done 结清，不再重投。
	var row queueRow
	if err := db.Table("events").Where("id=?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Event.Status != event.StatusDone || row.Stage != event.StageVerify ||
		row.Owner != nil || row.Version != c.Lease.Version+1 {
		t.Fatalf("输入结算=%+v owner=%v version=%d", row, row.Owner, row.Version)
	}

	var logRow struct {
		OK        bool
		Verified  bool
		MsgID     string `gorm:"column:msg_id"`
		Error     string
		AttemptID string `gorm:"column:attempt_id"`
	}
	if err := db.Table("send_log").Where("attempt_id=?", a.ID).Take(&logRow).Error; err != nil {
		t.Fatalf("缺少 send_log：%v", err)
	}
	if !logRow.OK || logRow.Verified || logRow.MsgID != "platform-unconfirmed" || logRow.Error != "unconfirmed" {
		t.Fatalf("send_log=%+v", logRow)
	}

	outID := string(ch) + ":out:" + session + ":" + a.ID
	var out struct {
		Status string
		Meta   json.RawMessage
	}
	if err := db.Table("events").Where("id=?", outID).Take(&out).Error; err != nil {
		t.Fatalf("缺少 out 事件：%v", err)
	}
	if out.Status != string(event.StatusRetry) {
		t.Fatalf("out 状态=%s want retry", out.Status)
	}
	var meta map[string]any
	if err := json.Unmarshal(out.Meta, &meta); err != nil || meta["sent_unconfirmed"] != true || meta["unknown_manual"] != nil ||
		meta["verified"] != false || meta["evidence"] != "platform_receipt" || meta["outcome"] != string(sender.StateAccepted) {
		t.Fatalf("out meta=%s %v", out.Meta, err)
	}

	// 恢复只返回同一事实，不重投；旧数据窗口里输入未结清时也不能重发。
	if err := db.Table("events").Where("id=?", id).Updates(map[string]any{
		"status": event.StatusReplying, "stage": event.StageVerify,
		"lease_owner": c.Lease.Owner, "lease_version": c.Lease.Version,
		"lease_until": time.Now().UTC().Add(time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	res, err = snd.Resume(ctx, c)
	if err == nil || planKind(t, err) != sender.KindUncertain {
		t.Fatalf("恢复 err=%v", err)
	}
	if !res.OK || res.Verified || res.MsgID != "platform-unconfirmed" || post.sends != 1 {
		t.Fatalf("恢复 res=%+v sends=%d", res, post.sends)
	}
}

// TestDeliveryPlanForgedVerifiedIsCorrected 数据层不信任调用方给的 Verified：
// 没有稳定回执时落库即纠正为未验证，输入 dead 而不是 done；
// 重复记录走已接受分支，同样不会把未验证事实升级成 done。
func TestDeliveryPlanForgedVerifiedIsCorrected(t *testing.T) {
	db, ctx := repositoryDB(t)
	ch, id, session := event.ChComment, "comment:666:777", "666"
	c := planFixture(t, db, ctx, id, ch, session, false)
	repo, _, _ := planSetup(t, db, ch)
	key := planKey(ch, session)

	a, err := repo.PreparePlan(ctx, c, key, "原文")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ArmPlan(ctx, c, key, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordPlan(ctx, c, key, a.ID, store.PlanOutcome{State: sender.StateAccepted, OK: true, Verified: true}); err != nil {
		t.Fatal(err)
	}
	_, got := planCurrent(t, db, id)
	if got.State != sender.StateAccepted || got.Verified || got.MsgID != "" || got.ErrorKind != "unconfirmed" {
		t.Fatalf("伪造 Verified 未纠正：%+v", got)
	}
	var row queueRow
	if err := db.Table("events").Where("id=?", id).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Event.Status != event.StatusDead || row.Owner != nil || row.Version != c.Lease.Version+1 {
		t.Fatalf("输入结算=%+v owner=%v version=%d", row, row.Owner, row.Version)
	}

	outID := string(ch) + ":out:" + session + ":" + a.ID
	var out struct {
		Status string
	}
	if err := db.Table("events").Where("id=?", outID).Take(&out).Error; err != nil {
		t.Fatalf("缺少 out 事件：%v", err)
	}
	if out.Status != string(event.StatusRetry) {
		t.Fatalf("out 状态=%s want retry", out.Status)
	}

	// 重复记录：已接受分支不再结清、不重复递增租约，也不改写 out。
	if _, err := repo.RecordPlan(ctx, c, key, a.ID, store.PlanOutcome{State: sender.StateAccepted, OK: true, Verified: true}); err != nil {
		t.Fatal(err)
	}
	var logs, outs int64
	if err := db.Table("send_log").Where("attempt_id=?", a.ID).Count(&logs).Error; err != nil || logs != 1 {
		t.Fatalf("send_log 行数=%d %v", logs, err)
	}
	if err := db.Table("events").Where("id=?", outID).Count(&outs).Error; err != nil || outs != 1 {
		t.Fatalf("out 行数=%d %v", outs, err)
	}
	var version int64
	if err := db.Table("events").Select("lease_version").Where("id=?", id).Scan(&version).Error; err != nil || version != c.Lease.Version+1 {
		t.Fatalf("重复记录改写了租约：%d %v", version, err)
	}
	if err := db.Table("events").Where("id=?", outID).Take(&out).Error; err != nil || out.Status != string(event.StatusRetry) {
		t.Fatalf("重复记录改写 out：%s %v", out.Status, err)
	}
}
