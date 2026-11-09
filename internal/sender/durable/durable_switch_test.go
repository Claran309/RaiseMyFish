package durable

// 本文件是 S09/T18 的发送归属测试：
//   - 切号后进行中的尝试只按计划原账号解析凭据，不交给新活跃账号；
//   - 计划 key（account=平台 MID）在 prepare/arm/record 全链路保持一致；
//   - 已确认的尝试不重发，armed/unknown 只核验；
//   - 旧账号凭据不可用时保持 ready，不产生任何平台请求。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

const oldMID = "111"
const newMID = "222"
const oldCookie = "SESSDATA=old-secret; bili_jct=csrf-old"
const newCookie = "SESSDATA=new-secret; bili_jct=csrf-new"

func actor(accountID, mid, cookie string) bili.Actor {
	return bili.Actor{AccountID: accountID, MID: mid, Cookie: cookie, CSRF: "csrf", Source: "db"}
}

// fakeActors 模拟「当前活跃账号已切到新号，但旧账号仍可取回凭据」。
type fakeActors struct {
	current      bili.Actor
	currentErr   error
	currentCalls int
	byMID        map[string]bili.Actor
	byMIDErr     error
	byMIDCalls   []string
}

func (f *fakeActors) CurrentActor(context.Context) (bili.Actor, error) {
	f.currentCalls++
	if f.currentErr != nil {
		return bili.Actor{}, f.currentErr
	}
	return f.current, nil
}

func (f *fakeActors) ActorByMID(_ context.Context, mid string) (bili.Actor, error) {
	f.byMIDCalls = append(f.byMIDCalls, mid)
	if f.byMIDErr != nil {
		return bili.Actor{}, f.byMIDErr
	}
	if a, ok := f.byMID[mid]; ok {
		return a, nil
	}
	return bili.Actor{}, bili.ErrNoCredential
}

