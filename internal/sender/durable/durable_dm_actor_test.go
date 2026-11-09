package durable

// N10：私信事件的接收账号从入库起沿 Session/Meta 传播到投递计划。
//
// 覆盖两个切号时刻：
//   - 入库后、首次 Send 前活跃账号已经切走；
//   - Brain 生成阻塞期间切号，生成结束才进入 Send。
// 两者都必须用事件持久化的接收账号建计划与 POST，且绝不查询当前活跃账号。

import (
	"context"
	"testing"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

// dmActorClaim 造一条新格式私信事件：Session 与 Meta 都带接收账号。
func dmActorClaim(recv, peer string) event.Claim {
	return event.Claim{
		Event: event.Event{
			ID:      "dm:" + recv + ":" + peer + ":900",
			Channel: event.ChDM,
			Dir:     event.DirIn,
			Session: event.DMSession(recv, peer),
			FromID:  peer,
			Private: true,
			Status:  event.StatusReplying,
			Meta:    []byte(`{"recv_actor":"` + recv + `"}`),
		},
		Stage: event.StageGenerate,
		Lease: event.Lease{Owner: "worker-1", Version: 3},
	}
}

// dmLegacyClaim 是旧格式私信事件：Session 只有对方 UID，Meta 无接收账号。
func dmLegacyClaim(peer string) event.Claim {
	return event.Claim{
		Event: event.Event{
			ID: "dm:" + peer + ":900", Channel: event.ChDM, Dir: event.DirIn,
			Session: peer, FromID: peer, Private: true, Status: event.StatusReplying,
		},
		Stage: event.StageGenerate,
		Lease: event.Lease{Owner: "worker-1", Version: 3},
	}
}

// TestSendDMUsesRecvActorWhenActiveSwitched 切号后首次建计划仍用接收账号。
func TestSendDMUsesRecvActorWhenActiveSwitched(t *testing.T) {
	cases := []struct {
		name string
		// duringBrain=true 表示生成开始时还是旧号活跃，生成期间才切到新号。
		duringBrain bool
	}{
		{name: "after_insert"},
		{name: "during_brain", duringBrain: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			inner := &fakeSender{result: sender.Result{OK: true, MsgID: "msg-1", Verified: true}}
			actors := &fakeActors{
				current: actor("account-new", newMID, newCookie),
				byMID:   map[string]bili.Actor{oldMID: actor("account-old", oldMID, oldCookie)},
			}
			if tc.duringBrain {
				// 生成（Brain）期间发生切号：生成入口还是旧号，Send 时已是新号。
				actors.current = actor("account-old", oldMID, oldCookie)
			}
			s := newSwitchSender(t, event.ChDM, repo, inner, actors)
			c := dmActorClaim(oldMID, "20002")
			if tc.duringBrain {
				actors.current = actor("account-new", newMID, newCookie)
			}

			res, err := s.Send(context.Background(), c, replyFor(c, "正文"))
			if err != nil || !res.OK || !res.Verified {
				t.Fatalf("发送失败：%+v %v", res, err)
			}
			if repo.prepareKey.AccountID != oldMID || repo.armKey.AccountID != oldMID || repo.recordKey.AccountID != oldMID {
				t.Fatalf("计划账号不是接收账号：prepare=%+v arm=%+v record=%+v", repo.prepareKey, repo.armKey, repo.recordKey)
			}
			if repo.prepareKey.Destination != "111:20002" || repo.prepareKey.Channel != event.ChDM {
				t.Fatalf("计划目标不是接收会话：%+v", repo.prepareKey)
			}
			if len(actors.byMIDCalls) != 1 || actors.byMIDCalls[0] != oldMID {
				t.Fatalf("应按接收账号取凭据：%v", actors.byMIDCalls)
			}
			if inner.actor == nil || inner.actor.MID != oldMID || inner.actor.Cookie != oldCookie {
				t.Fatalf("POST 串用了其他账号：%+v", inner.actor)
			}
			if actors.currentCalls != 0 {
				t.Fatalf("私信建计划不允许回落当前活跃账号：currentCalls=%d", actors.currentCalls)
			}
		})
	}
}

// TestSendDMRecvActorUnavailableNeverUsesCurrent 接收账号凭据不可用 → 不 POST、不换号。
func TestSendDMRecvActorUnavailableNeverUsesCurrent(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{result: sender.Result{OK: true}}
	actors := &fakeActors{
		current:  actor("account-new", newMID, newCookie),
		byMIDErr: bili.ErrNoCredential,
	}
	s := newSwitchSender(t, event.ChDM, repo, inner, actors)
	c := dmActorClaim(oldMID, "20002")

	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if kindOf(t, err) != sender.KindUncertain {
		t.Fatalf("kind=%s want uncertain", kindOf(t, err))
	}
	if repo.prepareCalls != 1 || repo.prepareKey.AccountID != oldMID {
		t.Fatalf("计划应绑定接收账号：calls=%d key=%+v", repo.prepareCalls, repo.prepareKey)
	}
	if repo.armCalls != 0 || inner.sends != 0 {
		t.Fatalf("旧账号不可用仍产生了平台调用：arm=%d sends=%d", repo.armCalls, inner.sends)
	}
	if actors.currentCalls != 0 {
		t.Fatalf("凭据不可用时回落了当前活跃账号：%d", actors.currentCalls)
	}
}

