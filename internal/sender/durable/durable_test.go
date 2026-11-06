package durable

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

const testAccount = "10001"

func commentClaim() event.Claim {
	return event.Claim{
		Event: event.Event{
			ID: "comment:555:666", Channel: event.ChComment, Dir: event.DirIn,
			Session: "555", Status: event.StatusReplying,
		},
		Stage: event.StageGenerate,
		Lease: event.Lease{Owner: "worker-1", Version: 3},
	}
}

func dmClaim() event.Claim {
	return event.Claim{
		Event: event.Event{
			ID: "dm:20002:900", Channel: event.ChDM, Dir: event.DirIn,
			Session: "20002", Private: true, Status: event.StatusReplying,
		},
		Stage: event.StageGenerate,
		Lease: event.Lease{Owner: "worker-1", Version: 3},
	}
}

func replyFor(c event.Claim, text string) sender.Reply {
	return sender.Reply{Text: text, Scope: memory.Scope{Channel: c.Event.Channel, Session: c.Event.Session, Private: c.Event.Private}}
}

func normalize(c event.Claim, p sender.Reply) (sender.Reply, error) {
	if strings.TrimSpace(p.Text) == "" {
		return sender.Reply{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("空正文")}
	}
	return sender.Reply{Text: strings.TrimSpace(p.Text), Scope: p.Scope}, nil
}

// fakeRepo 模拟 PlannedRepo：记录调用顺序与落库结果。
type fakeRepo struct {
	attempt      sender.Attempt
	prepareState string
	prepareErr   error
	loadErr      error
	armErr       error
	recordErr    error

	preparedText string
	armed        bool
	recorded     *store.PlanOutcome
	recordedID   string
	prepareCalls int
	loadCalls    int
	armCalls     int
	recordCalls  int
}

func (f *fakeRepo) PreparePlan(_ context.Context, c event.Claim, key store.PlanKey, text string) (sender.Attempt, error) {
	f.prepareCalls++
	if f.prepareErr != nil {
		return sender.Attempt{}, f.prepareErr
	}
	if f.attempt.ID == "" {
		f.attempt = sender.Attempt{
			ID: "attempt-1", EventID: c.Event.ID, ActorID: key.AccountID,
			RoomID: key.Destination, Text: text, Number: 1, State: sender.StateReady,
		}
	}
	if f.prepareState != "" {
		f.attempt.State = f.prepareState
	}
	f.preparedText = text
	return f.attempt, nil
}

func (f *fakeRepo) LoadPlan(_ context.Context, c event.Claim, key store.PlanKey) (sender.Attempt, error) {
	f.loadCalls++
	if f.loadErr != nil {
		return sender.Attempt{}, f.loadErr
	}
	if f.attempt.ID == "" {
		f.attempt = sender.Attempt{ID: "attempt-1", EventID: c.Event.ID, ActorID: key.AccountID, RoomID: key.Destination, Text: "原文", Number: 1, State: sender.StateReady}
	}
	return f.attempt, nil
}

func (f *fakeRepo) ArmPlan(_ context.Context, _ event.Claim, _ store.PlanKey, id string) (sender.Attempt, error) {
	f.armCalls++
	if f.armErr != nil {
		return sender.Attempt{}, f.armErr
	}
	if id != f.attempt.ID {
		return sender.Attempt{}, store.ErrConflict
	}
	f.attempt.State = sender.StateArmed
	f.armed = true
	return f.attempt, nil
}

func (f *fakeRepo) RecordPlan(_ context.Context, _ event.Claim, _ store.PlanKey, id string, o store.PlanOutcome) (sender.Attempt, error) {
	f.recordCalls++
	if f.recordErr != nil {
		return sender.Attempt{}, f.recordErr
	}
	f.recorded = &o
	f.recordedID = id
	f.attempt.State = o.State
	f.attempt.Verified = o.Verified
	f.attempt.MsgID = o.MsgID
	f.attempt.ErrorKind = o.ErrorKind
	return f.attempt, nil
}

// fakeSender 是下游平台发送器，只记录是否真的发生了 POST。
type fakeSender struct {
	sends  int
	text   string
	sendAt func()
	result sender.Result
	err    error
}

func (f *fakeSender) Name() string { return "fake" }

