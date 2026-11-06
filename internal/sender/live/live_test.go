package live

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"context"
	"encoding/json"
	"errors"
	"go.uber.org/zap"
	"strings"
	"testing"
	"time"
)

type fakeRepo struct {
	store.DeliveryRepo
	a                                  sender.Attempt
	prepared, armed, settled, deferred int
	last                               sender.Verdict
}

func (r *fakeRepo) Prepare(_ context.Context, _ event.Claim, p sender.Reply, actor string) (sender.Attempt, error) {
	r.prepared++
	r.a.Text = p.Text
	r.a.ActorID = actor
	return r.a, nil
}
func (r *fakeRepo) Load(context.Context, event.Claim, string) (sender.Attempt, error) {
	return r.a, nil
}
func (r *fakeRepo) Arm(context.Context, event.Claim, string) (sender.Permit, error) {
	r.armed++
	return sender.Permit{AttemptID: r.a.ID, RequestID: r.a.ID, Nonce: "1234567890", ExpiresAt: time.Now().Add(time.Second)}, nil
}
func (r *fakeRepo) Defer(_ context.Context, _ event.Claim, _ string, d sender.Deferral) (sender.Result, error) {
	r.deferred++
	return sender.Result{Error: string(d.Kind)}, &sender.Error{Kind: d.Kind, RetryAt: d.RetryAt}
}
func (r *fakeRepo) Settle(_ context.Context, _ event.Claim, _ string, v sender.Verdict) (sender.Result, error) {
	r.settled++
	r.last = v
	res := sender.Result{OK: v.Outcome == sender.OutcomeAccepted, MsgID: v.MsgID, Verified: v.Verified}
	if !v.Verified {
		return res, &sender.Error{Kind: sender.KindUncertain}
	}
	return res, nil
}

type fakeTransport struct {
	opened, posted, verified, closed int
	openedAttempt                    sender.Attempt
	verifiedAttempt                  sender.Attempt
	verdict                          sender.Verdict
	// verdicts 按调用顺序返回；用完后回落到 verdict。
	verdicts  []sender.Verdict
	verifyErr error
	postErr   error
}

func (f *fakeTransport) Open(_ context.Context, a sender.Attempt) (Session, error) {
	f.opened++
	f.openedAttempt = a
	return &fakeSession{f: f}, nil
}
func (f *fakeTransport) Verify(_ context.Context, a sender.Attempt) (sender.Verdict, error) {
	f.verifiedAttempt = a
	index := f.verified
	f.verified++
	if index < len(f.verdicts) {
		return f.verdicts[index], f.verifyErr
	}
	return f.verdict, f.verifyErr
}

type fakeSession struct{ f *fakeTransport }

