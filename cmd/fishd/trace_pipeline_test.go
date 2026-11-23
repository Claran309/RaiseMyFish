package main

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/trace"
	"go.uber.org/zap"
)

// TestGeneratePipelineWritesTraceTree 一条事件从 collect 到 deliver 应落在同一 trace，
// 且子 span 挂在对应阶段下；测试只断言结构与元数据，不关心正文。
func TestGeneratePipelineWritesTraceTree(t *testing.T) {
	local, err := trace.NewLocal(1000, "")
	if err != nil {
		t.Fatal(err)
	}
	ev := snapshotTestEvent("在吗")
	ev.ID = "live:30308220:trace-1"
	bus := newFakeBus(ev.ID)
	engine := &fakeBrain{reply: brain.Reply{Text: "在的"}}
	snd := &fakeSender{sendResult: sender.Result{OK: true, Verified: true}}
	workers, err := buildConsumeWorkers(scheduler.ChannelPolicy{}, persona.Pack{}, map[event.Channel]brain.Brain{event.ChLive: engine}, snd, bus, time.Second, nil, nil, nil, "", nil, nil, nil, nil, zap.NewNop(), withTracer(local))
	if err != nil {
		t.Fatal(err)
	}
	// 模拟采集阶段：真实运行由 tracedBus.Publish 写 collect。
	_, collectSpan := local.StartEvent(context.Background(), ev.ID, "collect", trace.A("channel", "live"))
	local.End(collectSpan, nil)
	// 调度器在 launch 时注入 generate 阶段 span；这里按同一路径构造上下文。
	genCtx, genSpan := local.StartEvent(context.Background(), ev.ID, "generate", trace.A("channel", "live"))
	if _, err := workers.Generate(genCtx, scheduler.Work{Claim: event.Claim{Event: ev, Stage: event.StageGenerate}, Task: liveTask(ev.ID)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
	local.End(genSpan, nil)
	detail := local.Get(trace.TraceID(ev.ID))
	names := make(map[string]bool, len(detail.Spans))
	for _, span := range detail.Spans {
		names[span.Name] = true
	}
	for _, want := range []string{"collect", "generate", "context.build", "llm.step", "deliver"} {
		if !names[want] {
			t.Fatalf("缺少 span %q：%+v", want, names)
		}
	}
	if detail.EventID != ev.ID || detail.SpanCount != 5 {
		t.Fatalf("trace 聚合错误：%+v", detail.TraceSummary)
	}
}
