package dryrun

import (
	"context"
	"testing"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
)

func claim() event.Claim {
	return event.Claim{
		Event: event.Event{
			ID: "live:1:2", Channel: event.ChLive, Dir: event.DirIn, Session: "1",
			Text: "在吗", Status: event.StatusReplying,
		},
		Lease: event.Lease{Owner: "w", Version: 1},
	}
}

func reply(c event.Claim) sender.Reply {
	return sender.Reply{Text: "在的", Scope: memory.Scope{Channel: c.Event.Channel, Session: c.Event.Session}}
}

// TestSendReturnsVerifiedReceipt 成功路径返回稳定回执，且按通道计数。
func TestSendReturnsVerifiedReceipt(t *testing.T) {
	s := New("test")
	c := claim()
	res, err := s.Send(context.Background(), c, reply(c))
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.Verified || res.MsgID == "" {
		t.Fatalf("回执不符：%+v", res)
	}
	res2, err := s.Send(context.Background(), c, reply(c))
	if err != nil || res2.MsgID == res.MsgID {
		t.Fatalf("回执未递增：%v %v", res2, err)
	}
	stats := s.Stats()
	if stats.Sent != 2 || stats.ByChannel["live"] != 2 {
		t.Fatalf("计数错误：%+v", stats)
	}
}

// TestSendRejectsBadInput 非法入参不能伪装成功。
func TestSendRejectsBadInput(t *testing.T) {
	s := New("")
	good := claim()
	cases := []struct {
		name string
		c    event.Claim
		p    sender.Reply
	}{
		{"缺租约", event.Claim{Event: good.Event}, reply(good)},
		{"空正文", good, sender.Reply{Text: " ", Scope: memory.Scope{Channel: event.ChLive, Session: "1"}}},
		{"范围不符", good, sender.Reply{Text: "在", Scope: memory.Scope{Channel: event.ChLive, Session: "other"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Send(context.Background(), tc.c, tc.p); err == nil {
				t.Fatal("非法入参被接受")
			}
		})
	}
	if _, err := s.Send(nil, good, reply(good)); err == nil { //nolint:staticcheck // 刻意传 nil 验证防御
		t.Fatal("nil ctx 被接受")
	}
}

// TestResumeMatchesSend Resume 不产生额外网络语义，返回成功。
func TestResumeMatchesSend(t *testing.T) {
	s := New("test")
	if _, err := s.Send(context.Background(), claim(), reply(claim())); err != nil {
		t.Fatal(err)
	}
	res, err := s.Resume(context.Background(), claim())
	if err != nil || !res.OK {
		t.Fatalf("Resume 失败：%+v %v", res, err)
	}
}

// TestNilSenderDoesNotPanic nil 接收者返回明确错误。
func TestNilSenderDoesNotPanic(t *testing.T) {
	var s *Sender
	if _, err := s.Send(context.Background(), claim(), reply(claim())); err == nil {
		t.Fatal("nil 发送器被接受")
	}
	if stats := s.Stats(); stats.Sent != 0 || stats.ByChannel == nil {
		t.Fatalf("nil Stats 错误：%+v", stats)
	}
}
