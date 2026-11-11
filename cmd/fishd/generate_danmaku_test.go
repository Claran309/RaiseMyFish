package main

// 本文件是批 D（#79 send_danmaku 工具）的生产装配级验收：
// 用脚本化模型与假发送器确认「一轮连发多条弹幕 → 不再发普通回复 → 输入事件正常结清」。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

// captureDanmakuSender 记录工具实际发出的弹幕正文。
type captureDanmakuSender struct {
	mu    sync.Mutex
	texts []string
}

func (c *captureDanmakuSender) SendDanmaku(_ context.Context, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = append(c.texts, text)
	return nil
}

func (c *captureDanmakuSender) sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.texts...)
}

// danmakuTestClock 每次取时间前进一秒，避开工具默认的发送间隔。
func danmakuTestClock() func() time.Time {
	now := time.Unix(2000, 0)
	return func() time.Time {
		now = now.Add(time.Second)
		return now
	}
}

func danmakuWorkers(t *testing.T, llm reasoning.LLM, box *reasoning.Toolbox, bus *fakeBus, snd *fakeSender, fallback brain.Brain) scheduler.Workers {
	t.Helper()
	factory := func(memory.Scope, string, bool, event.Channel) (*reasoning.Agent, error) {
		return reasoning.New(llm, box, reasoning.Options{MaxSteps: 3, Timeout: 2 * time.Second}, zap.NewNop())
	}
	workers, err := buildConsumeWorkers(
		scheduler.ChannelPolicy{}, snapshotTestPack(),
		map[event.Channel]brain.Brain{event.ChLive: fallback},
		snd, bus, time.Second, nil, nil, nil, "", nil, nil,
		&reactRuntime{factory: factory, defaultReact: true}, nil, zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	return workers
}

func TestGenerateUsesDanmakuToolWithoutNormalReply(t *testing.T) {
	const id = "live:30308220:danmaku"
	dm := &captureDanmakuSender{}
	box, err := reasoning.BuildDanmakuTool(reasoning.DanmakuDeps{Sender: dm, Now: danmakuTestClock()})
	if err != nil {
		t.Fatalf("装配弹幕工具失败：%v", err)
	}
	llm := &scriptedStepLLM{replies: []string{
		`{"thought":"先吐槽","action":"send_danmaku","args":"{\"text\":\"第一条：哈哈哈\"}"}`,
		`{"thought":"补一句","action":"send_danmaku","args":"{\"text\":\"第二条：再来\"}"}`,
		`{"thought":"够了","action":"final","final":"这段不该再发出去"}`,
	}}
	fallback := &fakeBrain{reply: brain.Reply{Text: "单步回复不该出现"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	bus := newFakeBus(id)
	workers := danmakuWorkers(t, llm, box, bus, snd, fallback)

	got, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)})
	if err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if !got.Replied {
		t.Errorf("Result.Replied 应为 true：%+v", got)
	}
	if texts := dm.sent(); len(texts) != 2 || texts[0] != "第一条：哈哈哈" || texts[1] != "第二条：再来" {
		t.Fatalf("工具实际发送不符：%+v", texts)
	}
	if snd.sendCalls != 0 {
		t.Errorf("已发弹幕后不应再走普通发送，实际调用 %d 次", snd.sendCalls)
	}
	if fallback.calls != 0 {
		t.Errorf("ReAct 成功时不应回落单步，实际调用 %d 次", fallback.calls)
	}
	if status := bus.finalStatus(id); status != event.StatusDone {
		t.Errorf("输入事件最终状态 = %s，期望 done", status)
	}
	if tos := bus.markTos(); len(tos) != 3 || tos[0] != event.StatusReplying || tos[1] != event.StatusSent || tos[2] != event.StatusDone {
		t.Errorf("状态链应为 replying→sent→done，实际 %+v", tos)
	}
}

func TestGenerateFallsBackWhenDanmakuToolFails(t *testing.T) {
	const id = "live:30308220:danmaku-fail"
	failing := &failingDanmakuSender{}
	box, err := reasoning.BuildDanmakuTool(reasoning.DanmakuDeps{Sender: failing, Now: danmakuTestClock()})
	if err != nil {
		t.Fatalf("装配弹幕工具失败：%v", err)
	}
	llm := &scriptedStepLLM{replies: []string{
		`{"thought":"试发","action":"send_danmaku","args":"{\"text\":\"发不出去\"}"}`,
		`{"thought":"换普通回复","action":"final","final":"那就正常回一句"}`,
	}}
	fallback := &fakeBrain{reply: brain.Reply{Text: "单步兜底"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	bus := newFakeBus(id)
	workers := danmakuWorkers(t, llm, box, bus, snd, fallback)

	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: liveClaim(id), Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	if snd.sendCalls != 1 || snd.gotReply.Text != "那就正常回一句" {
		t.Fatalf("工具发送失败后应走普通回复，实际 %+v", snd.gotReply)
	}
	if fallback.calls != 0 {
		t.Errorf("ReAct 有 final 正文时不应回落单步：%d", fallback.calls)
	}
	if status := bus.finalStatus(id); status != event.StatusDone {
		t.Errorf("输入事件最终状态 = %s，期望 done", status)
	}
}

// failingDanmakuSender 模拟平台拒收。
type failingDanmakuSender struct{}

func (failingDanmakuSender) SendDanmaku(context.Context, string) error {
	return errors.New("平台拒收")
}