func newSwitchSender(t *testing.T, ch event.Channel, repo store.PlannedRepo, inner sender.Sender, actors *fakeActors) *Sender {
	t.Helper()
	s, err := New(repo, inner, Options{
		Channel: ch, AccountID: oldMID, Validate: normalize, MaxAttempts: 3, Actors: actors,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func requireKey(t *testing.T, label string, got store.PlanKey) {
	t.Helper()
	if got.AccountID != oldMID {
		t.Fatalf("%s 使用了非计划账号：%+v", label, got)
	}
	if got.Channel != event.ChComment || got.Destination != "555" {
		t.Fatalf("%s 计划 key 不完整：%+v", label, got)
	}
}

// TestSendSwitchedActiveKeepsOldPlanAccount 当前已是新号，旧计划仍按旧号落计划并发送。
func TestSendSwitchedActiveKeepsOldPlanAccount(t *testing.T) {
	repo := &fakeRepo{planAccount: oldMID}
	inner := &fakeSender{result: sender.Result{OK: true, MsgID: "rpid-1", Verified: true}}
	actors := &fakeActors{
		current: actor("account-new", newMID, newCookie),
		byMID:   map[string]bili.Actor{oldMID: actor("account-old", oldMID, oldCookie)},
	}
	s := newSwitchSender(t, event.ChComment, repo, inner, actors)
	c := commentClaim()

	res, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if err != nil || !res.OK || !res.Verified {
		t.Fatalf("发送失败：%+v %v", res, err)
	}
	requireKey(t, "prepare", repo.prepareKey)
	requireKey(t, "arm", repo.armKey)
	requireKey(t, "record", repo.recordKey)
	if len(actors.byMIDCalls) != 1 || actors.byMIDCalls[0] != oldMID {
		t.Fatalf("应按计划账号取旧凭据：%v", actors.byMIDCalls)
	}
	if inner.sends != 1 || inner.actor == nil {
		t.Fatalf("应发生一次带账号快照的 POST：sends=%d actor=%+v", inner.sends, inner.actor)
	}
	if inner.actor.MID != oldMID || inner.actor.Cookie != oldCookie || inner.actor.AccountID != "account-old" {
		t.Fatalf("POST 串用了新账号：%+v", inner.actor)
	}
}

// TestSendSwitchDuringPrepareKeepsSnapshot 计划落库前切号，POST 仍用开头捕获的旧快照。
func TestSendSwitchDuringPrepareKeepsSnapshot(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{result: sender.Result{OK: true, MsgID: "rpid-2", Verified: true}}
	actors := &fakeActors{
		current: actor("account-old", oldMID, oldCookie),
		byMID:   map[string]bili.Actor{oldMID: actor("account-old", oldMID, oldCookie)},
	}
	// 计划落库后、arm/POST 之前面板完成切号。
	repo.onPrepare = func() { actors.current = actor("account-new", newMID, newCookie) }
	s := newSwitchSender(t, event.ChComment, repo, inner, actors)
	c := commentClaim()

	res, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if err != nil || !res.OK {
		t.Fatalf("发送失败：%+v %v", res, err)
	}
	requireKey(t, "prepare", repo.prepareKey)
	requireKey(t, "arm", repo.armKey)
	requireKey(t, "record", repo.recordKey)
	// 开头快照与计划账号一致，不需要重新解析；但绝不能用切号后的新号。
	if len(actors.byMIDCalls) != 0 {
		t.Fatalf("同账号快照不应再查账号表：%v", actors.byMIDCalls)
	}
	if inner.actor == nil || inner.actor.MID != oldMID || inner.actor.Cookie != oldCookie {
		t.Fatalf("POST 串用了新账号：%+v", inner.actor)
	}
}

// TestResumeReadyUsesPlanAccount 旧计划恢复只按计划账号核验/投递，不跟随当前活跃账号。
func TestResumeReadyUsesPlanAccount(t *testing.T) {
	repo := &fakeRepo{planAccount: oldMID, attempt: sender.Attempt{ID: "attempt-1", Text: "原文", State: sender.StateReady}}
	inner := &fakeSender{result: sender.Result{OK: true, MsgID: "rpid-3", Verified: true}}
	actors := &fakeActors{
		current: actor("account-new", newMID, newCookie),
		byMID:   map[string]bili.Actor{oldMID: actor("account-old", oldMID, oldCookie)},
	}
	s := newSwitchSender(t, event.ChComment, repo, inner, actors)
	c := commentClaim()
	c.Stage = event.StageDeliver

	res, err := s.Resume(context.Background(), c)
	if err != nil || !res.OK {
		t.Fatalf("恢复失败：%+v %v", res, err)
	}
	requireKey(t, "load", repo.loadKey)
	requireKey(t, "arm", repo.armKey)
	requireKey(t, "record", repo.recordKey)
	if len(actors.byMIDCalls) != 1 || actors.byMIDCalls[0] != oldMID {
		t.Fatalf("恢复应按计划账号取旧凭据：%v", actors.byMIDCalls)
	}
	if inner.actor == nil || inner.actor.MID != oldMID || inner.actor.Cookie != oldCookie {
		t.Fatalf("恢复用新账号重发：%+v", inner.actor)
	}
}

// TestResumeOldCredentialUnavailableStaysReady 旧账号凭据不可用时不 arm、不 POST，保持 ready。
func TestResumeOldCredentialUnavailableStaysReady(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"no_credential", bili.ErrNoCredential},
		{"lookup_failed", errors.New("db down")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{planAccount: oldMID, attempt: sender.Attempt{ID: "attempt-1", Text: "原文", State: sender.StateReady}}
			inner := &fakeSender{result: sender.Result{OK: true}}
			actors := &fakeActors{
				current:  actor("account-new", newMID, newCookie),
				byMIDErr: tc.err,
			}
			s := newSwitchSender(t, event.ChComment, repo, inner, actors)
			c := commentClaim()
			c.Stage = event.StageDeliver

			_, err := s.Resume(context.Background(), c)
			if kindOf(t, err) != sender.KindUncertain {
				t.Fatalf("kind=%s want uncertain", kindOf(t, err))
			}
			if repo.armCalls != 0 || repo.recordCalls != 0 || inner.sends != 0 {
				t.Fatalf("旧账号不可用仍产生了投递：arm=%d record=%d sends=%d", repo.armCalls, repo.recordCalls, inner.sends)
			}
			if repo.attempt.State != sender.StateReady {
				t.Fatalf("尝试应保持 ready：%s", repo.attempt.State)
			}
		})
	}
}

