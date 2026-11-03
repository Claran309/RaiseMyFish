package main

// 发送路由：Generate/Deliver 只依赖一个 sender.Sender，评论通道接入后由装配层
// 按 claim.Event.Channel 分发，避免改动调度器或 Brain 的既有语义。

import (
	"context"
	"errors"
	"fmt"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
)

type sendRouter struct {
	routes map[event.Channel]sender.Sender
}

var _ sender.Sender = (*sendRouter)(nil)

func newSendRouter(routes map[event.Channel]sender.Sender) (*sendRouter, error) {
	if len(routes) == 0 {
		return nil, errors.New("没有已启用的发送通道")
	}
	copied := make(map[event.Channel]sender.Sender, len(routes))
	for ch, snd := range routes {
		if !ch.Valid() || snd == nil {
			return nil, fmt.Errorf("发送通道 %s 未装配发送器", ch)
		}
		copied[ch] = snd
	}
	return &sendRouter{routes: copied}, nil
}

func (r *sendRouter) Name() string { return "channel-router" }

func (r *sendRouter) route(ch event.Channel) (sender.Sender, error) {
	snd, ok := r.routes[ch]
	if !ok || snd == nil {
		return nil, &sender.Error{Kind: sender.KindSkipped, Cause: fmt.Errorf("通道 %s 未装配发送器", ch)}
	}
	return snd, nil
}

func (r *sendRouter) Send(ctx context.Context, c event.Claim, p sender.Reply) (sender.Result, error) {
	snd, err := r.route(c.Event.Channel)
	if err != nil {
		return sender.Result{}, err
	}
	return snd.Send(ctx, c, p)
}

func (r *sendRouter) Resume(ctx context.Context, c event.Claim) (sender.Result, error) {
	snd, err := r.route(c.Event.Channel)
	if err != nil {
		return sender.Result{}, err
	}
	return snd.Resume(ctx, c)
}
