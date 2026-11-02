package live

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

type proactiveRecorder struct {
	events []event.Event
	err    error
}

func (r *proactiveRecorder) Insert(_ context.Context, e event.Event) (bool, error) {
	r.events = append(r.events, e)
	return r.err == nil, r.err
}

type proactiveSession struct {
	attempt sender.Attempt
	permit  sender.Permit
	verdict sender.Verdict
	err     error
	posted  int
	closed  int
}

func (s *proactiveSession) Post(_ context.Context, permit sender.Permit) (sender.Verdict, error) {
	s.posted++
	s.permit = permit
	return s.verdict, s.err
}

func (s *proactiveSession) Close() error {
	s.closed++
	return nil
}

type proactiveTransport struct {
	attempt sender.Attempt
	openErr error
	session *proactiveSession
	opened  int
}

func (t *proactiveTransport) Open(_ context.Context, a sender.Attempt) (Session, error) {
	t.opened++
	t.attempt = a
	if t.openErr != nil {
		return nil, t.openErr
	}
	if t.session == nil {
		t.session = &proactiveSession{}
	}
	t.session.attempt = a
	return t.session, nil
}

func (t *proactiveTransport) Verify(context.Context, sender.Attempt) (sender.Verdict, error) {
	return sender.Verdict{}, errors.New("主动弹幕不应核验")
}

