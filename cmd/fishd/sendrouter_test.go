package main

// 本文件测装配层发送路由：按事件通道分发，未装配通道明确拒绝。

import (
	"context"
	"errors"
	"testing"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

func routerClaim(ch event.Channel) event.Claim {
	return event.Claim{Event: event.Event{ID: string(ch) + ":1:2", Channel: ch, Session: "1"}, Lease: event.Lease{Owner: "w", Version: 1}}
}

func TestSendRouterDispatchesByChannel(t *testing.T) {
	liveSnd := &fakeSender{sendResult: sender.Result{OK: true, MsgID: "live"}}
	commentSnd := &fakeSender{sendResult: sender.Result{OK: true, MsgID: "comment"}}
	r, err := newSendRouter(map[event.Channel]sender.Sender{event.ChLive: liveSnd, event.ChComment: commentSnd})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name() != "channel-router" {
		t.Errorf("Name = %q", r.Name())
	}

	res, err := r.Send(context.Background(), routerClaim(event.ChComment), sender.Reply{Text: "评论回复"})
	if err != nil || res.MsgID != "comment" {
		t.Fatalf("评论发送未路由：%+v %v", res, err)
	}
	if commentSnd.sendCalls != 1 || liveSnd.sendCalls != 0 {
		t.Errorf("分发次数不符：comment=%d live=%d", commentSnd.sendCalls, liveSnd.sendCalls)
	}

	res, err = r.Send(context.Background(), routerClaim(event.ChLive), sender.Reply{Text: "弹幕"})
	if err != nil || res.MsgID != "live" {
		t.Fatalf("live 发送未路由：%+v %v", res, err)
	}
	if liveSnd.sendCalls != 1 || commentSnd.sendCalls != 1 {
		t.Errorf("分发次数不符：comment=%d live=%d", commentSnd.sendCalls, liveSnd.sendCalls)
	}

	if _, err := r.Resume(context.Background(), routerClaim(event.ChComment)); err != nil {
		t.Fatalf("Resume 未路由：%v", err)
	}
	if commentSnd.resumeCalls != 1 || liveSnd.resumeCalls != 0 {
		t.Errorf("Resume 分发次数不符：comment=%d live=%d", commentSnd.resumeCalls, liveSnd.resumeCalls)
	}
}

func TestSendRouterRejectsUnknownChannel(t *testing.T) {
	liveSnd := &fakeSender{}
	r, err := newSendRouter(map[event.Channel]sender.Sender{event.ChLive: liveSnd})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Send(context.Background(), routerClaim(event.ChComment), sender.Reply{})
	var typed *sender.Error
	if !errors.As(err, &typed) || typed.Kind != sender.KindSkipped {
		t.Fatalf("未装配通道应返回 skipped，实际 %v", err)
	}
	if liveSnd.sendCalls != 0 {
		t.Error("不应误投给 live 发送器")
	}
	if _, err := r.Resume(context.Background(), routerClaim(event.ChComment)); !errors.As(err, &typed) || typed.Kind != sender.KindSkipped {
		t.Fatalf("Resume 未装配通道应返回 skipped，实际 %v", err)
	}
}

func TestNewSendRouterRejectsInvalid(t *testing.T) {
	if _, err := newSendRouter(nil); err == nil {
		t.Fatal("空路由应被拒绝")
	}
	if _, err := newSendRouter(map[event.Channel]sender.Sender{event.ChLive: nil}); err == nil {
		t.Fatal("nil 发送器应被拒绝")
	}
	if _, err := newSendRouter(map[event.Channel]sender.Sender{"bad": &fakeSender{}}); err == nil {
		t.Fatal("非法通道名应被拒绝")
	}
}
