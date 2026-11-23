package main

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// TestGenerateAppliesChannelPromptBudget 通道级 prompt 预算进入 Brain 输入，
// 且不影响没有配置预算的通道。
func TestGenerateAppliesChannelPromptBudget(t *testing.T) {
	const id = "comment:123:77"
	engine := &fakeBrain{reply: brain.Reply{Text: "收到"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	pack := snapshotTestPack()
	pack.Contexts["comment"] = "你在B站评论区陪观众聊天"
	workers, err := buildConsumeWorkers(
		scheduler.ChannelPolicy{}, pack,
		map[event.Channel]brain.Brain{event.ChComment: engine},
		snd, newFakeBus(id), time.Second,
		nil, nil, nil, "", nil, nil, nil, nil, zap.NewNop(),
		withPromptBudgets(map[event.Channel]int{event.ChComment: 16000}),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: commentClaim(nil), Task: commentTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if engine.gotInput.MaxPromptTokens != 16000 {
		t.Fatalf("预算未注入：%d", engine.gotInput.MaxPromptTokens)
	}
}

// TestPromptBudgetsFromConfig 只投影显式配置的通道，0 值不出现。
func TestPromptBudgetsFromConfig(t *testing.T) {
	cfg := config.Config{Scheduler: &config.Scheduler{Channels: map[event.Channel]config.ChannelPolicy{
		event.ChComment: {MaxPromptTokens: 16000},
		event.ChDM:      {MaxPromptTokens: 0},
		event.ChLive:    {MaxPromptTokens: 32000},
	}}}
	got := promptBudgetsFrom(cfg)
	if len(got) != 2 || got[event.ChComment] != 16000 || got[event.ChLive] != 32000 {
		t.Fatalf("预算投影错误：%+v", got)
	}
	if promptBudgetsFrom(config.Config{}) != nil {
		t.Fatal("未启用调度时应返回 nil")
	}
}