func newProactiveFixture(t *testing.T, verdict sender.Verdict) (*ProactiveSender, *proactiveTransport, *proactiveRecorder) {
	t.Helper()
	transport := &proactiveTransport{session: &proactiveSession{verdict: verdict}}
	recorder := &proactiveRecorder{}
	o := DefaultProactiveOptions()
	o.RoomID = "123"
	o.SelfID = "456"
	p, err := NewProactiveSender(transport, recorder, o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	p.newToken = func() (string, error) { return "pro-fixed", nil }
	p.newNonce = func() (string, error) { return "789", nil }
	return p, transport, recorder
}

func TestProactiveSendAcceptedRecordsOutEvent(t *testing.T) {
	p, transport, recorder := newProactiveFixture(t, sender.Verdict{Outcome: sender.OutcomeAccepted, Verified: true, MsgID: "msg-1", Evidence: "receipt_id"})
	res, err := p.Send(context.Background(), "鱼鱼今天状态不错")
	if err != nil || !res.OK || !res.Verified || transport.opened != 1 || transport.session.closed != 1 {
		t.Fatal("正常发送路径错误", res, err)
	}
	if transport.attempt.ID != "pro-fixed" || transport.attempt.EventID != "live:out:123:pro-fixed" || transport.attempt.RoomID != "123" || transport.attempt.ActorID != "456" {
		t.Fatalf("Attempt 构造错误：%+v", transport.attempt)
	}
	permit := transport.session.permit
	if permit.AttemptID != "pro-fixed" || permit.RequestID != "pro-fixed" || permit.Nonce != "789" || !permit.ExpiresAt.After(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("Permit 构造错误：%+v", permit)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("应写入一条出事件，实际 %d", len(recorder.events))
	}
	out := recorder.events[0]
	if out.ID != "live:out:123:pro-fixed" || out.Dir != event.DirOut || out.Status != event.StatusDone ||
		out.Session != "123" || out.FromID != "456" || out.Text != "鱼鱼今天状态不错" || out.ReplyTo != "" || !out.At.Equal(p.now()) {
		t.Fatalf("出事件字段错误：+%v", out)
	}
	var meta map[string]any
	if err := json.Unmarshal(out.Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["attempt_id"] != "pro-fixed" || meta["outcome"] != sender.OutcomeAccepted || meta["mode"] != "proactive" || meta["verified"] != true || meta["evidence"] != "receipt_id" {
		t.Fatalf("出事件Meta错误：%v", meta)
	}
}

func TestProactiveSendRejectedAndUnknownAreDead(t *testing.T) {
	cases := []sender.Verdict{
		{Outcome: sender.OutcomeRejected, ErrorKind: "content", Evidence: "none"},
		{Outcome: sender.OutcomeUnknown, ErrorKind: "timeout", Evidence: "none"},
	}
	for _, verdict := range cases {
		p, transport, recorder := newProactiveFixture(t, verdict)
		res, err := p.Send(context.Background(), "鱼鱼好可爱")
		if err == nil || res.OK || transport.session.closed != 1 {
			t.Fatal("失败结果应报错并关闭会话", verdict.Outcome, res, err)
		}
		var typed *sender.Error
		if !errors.As(err, &typed) || typed.Kind != sender.KindUncertain {
			t.Fatal("失败结果分类错误", err)
		}
		if len(recorder.events) != 1 || recorder.events[0].Status != event.StatusDead {
			t.Fatalf("失败结果应写 dead 出事件：%+v", recorder.events)
		}
	}
}

func TestProactiveSendNotPostedRecordsSkipped(t *testing.T) {
	p, transport, recorder := newProactiveFixture(t, sender.Verdict{Outcome: sender.OutcomeNotPosted, ErrorKind: "permit", Evidence: "none"})
	res, err := p.Send(context.Background(), "鱼鱼好可爱")
	if err == nil || res.OK {
		t.Fatal("未投递不应报成功")
	}
	var typed *sender.Error
	if !errors.As(err, &typed) || typed.Kind != sender.KindSkipped {
		t.Fatal("未投递分类错误", err)
	}
	if transport.opened != 1 || len(recorder.events) != 1 || recorder.events[0].Status != event.StatusSkipped {
		t.Fatalf("未投递应写 skipped 出事件：%+v", recorder.events)
	}
}

func TestProactiveSendPostErrorIsUnknownAndSafe(t *testing.T) {
	p, transport, recorder := newProactiveFixture(t, sender.Verdict{})
	transport.session.err = errors.New("private-response-canary")
	res, err := p.Send(context.Background(), "鱼鱼好可爱")
	if err == nil || res.OK || strings.Contains(err.Error(), "canary") {
		t.Fatal("未知结果泄漏或报成功", err)
	}
	if len(recorder.events) != 1 || recorder.events[0].Status != event.StatusDead {
		t.Fatalf("Post失败应写 dead 出事件：%+v", recorder.events)
	}
	var meta map[string]any
	if err := json.Unmarshal(recorder.events[0].Meta, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["outcome"] != sender.OutcomeUnknown || meta["error_kind"] != "network" {
		t.Fatalf("Post失败Meta错误：%v", meta)
	}
}

func TestProactiveSendOpenFailureWritesSkipped(t *testing.T) {
	p, transport, recorder := newProactiveFixture(t, sender.Verdict{})
	transport.openErr = &sender.Error{Kind: sender.KindAuth}
	res, err := p.Send(context.Background(), "鱼鱼好可爱")
	if err == nil || res.Error != "auth" || transport.session.posted != 0 {
		t.Fatal("预检失败路径错误", res, err)
	}
	if len(recorder.events) != 1 || recorder.events[0].Status != event.StatusSkipped {
		t.Fatalf("预检失败应写 skipped 出事件：%+v", recorder.events)
	}
}

func TestProactiveSendRejectsInvalidInputBeforeIO(t *testing.T) {
	for _, text := range []string{"", "   ", strings.Repeat("鱼", 41), "鱼\x00鱼"} {
		p, transport, recorder := newProactiveFixture(t, sender.Verdict{Outcome: sender.OutcomeAccepted})
		if _, err := p.Send(context.Background(), text); err == nil || transport.opened != 0 || len(recorder.events) != 0 {
			t.Fatalf("非法文本产生了外部操作：%q", text)
		}
	}
	p, transport, recorder := newProactiveFixture(t, sender.Verdict{Outcome: sender.OutcomeAccepted})
	if _, err := p.Send(nil, "鱼鱼好可爱"); err == nil || transport.opened != 0 || len(recorder.events) != 0 {
		t.Fatal("nil 上下文产生了外部操作")
	}
}

func TestProactiveSendRecordFailureKeepsSendResult(t *testing.T) {
	p, transport, recorder := newProactiveFixture(t, sender.Verdict{Outcome: sender.OutcomeAccepted, Verified: true, MsgID: "msg-2", Evidence: "receipt_id"})
	recorder.err = errors.New("db down")
	res, err := p.Send(context.Background(), "鱼鱼好可爱")
	if err == nil || !res.OK || transport.opened != 1 {
		t.Fatal("记账失败不应吞掉真实发送结果", res, err)
	}
	if !strings.Contains(err.Error(), "出事件") {
		t.Fatal("记账失败未包装说明", err)
	}
}

func TestProactiveSendTokenFailureBeforeIO(t *testing.T) {
	p, transport, recorder := newProactiveFixture(t, sender.Verdict{Outcome: sender.OutcomeAccepted})
	p.newToken = func() (string, error) { return "", errors.New("no entropy") }
	if _, err := p.Send(context.Background(), "鱼鱼好可爱"); err == nil || transport.opened != 0 || len(recorder.events) != 0 {
		t.Fatal("令牌失败产生了外部操作")
	}
}

func TestNewProactiveSenderValidation(t *testing.T) {
	transport := &proactiveTransport{}
	recorder := &proactiveRecorder{}
	base := DefaultProactiveOptions()
	base.RoomID, base.SelfID = "123", "456"
	if _, err := NewProactiveSender(transport, recorder, base, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ProactiveOptions){
		func(o *ProactiveOptions) { o.RoomID = "0" },
		func(o *ProactiveOptions) { o.SelfID = "" },
		func(o *ProactiveOptions) { o.PermitTTL = 0 },
		func(o *ProactiveOptions) { o.PermitTTL = 11 * time.Second },
		func(o *ProactiveOptions) { o.CleanupTimeout = 0 },
	} {
		o := base
		mutate(&o)
		if _, err := NewProactiveSender(transport, recorder, o, zap.NewNop()); err == nil {
			t.Fatalf("非法参数未被拒绝：%+v", o)
		}
	}
	if _, err := NewProactiveSender(transport, recorder, base, nil); err == nil {
		t.Fatal("nil logger 未被拒绝")
	}
	if _, err := NewProactiveSender(nil, recorder, base, zap.NewNop()); err == nil {
		t.Fatal("nil transport 未被拒绝")
	}
	if _, err := NewProactiveSender(transport, nil, base, zap.NewNop()); err == nil {
		t.Fatal("nil recorder 未被拒绝")
	}
}