func (f *fakeSender) Send(_ context.Context, _ event.Claim, r sender.Reply) (sender.Result, error) {
	f.sends++
	f.text = r.Text
	if f.sendAt != nil {
		f.sendAt()
	}
	return f.result, f.err
}

func (f *fakeSender) Resume(context.Context, event.Claim) (sender.Result, error) {
	return sender.Result{}, errors.New("不应调用底层 Resume")
}

func newTestSender(t *testing.T, ch event.Channel, repo store.PlannedRepo, inner sender.Sender) *Sender {
	t.Helper()
	s, err := New(repo, inner, Options{
		Channel: ch, AccountID: testAccount, Validate: normalize, MaxAttempts: 3,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func kindOf(t *testing.T, err error) sender.Kind {
	t.Helper()
	var typed *sender.Error
	if !errors.As(err, &typed) {
		t.Fatalf("错误类型不是 *sender.Error：%v", err)
	}
	return typed.Kind
}

// TestSendPlanBeforeArmBeforePost 校验顺序：先落计划，再 arm，最后才 POST 且正文取计划快照。
func TestSendPlanBeforeArmBeforePost(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{result: sender.Result{OK: true, MsgID: "rpid-1", Verified: true}}
	inner.sendAt = func() {
		if repo.prepareCalls != 1 || repo.armCalls != 1 || !repo.armed {
			t.Errorf("POST 前缺少计划或许可：prepare=%d arm=%d armed=%v", repo.prepareCalls, repo.armCalls, repo.armed)
		}
		if repo.attempt.State != sender.StateArmed {
			t.Errorf("POST 时 attempt 状态=%s want armed", repo.attempt.State)
		}
	}
	s := newTestSender(t, event.ChComment, repo, inner)
	c := commentClaim()
	res, err := s.Send(context.Background(), c, replyFor(c, "  你好呀  "))
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.Verified || res.MsgID != "rpid-1" || inner.sends != 1 {
		t.Fatalf("结果=%+v sends=%d", res, inner.sends)
	}
	if inner.text != "你好呀" || repo.preparedText != "你好呀" {
		t.Fatalf("正文未按计划快照发送：inner=%q plan=%q", inner.text, repo.preparedText)
	}
	if repo.recorded == nil || repo.recorded.State != sender.StateAccepted || !repo.recorded.Verified {
		t.Fatalf("结果未记录为 accepted：%+v", repo.recorded)
	}
}

func TestSendPrepareFailureNeverPosts(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want sender.Kind
	}{
		{"infra", errors.New("db down"), sender.KindRetry},
		{"conflict", store.ErrConflict, sender.KindInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{prepareErr: tc.err}
			inner := &fakeSender{}
			s := newTestSender(t, event.ChComment, repo, inner)
			c := commentClaim()
			_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
			if kindOf(t, err) != tc.want {
				t.Fatalf("kind=%s want %s", kindOf(t, err), tc.want)
			}
			if inner.sends != 0 {
				t.Fatal("计划失败仍然 POST")
			}
			if sender.Handled(err) {
				t.Fatal("未落计划不应标记已结清")
			}
		})
	}
}

func TestSendArmFailureNeverPosts(t *testing.T) {
	repo := &fakeRepo{armErr: errors.New("锁失败")}
	inner := &fakeSender{}
	s := newTestSender(t, event.ChComment, repo, inner)
	c := commentClaim()
	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if kindOf(t, err) != sender.KindUncertain {
		t.Fatalf("kind=%s want uncertain", kindOf(t, err))
	}
	if inner.sends != 0 || repo.recordCalls != 0 {
		t.Fatal("未取得许可仍然 POST 或记录")
	}
}

func TestSendArmedOrUnknownNeverReposts(t *testing.T) {
	for _, state := range []string{sender.StateArmed, sender.StateUnknown} {
		t.Run(state, func(t *testing.T) {
			repo := &fakeRepo{prepareState: state, attempt: sender.Attempt{ID: "attempt-1", Text: "原文"}}
			inner := &fakeSender{result: sender.Result{OK: true}}
			s := newTestSender(t, event.ChComment, repo, inner)
			c := commentClaim()
			_, err := s.Send(context.Background(), c, replyFor(c, "新正文"))
			if kindOf(t, err) != sender.KindUncertain {
				t.Fatalf("kind=%s want uncertain", kindOf(t, err))
			}
			if inner.sends != 0 || repo.recordCalls != 0 {
				t.Fatal("armed/unknown 被重新投递")
			}
		})
	}
}

func TestSendAcceptedAttemptSettlesWithoutPost(t *testing.T) {
	repo := &fakeRepo{
		prepareState: sender.StateAccepted,
		attempt:      sender.Attempt{ID: "attempt-1", Text: "原文", MsgID: "rpid-9", Verified: true},
	}
	inner := &fakeSender{}
	s := newTestSender(t, event.ChComment, repo, inner)
	c := commentClaim()
	res, err := s.Send(context.Background(), c, replyFor(c, "新正文"))
	if err != nil || !res.OK || !res.Verified || res.MsgID != "rpid-9" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if inner.sends != 0 || repo.armCalls != 0 {
		t.Fatal("accepted 计划被再次投递")
	}
}

func TestSendTrustedNotPostedKeepsRetryAt(t *testing.T) {
	at := time.Now().Add(30 * time.Second).UTC().Truncate(time.Microsecond)
	repo := &fakeRepo{}
	inner := &fakeSender{err: &sender.Error{Kind: sender.KindRetry, RetryAt: at, Cause: errors.New("限流")}}
	s := newTestSender(t, event.ChComment, repo, inner)
	c := commentClaim()
	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if repo.recorded == nil || repo.recorded.State != sender.StateNotPosted {
		t.Fatalf("未记录为 not_posted：%+v", repo.recorded)
	}
	if kindOf(t, err) != sender.KindRetry {
		t.Fatalf("kind=%s want retry", kindOf(t, err))
	}
	var retryable interface{ RetryAfter() (time.Time, bool) }
	if !errors.As(err, &retryable) {
		t.Fatal("错误缺少 RetryAfter")
	}
	if got, ok := retryable.RetryAfter(); !ok || !got.Equal(at) {
		t.Fatalf("RetryAt=%v want %v", got, at)
	}
	if sender.Handled(err) {
		t.Fatal("可重试错误不应标记已结清")
	}
}

func TestSendUncertainRecordsUnknown(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{err: &sender.Error{Kind: sender.KindUncertain, Cause: errors.New("超时")}}
	s := newTestSender(t, event.ChComment, repo, inner)
	c := commentClaim()
	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if repo.recorded == nil || repo.recorded.State != sender.StateUnknown {
		t.Fatalf("未记录为 unknown：%+v", repo.recorded)
	}
	if kindOf(t, err) != sender.KindUncertain {
		t.Fatalf("kind=%s want uncertain", kindOf(t, err))
	}
	if sender.Handled(err) {
		t.Fatal("unknown 不应标记已结清")
	}
}

func TestSendRejectedRecordsRejected(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{err: &sender.Error{Kind: sender.KindInput, Cause: errors.New("内容无效")}}
	s := newTestSender(t, event.ChComment, repo, inner)
	c := commentClaim()
	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if repo.recorded == nil || repo.recorded.State != sender.StateRejected || repo.recorded.ErrorKind != "invalid" {
		t.Fatalf("未记录为 rejected：%+v", repo.recorded)
	}
	if kindOf(t, err) != sender.KindInput {
		t.Fatalf("kind=%s want input", kindOf(t, err))
	}
}

func TestSendRecordFailureAfterAcceptIsUncertain(t *testing.T) {
	repo := &fakeRepo{recordErr: errors.New("db down")}
	inner := &fakeSender{result: sender.Result{OK: true, MsgID: "rpid-1", Verified: true}}
	s := newTestSender(t, event.ChComment, repo, inner)
	c := commentClaim()
	res, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if inner.sends != 1 {
		t.Fatal("未发生 POST")
	}
	if res.OK {
		t.Fatal("记录失败不能向调用方宣称已成功")
	}
	if kindOf(t, err) != sender.KindUncertain || sender.Handled(err) {
		t.Fatalf("kind=%s handled=%v", kindOf(t, err), sender.Handled(err))
	}
}

func TestResumeOnlyVerifiesKnownPlan(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		repo := &fakeRepo{loadErr: store.ErrNotFound}
		inner := &fakeSender{}
		s := newTestSender(t, event.ChComment, repo, inner)
		c := commentClaim()
		c.Stage = event.StageVerify
		_, err := s.Resume(context.Background(), c)
		if kindOf(t, err) != sender.KindDead {
			t.Fatalf("kind=%s want dead", kindOf(t, err))
		}
		if inner.sends != 0 {
			t.Fatal("无计划仍然 POST")
		}
	})
	t.Run("accepted", func(t *testing.T) {
		repo := &fakeRepo{attempt: sender.Attempt{ID: "attempt-1", Text: "原文", State: sender.StateAccepted, MsgID: "rpid-1", Verified: true}}
		inner := &fakeSender{}
		s := newTestSender(t, event.ChComment, repo, inner)
		c := commentClaim()
		c.Stage = event.StageVerify
		res, err := s.Resume(context.Background(), c)
		if err != nil || !res.OK || !res.Verified {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if inner.sends != 0 {
			t.Fatal("已接受计划被重新投递")
		}
	})
	t.Run("armed", func(t *testing.T) {
		repo := &fakeRepo{attempt: sender.Attempt{ID: "attempt-1", Text: "原文", State: sender.StateArmed}}
		inner := &fakeSender{}
		s := newTestSender(t, event.ChComment, repo, inner)
		c := commentClaim()
		c.Stage = event.StageVerify
		_, err := s.Resume(context.Background(), c)
		if kindOf(t, err) != sender.KindUncertain {
			t.Fatalf("kind=%s want uncertain", kindOf(t, err))
		}
		if inner.sends != 0 {
			t.Fatal("armed 计划在核验阶段被重新投递")
		}
	})
	t.Run("ready_in_verify", func(t *testing.T) {
		repo := &fakeRepo{attempt: sender.Attempt{ID: "attempt-1", Text: "原文", State: sender.StateReady}}
		inner := &fakeSender{}
		s := newTestSender(t, event.ChComment, repo, inner)
		c := commentClaim()
		c.Stage = event.StageVerify
		_, err := s.Resume(context.Background(), c)
		if kindOf(t, err) != sender.KindUncertain {
			t.Fatalf("kind=%s want uncertain", kindOf(t, err))
		}
		if inner.sends != 0 {
			t.Fatal("verify 阶段禁止补发")
		}
	})
}