// TestSendDMLegacyEventWithoutRecvActorIsFrozen 旧事件没有接收账号 → 明确冻结。
func TestSendDMLegacyEventWithoutRecvActorIsFrozen(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{result: sender.Result{OK: true}}
	actors := &fakeActors{current: actor("account-new", newMID, newCookie)}
	s := newSwitchSender(t, event.ChDM, repo, inner, actors)
	c := dmLegacyClaim("20002")

	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if kindOf(t, err) != sender.KindDead {
		t.Fatalf("kind=%s want dead", kindOf(t, err))
	}
	if repo.prepareCalls != 0 || inner.sends != 0 {
		t.Fatalf("归属不明的事件仍然落计划或发送：prepare=%d sends=%d", repo.prepareCalls, inner.sends)
	}
	if actors.currentCalls != 0 {
		t.Fatalf("旧事件被静默交给当前活跃账号：%d", actors.currentCalls)
	}
}

// TestSendDMOldPlanUsesOriginalActor 已有计划的旧任务按原 actor 处理。
func TestSendDMOldPlanUsesOriginalActor(t *testing.T) {
	repo := &fakeRepo{planAccount: oldMID}
	inner := &fakeSender{result: sender.Result{OK: true, MsgID: "msg-2", Verified: true}}
	actors := &fakeActors{
		current: actor("account-new", newMID, newCookie),
		byMID:   map[string]bili.Actor{oldMID: actor("account-old", oldMID, oldCookie)},
	}
	s := newSwitchSender(t, event.ChDM, repo, inner, actors)
	c := dmLegacyClaim("20002")

	res, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if err != nil || !res.OK {
		t.Fatalf("旧计划恢复失败：%+v %v", res, err)
	}
	if repo.prepareKey.AccountID != oldMID {
		t.Fatalf("旧计划改用了其他账号：%+v", repo.prepareKey)
	}
	if inner.actor == nil || inner.actor.MID != oldMID {
		t.Fatalf("旧计划 POST 账号不符：%+v", inner.actor)
	}
	if actors.currentCalls != 0 {
		t.Fatalf("旧计划仍读取了当前活跃账号：%d", actors.currentCalls)
	}
}

// TestSendDMActorMismatchIsFrozen Session 与 Meta 归属冲突时冻结，不任选一个发送。
func TestSendDMActorMismatchIsFrozen(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{result: sender.Result{OK: true}}
	actors := &fakeActors{
		current: actor("account-new", newMID, newCookie),
		byMID: map[string]bili.Actor{
			oldMID: actor("account-old", oldMID, oldCookie),
			newMID: actor("account-new", newMID, newCookie),
		},
	}
	s := newSwitchSender(t, event.ChDM, repo, inner, actors)
	c := dmActorClaim(oldMID, "20002")
	c.Event.Meta = []byte(`{"recv_actor":"222"}`)

	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if kindOf(t, err) != sender.KindDead {
		t.Fatalf("kind=%s want dead", kindOf(t, err))
	}
	if repo.prepareCalls != 0 || inner.sends != 0 || actors.currentCalls != 0 {
		t.Fatalf("冲突归属仍然发送：prepare=%d sends=%d current=%d", repo.prepareCalls, inner.sends, actors.currentCalls)
	}
}

// TestResumeDMCompositeSessionAccepted 复合 Session 的已接受尝试只核验，不重发。
func TestResumeDMCompositeSessionAccepted(t *testing.T) {
	repo := &fakeRepo{
		planAccount: oldMID,
		attempt:     sender.Attempt{ID: "attempt-1", Text: "原文", State: sender.StateAccepted, MsgID: "msg-3", Verified: true},
	}
	inner := &fakeSender{}
	actors := &fakeActors{current: actor("account-new", newMID, newCookie)}
	s := newSwitchSender(t, event.ChDM, repo, inner, actors)
	c := dmActorClaim(oldMID, "20002")
	c.Stage = event.StageVerify

	res, err := s.Resume(context.Background(), c)
	if err != nil || !res.OK || !res.Verified || res.MsgID != "msg-3" {
		t.Fatalf("核验失败：%+v %v", res, err)
	}
	if inner.sends != 0 || repo.armCalls != 0 || len(actors.byMIDCalls) != 0 {
		t.Fatalf("已接受尝试被重发：sends=%d arm=%d lookups=%v", inner.sends, repo.armCalls, actors.byMIDCalls)
	}
}