// TestResumeConfirmedNeverLooksUpOrReposts 已确认的尝试直接返回事实，不查旧账号也不重发。
func TestResumeConfirmedNeverLooksUpOrReposts(t *testing.T) {
	repo := &fakeRepo{
		planAccount: oldMID,
		attempt:     sender.Attempt{ID: "attempt-1", Text: "原文", State: sender.StateAccepted, MsgID: "rpid-9", Verified: true},
	}
	inner := &fakeSender{}
	actors := &fakeActors{current: actor("account-new", newMID, newCookie)}
	s := newSwitchSender(t, event.ChComment, repo, inner, actors)
	c := commentClaim()
	c.Stage = event.StageVerify

	res, err := s.Resume(context.Background(), c)
	if err != nil || !res.OK || !res.Verified || res.MsgID != "rpid-9" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if inner.sends != 0 || repo.armCalls != 0 || len(actors.byMIDCalls) != 0 {
		t.Fatalf("已确认尝试不应重发或查凭据：sends=%d arm=%d lookups=%v", inner.sends, repo.armCalls, actors.byMIDCalls)
	}
}

// TestResumeArmedOrUnknownNeverRepostsAcrossSwitch armed/unknown 在切号后仍只核验。
func TestResumeArmedOrUnknownNeverRepostsAcrossSwitch(t *testing.T) {
	for _, state := range []string{sender.StateArmed, sender.StateUnknown} {
		t.Run(state, func(t *testing.T) {
			repo := &fakeRepo{planAccount: oldMID, attempt: sender.Attempt{ID: "attempt-1", Text: "原文", State: state}}
			inner := &fakeSender{}
			actors := &fakeActors{current: actor("account-new", newMID, newCookie)}
			s := newSwitchSender(t, event.ChComment, repo, inner, actors)
			c := commentClaim()
			c.Stage = event.StageDeliver

			_, err := s.Resume(context.Background(), c)
			if kindOf(t, err) != sender.KindUncertain {
				t.Fatalf("kind=%s want uncertain", kindOf(t, err))
			}
			if inner.sends != 0 || repo.armCalls != 0 || len(actors.byMIDCalls) != 0 {
				t.Fatalf("armed/unknown 不应 arm、查凭据或重发：sends=%d arm=%d lookups=%v", inner.sends, repo.armCalls, actors.byMIDCalls)
			}
		})
	}
}

// TestResumePlanMissingIsDead 计划不存在时拒绝重发（切号不应凭空造出新计划）。
func TestResumePlanMissingIsDead(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{}
	actors := &fakeActors{current: actor("account-new", newMID, newCookie)}
	s := newSwitchSender(t, event.ChComment, repo, inner, actors)
	c := commentClaim()
	c.Stage = event.StageDeliver

	_, err := s.Resume(context.Background(), c)
	if kindOf(t, err) != sender.KindDead {
		t.Fatalf("kind=%s want dead", kindOf(t, err))
	}
	if repo.loadCalls != 0 || inner.sends != 0 {
		t.Fatal("无计划仍然读取或发送")
	}
}

// TestSendCurrentActorUnavailable 当前账号不可用时直接失败，不落计划。
func TestSendCurrentActorUnavailable(t *testing.T) {
	repo := &fakeRepo{planAccount: oldMID}
	inner := &fakeSender{}
	actors := &fakeActors{currentErr: bili.ErrNoCredential}
	s := newSwitchSender(t, event.ChComment, repo, inner, actors)
	c := commentClaim()

	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if kindOf(t, err) != sender.KindAuth {
		t.Fatalf("kind=%s want auth", kindOf(t, err))
	}
	if repo.prepareCalls != 0 || inner.sends != 0 {
		t.Fatal("账号不可用仍然落计划或发送")
	}
}

// TestSendPlanAccountFromDBOverridesCurrent 数据库里的旧计划账号优先于当前活跃账号。
func TestSendPlanAccountFromDBOverridesCurrent(t *testing.T) {
	repo := &fakeRepo{planAccount: " " + oldMID + " "}
	inner := &fakeSender{result: sender.Result{OK: true, MsgID: "rpid-4"}}
	actors := &fakeActors{
		current: actor("account-new", newMID, newCookie),
		byMID:   map[string]bili.Actor{oldMID: actor("account-old", oldMID, oldCookie)},
	}
	s := newSwitchSender(t, event.ChComment, repo, inner, actors)
	c := commentClaim()

	if _, err := s.Send(context.Background(), c, replyFor(c, "正文")); err != nil {
		t.Fatal(err)
	}
	if repo.prepareKey.AccountID != oldMID || strings.TrimSpace(repo.prepareKey.AccountID) != oldMID {
		t.Fatalf("计划账号未取数据库里的旧号：%+v", repo.prepareKey)
	}
	if inner.actor == nil || inner.actor.MID != oldMID {
		t.Fatalf("发送未绑定旧号：%+v", inner.actor)
	}
}
