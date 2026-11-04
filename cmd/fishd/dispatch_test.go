package main

// 本文件测「发送走通道」：注册表优先、取不到回落直连 sender。

import (
	"context"
	"testing"

	"RaiseMyFish/internal/channel"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

// stubChannel 是只用于路由断言的通道。
type stubChannel struct {
	name     event.Channel
	sends    int
	resumes  int
	lastText string
}

func (c *stubChannel) Name() event.Channel { return c.name }

func (c *stubChannel) Collect(context.Context, event.Bus) (func(), error) {
	return func() {}, nil
}

func (c *stubChannel) Send(_ context.Context, _ event.Claim, r channel.Reply) (channel.Result, error) {
	c.sends++
	c.lastText = r.Text
	return channel.Result{OK: true, MsgID: "via-channel"}, nil
}

func (c *stubChannel) Resume(context.Context, event.Claim) (channel.Result, error) {
	c.resumes++
	return channel.Result{OK: true, MsgID: "resumed-by-channel"}, nil
}

// stubSender 是兜底的直连 sender。
type stubSender struct{ sends, resumes int }

func (s *stubSender) Name() string { return "stub" }
func (s *stubSender) Send(context.Context, event.Claim, sender.Reply) (sender.Result, error) {
	s.sends++
	return sender.Result{OK: true, MsgID: "via-sender"}, nil
}
func (s *stubSender) Resume(context.Context, event.Claim) (sender.Result, error) {
	s.resumes++
	return sender.Result{OK: true}, nil
}

// TestChannelSetPrefersRegistry 注册表命中时走通道，sender 不被调用。
func TestChannelSetPrefersRegistry(t *testing.T) {
	reg := channel.NewRegistry()
	ch := &stubChannel{name: event.ChLive}
	if err := reg.Register(ch); err != nil {
		t.Fatal(err)
	}
	set := &channelSet{}
	set.Set(reg)

	fallback := &stubSender{}
	disp, ok := set.resolve(event.ChLive, fallback)
	if !ok {
		t.Fatal("应解析出通道")
	}
	res, err := disp.Send(context.Background(), event.Claim{}, sender.Reply{Text: "你好"})
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if ch.sends != 1 || ch.lastText != "你好" {
		t.Errorf("通道未收到发送：%+v", ch)
	}
	if fallback.sends != 0 {
		t.Errorf("命中通道时不应回落 sender：%d", fallback.sends)
	}
	if res.MsgID != "via-channel" {
		t.Errorf("结果不是通道返回的：%+v", res)
	}

	// Resume 同样走通道
	res, err = disp.Resume(context.Background(), event.Claim{})
	if err != nil || ch.resumes != 1 || fallback.resumes != 0 {
		t.Fatalf("Resume 未走通道：%+v / %v calls=%d fallback=%d", res, err, ch.resumes, fallback.resumes)
	}
}

// TestChannelSetFallsBackToSender 通道未注册时回落直连 sender。
func TestChannelSetFallsBackToSender(t *testing.T) {
	reg := channel.NewRegistry()
	set := &channelSet{}
	set.Set(reg)

	fallback := &stubSender{}
	disp, ok := set.resolve(event.ChDM, fallback)
	if !ok {
		t.Fatal("应回落 sender")
	}
	res, err := disp.Send(context.Background(), event.Claim{}, sender.Reply{Text: "私信"})
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if fallback.sends != 1 || res.MsgID != "via-sender" {
		t.Errorf("未回落到 sender：%+v", res)
	}
	if _, err := disp.Resume(context.Background(), event.Claim{}); err != nil || fallback.resumes != 1 {
		t.Errorf("Resume 未回落：%v / %d", err, fallback.resumes)
	}
}

// TestChannelSetWithoutRegistryOrSender 两者都没有时明确失败（不 panic）。
func TestChannelSetWithoutRegistryOrSender(t *testing.T) {
	set := &channelSet{} // 未 Set
	if _, ok := set.resolve(event.ChLive, nil); ok {
		t.Error("无通道无 sender 应解析失败")
	}
	var nilSet *channelSet
	if _, ok := nilSet.resolve(event.ChLive, nil); ok {
		t.Error("nil 集合应安全失败")
	}
	// 有 sender 时 nil 集合也能兜底
	snd := &stubSender{}
	disp, ok := nilSet.resolve(event.ChLive, snd)
	if !ok {
		t.Fatal("有 sender 时应兜底")
	}
	if _, err := disp.Send(context.Background(), event.Claim{}, sender.Reply{Text: "x"}); err != nil || snd.sends != 1 {
		t.Errorf("兜底发送失败：%v", err)
	}
}