// TestDmAmbiguousRetryIsUnknown：私信的网络/超时 retry 没有 RetryAt，
// 不能断言平台没收到，必须按未知处理。
func TestDmAmbiguousRetryIsUnknown(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{err: &sender.Error{Kind: sender.KindRetry, Cause: errors.New("网络异常")}}
	s := newTestSender(t, event.ChDM, repo, inner)
	c := dmClaim()
	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if repo.recorded == nil || repo.recorded.State != sender.StateUnknown {
		t.Fatalf("未记录为 unknown：%+v", repo.recorded)
	}
	if kindOf(t, err) != sender.KindUncertain {
		t.Fatalf("kind=%s want uncertain", kindOf(t, err))
	}
}

func TestDmRateLimitedRetryIsNotPosted(t *testing.T) {
	at := time.Now().Add(30 * time.Second).UTC().Truncate(time.Microsecond)
	repo := &fakeRepo{}
	inner := &fakeSender{err: &sender.Error{Kind: sender.KindRetry, RetryAt: at, Cause: errors.New("限流")}}
	s := newTestSender(t, event.ChDM, repo, inner)
	c := dmClaim()
	_, err := s.Send(context.Background(), c, replyFor(c, "正文"))
	if repo.recorded == nil || repo.recorded.State != sender.StateNotPosted {
		t.Fatalf("未记录为 not_posted：%+v", repo.recorded)
	}
	if kindOf(t, err) != sender.KindRetry {
		t.Fatalf("kind=%s want retry", kindOf(t, err))
	}
}

func TestSendValidateFailureNeverPrepares(t *testing.T) {
	repo := &fakeRepo{}
	inner := &fakeSender{}
	s, err := New(repo, inner, Options{
		Channel: event.ChComment, AccountID: testAccount,
		Validate: func(event.Claim, sender.Reply) (sender.Reply, error) {
			return sender.Reply{}, &sender.Error{Kind: sender.KindInput, Cause: errors.New("不合格")}
		},
		MaxAttempts: 3,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	c := commentClaim()
	_, err = s.Send(context.Background(), c, replyFor(c, "正文"))
	if kindOf(t, err) != sender.KindInput {
		t.Fatalf("kind=%s want input", kindOf(t, err))
	}
	if repo.prepareCalls != 0 || inner.sends != 0 {
		t.Fatal("校验失败仍然落计划或 POST")
	}
}
