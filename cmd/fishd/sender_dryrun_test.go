package main

import (
	"context"
	"testing"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// TestBuildSenderDryRunNeedsNoCredentials dry_run 不应要求凭据或 node 环境，
// 且三个通道都能返回成功回执。
func TestBuildSenderDryRunNeedsNoCredentials(t *testing.T) {
	cfg := config.Config{
		Channels: config.Channels{
			Live:    config.Channel{Enabled: true},
			Comment: config.Channel{Enabled: true},
			DM:      config.Channel{Enabled: true},
		},
		Sender: &config.Sender{
			NodePath: "node", ScriptPath: "x.mjs", HTTPTimeoutMS: 1000, DryRun: true,
		},
	}
	snd, whitelist, err := buildSender(cfg, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("dry_run 装配失败：%v", err)
	}
	if snd == nil || whitelist != nil {
		t.Fatalf("发送器/白名单不符：%v %v", snd, whitelist)
	}
	for _, ch := range []event.Channel{event.ChLive, event.ChComment, event.ChDM} {
		claim := event.Claim{
			Event: event.Event{
				ID: string(ch) + ":1:2", Channel: ch, Dir: event.DirIn, Session: "1",
				Text: "在吗", Private: ch == event.ChDM, Status: event.StatusReplying,
			},
			Lease: event.Lease{Owner: "w", Version: 1},
		}
		reply := sender.Reply{Text: "在的", Scope: memory.Scope{Channel: ch, Session: "1", Private: ch == event.ChDM}}
		res, err := snd.Send(context.Background(), claim, reply)
		if err != nil || !res.OK || !res.Verified {
			t.Fatalf("%s dry_run 失败：%+v %v", ch, res, err)
		}
	}
}

// TestBuildSenderDryRunWithoutChannels dry_run 但没有启用通道时明确报错。
func TestBuildSenderDryRunWithoutChannels(t *testing.T) {
	cfg := config.Config{Sender: &config.Sender{DryRun: true}}
	if _, _, err := buildSender(cfg, nil, nil, zap.NewNop()); err == nil {
		t.Fatal("无通道时 dry_run 被接受")
	}
}
