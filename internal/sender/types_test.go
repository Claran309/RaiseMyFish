package sender

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEvidenceOriginCannotBeSwapped(t *testing.T) {
	for _, v := range []Verdict{{Outcome: OutcomeAccepted, MsgID: "id", Verified: true, Evidence: "history_id", Origin: OriginPost}, {Outcome: OutcomeAccepted, MsgID: "id", Verified: true, Evidence: "receipt_id", Origin: OriginVerify}} {
		if err := v.Validate(); err == nil {
			t.Fatal("跨来源证据被接受")
		}
	}
}

func TestReplyScopeAndPayloadValidation(t *testing.T) {
	c := event.Claim{Event: event.Event{ID: "live:123:message", Channel: event.ChLive, Dir: event.DirIn, Session: "123"}, Lease: event.Lease{Owner: "owner", Version: 1}}
	p := Reply{Text: "正常公开回复", Scope: memory.Scope{Channel: event.ChLive, Session: "123"}}
	if err := p.Validate(c); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Reply, *event.Claim){func(p *Reply, _ *event.Claim) { p.Scope.Private = true }, func(p *Reply, _ *event.Claim) { p.Scope.Session = "other" }, func(p *Reply, _ *event.Claim) { p.Scope.Channel = event.ChDM }, func(p *Reply, _ *event.Claim) { p.Text = " " }, func(p *Reply, _ *event.Claim) { p.Text = strings.Repeat("鱼", 41) }, func(p *Reply, _ *event.Claim) { p.Text = string([]byte{255}) }, func(p *Reply, _ *event.Claim) { p.Actions = []Action{{Name: "tool"}} }, func(_ *Reply, c *event.Claim) { c.Event.Private = true }, func(_ *Reply, c *event.Claim) { c.Event.Dir = event.DirOut }, func(_ *Reply, c *event.Claim) { c.Event.Channel = event.ChQQ }, func(_ *Reply, c *event.Claim) { c.Event.Session = "room" }, func(_ *Reply, c *event.Claim) { c.Lease.Owner = "" }, func(_ *Reply, c *event.Claim) { c.Lease.Version = 0 }} {
		bad, claim := p, c
		mutate(&bad, &claim)
		if err := bad.Validate(claim); err == nil {
			t.Fatal("错误范围或载荷被接受")
		}
	}
}

func TestPolicyBoundsAndVerdictProof(t *testing.T) {
	p := DefaultPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Policy){func(p *Policy) { p.MinInterval = 0 }, func(p *Policy) { p.DuplicateWindow = 0 }, func(p *Policy) { p.PermitTTL = 11 * time.Second }, func(p *Policy) { p.RetryDelay = 0 }, func(p *Policy) { p.MaxAttempts = 4 }, func(p *Policy) { p.MaxVerifications = 4 }} {
		bad := p
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("不安全策略被接受")
		}
	}
	valid := Verdict{Outcome: OutcomeAccepted, Origin: OriginPost, Verified: true, MsgID: "platform-id", Evidence: "receipt_id"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Verdict){func(v *Verdict) { v.Origin = "unknown" }, func(v *Verdict) { v.Outcome = "unknown-outcome" }, func(v *Verdict) { v.MsgID = "0" }, func(v *Verdict) { v.MsgID = "bad:id" }, func(v *Verdict) { v.Evidence = "none" }, func(v *Verdict) { v.Verified = false }, func(v *Verdict) { v.ErrorKind = "untrusted-private-data" }} {
		bad := valid
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatal("弱/伪证据被接受")
		}
	}
	for _, outcome := range []string{OutcomeRejected, OutcomeUnknown, OutcomeNotPosted} {
		v := Verdict{Outcome: outcome, Origin: OriginPost, Evidence: "none", ErrorKind: "network"}
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSafeErrorAndHandledClassification(t *testing.T) {
	cause := errors.New("secret-cause-canary")
	e := &Error{Kind: KindUncertain, Cause: cause, Settled: true}
	if !errors.Is(e, cause) || !Handled(e) || Handled(cause) || Handled(nil) {
		t.Fatal("错误链或结清分类错误")
	}
	encoded, err := json.Marshal(e)
	if err != nil || strings.Contains(string(encoded), "canary") || strings.Contains(e.Error(), "canary") {
		t.Fatal("错误回显秘密")
	}
	for _, kind := range []Kind{KindInput, KindRetry, KindDead, KindSkipped, KindAuth, KindUncertain, "unexpected"} {
		if text := (&Error{Kind: kind}).Error(); !strings.Contains(text, "弹幕发送结果") {
			t.Fatal("缺少安全错误文字")
		}
	}
	if !ValidActor("123") || ValidActor("0") || ValidActor("room") {
		t.Fatal("账号标识校验错误")
	}
	a := Attempt{State: StateAccepted, Verified: true, MsgID: "id"}
	if !a.Result().OK || !a.Result().Verified {
		t.Fatal("已核验投递结果丢失")
	}
}
