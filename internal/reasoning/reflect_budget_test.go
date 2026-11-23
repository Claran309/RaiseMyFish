package reasoning

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// stepScriptLLM 按脚本返回文本 ReAct 的 JSON 步进，用于精确控制「工具步」与「反思」。
type stepScriptLLM struct {
	steps []string
	calls int
}

func (f *stepScriptLLM) Complete(context.Context, string, string) (string, error) {
	f.calls++
	idx := f.calls - 1
	if idx >= len(f.steps) {
		idx = len(f.steps) - 1
	}
	return f.steps[idx], nil
}

func toolStep(name, args string) string {
	data, _ := json.Marshal(Step{Thought: "先查", Action: name, Args: args})
	return string(data)
}

func finalStep(text string) string {
	data, _ := json.Marshal(Step{Action: "final", Final: text})
	return string(data)
}

// TestReflectRunsAfterToolStepsExhausted 工具步数用满后，Reflection 仍按独立预算执行。
func TestReflectRunsAfterToolStepsExhausted(t *testing.T) {
	box, err := NewToolbox(Tool{Name: ToolHistory, Desc: "历史", Run: func(context.Context, string) (string, error) { return "弹幕", nil }})
	if err != nil {
		t.Fatal(err)
	}
	// 3 个工具步用满 MaxToolSteps，第 4 次调用是 Reflection。
	llm := &stepScriptLLM{steps: []string{
		toolStep(ToolHistory, "1"), toolStep(ToolHistory, "2"), toolStep(ToolHistory, "3"),
		"已经回答好了",
	}}
	agent, err := New(llm, box, Options{MaxSteps: 4, MaxToolSteps: 3, ReflectBudget: 1, Timeout: 2 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.Run(context.Background(), Input{System: "人设", Question: "问", Reflect: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ReflectCalled || got.Text != "已经回答好了" {
		t.Fatalf("工具步数用满后未反思：%+v calls=%d", got, llm.calls)
	}
	// 工具步用满后进入降级正文，再追加一次独立反思：3 工具步 + 1 降级 + 1 反思。
	if llm.calls != 5 || got.Steps != 5 {
		t.Fatalf("调用次数=%d steps=%d，期望 3+1+1", llm.calls, got.Steps)
	}
}

// TestReflectBudgetNegativeDisables 显式关闭时不发起反思调用。
func TestReflectBudgetNegativeDisables(t *testing.T) {
	box, err := NewToolbox(Tool{Name: ToolHistory, Desc: "历史", Run: func(context.Context, string) (string, error) { return "x", nil }})
	if err != nil {
		t.Fatal(err)
	}
	llm := &stepScriptLLM{steps: []string{finalStep("草稿")}}
	agent, err := New(llm, box, Options{MaxSteps: 3, ReflectBudget: -1, Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.Run(context.Background(), Input{System: "人设", Question: "问", Reflect: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.ReflectCalled || got.ReflectSkippedReason != "disabled" || llm.calls != 1 {
		t.Fatalf("关闭反思未生效：%+v calls=%d", got, llm.calls)
	}
}

// TestReflectOffRecordsReason Reflect=false 时也记录原因，便于排查「为什么没反思」。
func TestReflectOffRecordsReason(t *testing.T) {
	box, err := NewToolbox(Tool{Name: ToolHistory, Desc: "历史", Run: func(context.Context, string) (string, error) { return "x", nil }})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := New(&stepScriptLLM{steps: []string{finalStep("草稿")}}, box, Options{MaxSteps: 3, Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.Run(context.Background(), Input{System: "人设", Question: "问"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ReflectCalled || got.ReflectSkippedReason != "disabled" {
		t.Fatalf("未反思原因缺失：%+v", got)
	}
}