func (s *fakeSession) Post(context.Context, sender.Permit) (sender.Verdict, error) {
	s.f.posted++
	return s.f.verdict, s.f.postErr
}
func (s *fakeSession) Close() error { s.f.closed++; return nil }
func fixture(t *testing.T) (*Service, *fakeRepo, *fakeTransport, event.Claim, sender.Reply) {
	t.Helper()
	a := sender.Attempt{ID: "attempt-1", EventID: "live:99:in", ActorID: "8", RoomID: "99", Text: "你好鱼鱼", State: sender.StateReady, Number: 1, CreatedAt: time.Now()}
	r := &fakeRepo{a: a}
	f := &fakeTransport{verdict: sender.Verdict{Outcome: sender.OutcomeAccepted, Verified: true, MsgID: "platform-1", Evidence: "receipt_id"}}
	o := DefaultOptions()
	o.Enabled = true
	o.SelfID = "8"
	// 单测不等待重试间隔，避免用例变慢；需要测间隔的用例自己改回去
	o.VerifyInterval = 0
	s, err := New(r, f, o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	c := event.Claim{Event: event.Event{ID: a.EventID, Channel: event.ChLive, Dir: event.DirIn, Session: "99", Text: "入消息", At: time.Now(), Status: event.StatusReplying}, Stage: event.StageGenerate, Lease: event.Lease{Owner: "owner", Version: 1}, LeaseUntil: time.Now().Add(time.Minute)}
	p := sender.Reply{Text: a.Text, Scope: memory.Scope{Channel: event.ChLive, Session: "99"}}
	return s, r, f, c, p
}
func TestSendSinglePostAndVerification(t *testing.T) {
	s, r, f, c, p := fixture(t)
	res, err := s.Send(context.Background(), c, p)
	if err != nil || !res.Verified || f.posted != 1 || r.armed != 1 || r.settled != 1 || f.closed != 1 {
		t.Fatal("投递路径错误", res, err)
	}
	if r.last.Origin != sender.OriginPost {
		t.Fatal("未绑定结果来源")
	}
}
func TestUnknownDoesNotRetryAndResumeOnlyVerifies(t *testing.T) {
	s, r, f, c, p := fixture(t)
	f.postErr = errors.New("private-response-canary")
	_, err := s.Send(context.Background(), c, p)
	if err == nil || strings.Contains(err.Error(), "canary") || f.posted != 1 || r.last.Outcome != sender.OutcomeUnknown {
		t.Fatal("未知被重试或泄漏")
	}
	r.a.State = sender.StateUnknown
	c.Stage = event.StageVerify
	f.postErr = nil
	f.verdict.Evidence = "history_id"
	res, err := s.Resume(context.Background(), c)
	if err != nil || !res.Verified || f.posted != 1 || f.verified != 1 || r.last.Origin != sender.OriginVerify {
		t.Fatal("核验误入投递", err)
	}
}
func TestPrivacyLengthAndDisabledBeforeIO(t *testing.T) {
	for _, mutate := range []func(*Service, *event.Claim, *sender.Reply){func(_ *Service, c *event.Claim, _ *sender.Reply) { c.Event.Private = true }, func(_ *Service, _ *event.Claim, p *sender.Reply) { p.Scope.Private = true }, func(_ *Service, _ *event.Claim, p *sender.Reply) { p.Scope.Session = "other" }, func(_ *Service, _ *event.Claim, p *sender.Reply) { p.Text = strings.Repeat("鱼", 41) }, func(s *Service, _ *event.Claim, _ *sender.Reply) { s.options.Enabled = false }} {
		s, r, f, c, p := fixture(t)
		mutate(s, &c, &p)
		if _, err := s.Send(context.Background(), c, p); err == nil || r.prepared != 0 || f.opened != 0 {
			t.Fatal("无效输入产生了外部操作")
		}
	}
}
func TestPhysicalTokenOnlyForDelivery(t *testing.T) {
	s, r, f, c, p := fixture(t)
	if _, err := s.Send(context.Background(), c, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(context.Background(), c, p); err == nil || r.deferred != 1 || f.posted != 1 {
		t.Fatal("没有发送前节流")
	}
	r.a.State = sender.StateUnknown
	c.Stage = event.StageVerify
	f.verdict.Evidence = "history_id"
	if _, err := s.Resume(context.Background(), c); err != nil || f.verified != 1 {
		t.Fatal("核验扣了发送令牌", err)
	}
}

func TestNoPostRefundsPhysicalToken(t *testing.T) {
	s, r, f, c, p := fixture(t)
	f.verdict = sender.Verdict{Outcome: sender.OutcomeNotPosted, Evidence: "none", ErrorKind: "permit"}
	_, _ = s.Send(context.Background(), c, p)
	r.a.ID = "attempt-2"
	r.a.EventID = "live:100:next"
	r.a.RoomID = "100"
	c.Event.ID = r.a.EventID
	c.Event.Session = "100"
	p.Scope.Session = "100"
	_, _ = s.Send(context.Background(), c, p)
	if f.posted != 2 || r.deferred != 0 {
		t.Fatal("明确未投递却消耗了实际发送令牌")
	}
}
func TestKnownMessageIDCannotBeReplacedByVerifier(t *testing.T) {
	s, r, f, c, _ := fixture(t)
	r.a.State = sender.StateUnknown
	r.a.MsgID = "known-id"
	c.Stage = event.StageVerify
	f.verdict = sender.Verdict{Outcome: sender.OutcomeAccepted, Verified: true, MsgID: "other-id", Evidence: "history_id"}
	_, err := s.Resume(context.Background(), c)
	if err == nil || r.last.Verified || r.last.Outcome != sender.OutcomeUnknown {
		t.Fatal("核验更换了原消息ID")
	}
}

func TestTransportCauseRemainsBehindSafeError(t *testing.T) {
	s, _, f, c, p := fixture(t)
	cause := errors.New("private-network-canary")
	f.postErr = cause
	_, err := s.Send(context.Background(), c, p)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "canary") {
		t.Fatal("运输原因链丢失或泄漏", err)
	}
}

// unconfirmed 是「查了 history 但没有自己那条」的核验结果（脚本里的默认未知原因）。
var unconfirmed = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "unconfirmed", Evidence: "none"}

// TestVerifyRetriesUntilHistoryCatchesUp 前两次没查到、第三次查到 → 算成功，且全程不重投。
func TestVerifyRetriesUntilHistoryCatchesUp(t *testing.T) {
	s, r, f, c, _ := fixture(t)
	r.a.State = sender.StateUnknown
	c.Stage = event.StageVerify
	f.verdicts = []sender.Verdict{
		unconfirmed,
		unconfirmed,
		{Outcome: sender.OutcomeAccepted, Verified: true, MsgID: "platform-1", Evidence: "history_id"},
	}

	res, err := s.Resume(context.Background(), c)
	if err != nil || !res.Verified {
		t.Fatalf("第三次查到却未成功：%v / %+v", err, res)
	}
	if f.verified != 3 {
		t.Errorf("核验次数 = %d，期望 3", f.verified)
	}
	if f.posted != 0 || f.opened != 0 {
		t.Errorf("核验过程中重新投递了：posted=%d opened=%d", f.posted, f.opened)
	}
}

