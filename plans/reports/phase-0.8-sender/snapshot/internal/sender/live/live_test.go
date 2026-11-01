package live

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"context"
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
	verdict                          sender.Verdict
	postErr                          error
}

func (f *fakeTransport) Open(context.Context, sender.Attempt) (Session, error) {
	f.opened++
	return &fakeSession{f: f}, nil
}
func (f *fakeTransport) Verify(context.Context, sender.Attempt) (sender.Verdict, error) {
	f.verified++
	return f.verdict, nil
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