// TestVerifyExhaustedKeepsUnconfirmed 三次都没查到 → 保留 unknown 且原因分类为 unconfirmed。
func TestVerifyExhaustedKeepsUnconfirmed(t *testing.T) {
	s, r, f, c, _ := fixture(t)
	r.a.State = sender.StateUnknown
	c.Stage = event.StageVerify
	f.verdict = unconfirmed

	_, err := s.Resume(context.Background(), c)
	if err == nil {
		t.Fatal("未核验成功应返回错误")
	}
	if f.verified != s.options.VerifyAttempts {
		t.Errorf("核验次数 = %d，期望 %d", f.verified, s.options.VerifyAttempts)
	}
	if r.settled != 1 {
		t.Errorf("结算次数 = %d，期望 1", r.settled)
	}
	if r.last.Outcome != sender.OutcomeUnknown || r.last.ErrorKind != "unconfirmed" {
		t.Errorf("结算结果 = %+v，期望 unknown/unconfirmed", r.last)
	}
	if f.posted != 0 {
		t.Errorf("核验失败后重新投递了：posted=%d", f.posted)
	}
}

func TestReplyTargetFromEventMeta(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		meta       string
		fromID     string
		fromName   string
		wantMID    string
		wantName   string
	}{
		{"别人@鱼时回给发送者", "@弓长三益 收到", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "123", "弓长三益"},
		{"回复其他观众", "@弓长三益 你说得对", `{"reply_to_id":"999","reply_to_name":"弓长三益"}`, "123", "路人", "999", "弓长三益"},
		{"全角@", "＠弓长三益 好", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "123", "弓长三益"},
		{"正文没有@前缀", "收到", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "", ""},
		{"@鱼自己不产生目标", "@大肥鱼 我", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "", ""},
		{"更长昵称不算命中", "@弓长三益二号 好", `{"reply_to_id":"8","reply_to_name":"大肥鱼"}`, "123", "弓长三益", "", ""},
		{"损坏Meta", "@弓长三益 好", `{`, "123", "弓长三益", "", ""},
		{"昵称含控制字符", "@坏\x01蛋 好", `{"reply_to_id":"8"}`, "123", "坏\x01蛋", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := event.Event{Channel: event.ChLive, FromID: tc.fromID, FromName: tc.fromName, Meta: json.RawMessage(tc.meta)}
			mid, name := replyTarget(e, tc.text, "8")
			if mid != tc.wantMID || name != tc.wantName {
				t.Fatalf("目标=%q/%q，期望=%q/%q", mid, name, tc.wantMID, tc.wantName)
			}
		})
	}
}

func TestSendForwardsStructuredMention(t *testing.T) {
	s, _, f, c, p := fixture(t)
	p.Text = "@弓长三益 收到"
	c.Event.FromID, c.Event.FromName = "123", "弓长三益"
	c.Event.Meta = json.RawMessage(`{"reply_to_id":"8","reply_to_name":"大肥鱼"}`)
	if _, err := s.Send(context.Background(), c, p); err != nil {
		t.Fatal(err)
	}
	if f.openedAttempt.ReplyMID != "123" || f.openedAttempt.ReplyName != "弓长三益" {
		t.Fatalf("投递目标=%q/%q", f.openedAttempt.ReplyMID, f.openedAttempt.ReplyName)
	}

	s, r, f, c, _ := fixture(t)
	r.a.State = sender.StateUnknown
	r.a.Text = "@弓长三益 收到"
	c.Stage = event.StageVerify
	c.Event.FromID, c.Event.FromName = "123", "弓长三益"
	c.Event.Meta = json.RawMessage(`{"reply_to_id":"8","reply_to_name":"大肥鱼"}`)
	f.verdict = sender.Verdict{Outcome: sender.OutcomeUnknown, ErrorKind: "unconfirmed", Evidence: "none"}
	_, _ = s.Resume(context.Background(), c)
	if f.verifiedAttempt.ReplyMID != "123" || f.verifiedAttempt.ReplyName != "弓长三益" {
		t.Fatalf("核验恢复的目标=%q/%q", f.verifiedAttempt.ReplyMID, f.verifiedAttempt.ReplyName)
	}
}

// TestVerifyTimeoutClassified 预算耗尽与「查了没有」要分开：前者记为 timeout。
func TestVerifyTimeoutClassified(t *testing.T) {
	s, r, f, c, _ := fixture(t)
	r.a.State = sender.StateUnknown
	c.Stage = event.StageVerify
	f.verdict = unconfirmed
	s.options.VerifyInterval = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if _, err := s.Resume(ctx, c); err == nil {
		t.Fatal("超时也应有错误返回")
	}
	if r.settled != 1 {
		t.Errorf("结算次数 = %d，期望 1", r.settled)
	}
	if r.last.ErrorKind != "timeout" {
		t.Errorf("原因 = %q，期望 timeout", r.last.ErrorKind)
	}
	if f.posted != 0 {
		t.Errorf("超时后重新投递了：posted=%d", f.posted)
	}
}
