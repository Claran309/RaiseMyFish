package reasoning

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/brain/provider"
)

type scriptedToolLLM struct {
	responses []provider.ToolResponse
	errs      []error
	calls     int
	usage     Usage
	seen      [][]provider.Message
}

func (f *scriptedToolLLM) CompleteWithTools(_ context.Context, messages []provider.Message, _ []provider.ToolDefinition) (provider.ToolResponse, error) {
	f.seen = append(f.seen, append([]provider.Message(nil), messages...))
	f.usage.Calls++
	idx := f.calls
	f.calls++
	if idx < len(f.errs) && f.errs[idx] != nil {
		return provider.ToolResponse{}, f.errs[idx]
	}
	if idx >= len(f.responses) {
		return provider.ToolResponse{}, errors.New("没有脚本响应")
	}
	return f.responses[idx], nil
}

func (f *scriptedToolLLM) Usage() Usage { return f.usage }

func nativeTestTool(calls *[]string) Tool {
	return Tool{
		Name: ToolMemorySearch,
		Desc: "查记忆",
		Run: func(_ context.Context, args string) (string, error) {
			*calls = append(*calls, args)
			return "上次说想吃鱼", nil
		},
	}
}

func TestRunNativeToolCallThenFinal(t *testing.T) {
	var calls []string
	box, err := NewToolbox(nativeTestTool(&calls))
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedToolLLM{responses: []provider.ToolResponse{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Type: "function", Function: provider.ToolCallFunction{Name: ToolMemorySearch, Arguments: `{"args":"白饭"}`}}}},
		{Text: "记得，你说想吃鱼"},
	}}
	agent, err := NewNative(llm, box, Options{MaxSteps: 3, Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.RunNative(context.Background(), Input{System: "人设", Context: "资料", Question: "记得我吗"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "记得，你说想吃鱼" || got.Steps != 2 || len(got.Tools) != 1 || got.Tools[0] != ToolMemorySearch {
		t.Fatalf("结果错误：%+v", got)
	}
	if len(calls) != 1 || calls[0] != "白饭" || len(got.Evidence) != 1 || got.Evidence[0].Tool != ToolMemorySearch {
		t.Fatalf("工具执行/证据错误：calls=%v evidence=%+v", calls, got.Evidence)
	}
	// 第二轮 messages 必须包含 assistant(tool_calls) 与 tool 消息。
	last := llm.seen[len(llm.seen)-1]
	if len(last) != 4 || last[2].Role != "assistant" || len(last[2].ToolCalls) != 1 || last[3].Role != "tool" || last[3].ToolCallID != "c1" {
		t.Fatalf("tool 消息未回填：%+v", last)
	}
}

func TestRunNativeInvalidArgsBecomesObservation(t *testing.T) {
	var calls []string
	box, err := NewToolbox(nativeTestTool(&calls))
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedToolLLM{responses: []provider.ToolResponse{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Type: "function", Function: provider.ToolCallFunction{Name: ToolMemorySearch, Arguments: `{"args":123}`}}}},
		{Text: "参数不对，但鱼还是能答"},
	}}
	agent, err := NewNative(llm, box, Options{MaxSteps: 3, Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.RunNative(context.Background(), Input{System: "人设", Question: "问"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("参数错误的工具不应执行：%v", calls)
	}
	if got.Text != "参数不对，但鱼还是能答" || len(got.Tools) != 0 {
		t.Fatalf("参数错误后的收尾错误：%+v", got)
	}
}

func TestRunNativeDegradesOnModelError(t *testing.T) {
	var calls []string
	box, err := NewToolbox(nativeTestTool(&calls))
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedToolLLM{
		errs:      []error{errors.New("模型挂了"), nil},
		responses: []provider.ToolResponse{{}, {Text: "降级也能说话"}},
	}
	agent, err := NewNative(llm, box, Options{MaxSteps: 3, Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.RunNative(context.Background(), Input{System: "人设", Question: "问"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Degraded || got.Reason != DegradeLLMError || got.Text != "降级也能说话" {
		t.Fatalf("降级结果错误：%+v", got)
	}
}

func TestRunNativeStopsAtMaxSteps(t *testing.T) {
	var calls []string
	box, err := NewToolbox(nativeTestTool(&calls))
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedToolLLM{}
	for i := 0; i < 10; i++ {
		llm.responses = append(llm.responses, provider.ToolResponse{ToolCalls: []provider.ToolCall{{ID: "c", Type: "function", Function: provider.ToolCallFunction{Name: ToolMemorySearch, Arguments: `{"args":"x"}`}}}})
	}
	agent, err := NewNative(llm, box, Options{MaxSteps: 2, Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agent.RunNative(context.Background(), Input{System: "人设", Question: "问"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Degraded || got.Reason != DegradeSteps || len(calls) != 2 || len(got.Evidence) != 2 {
		t.Fatalf("步数预算错误：%+v calls=%v", got, calls)
	}
}

func TestRunNativePromptBudgetTrimsToolMessages(t *testing.T) {
	var calls []string
	box, err := NewToolbox(nativeTestTool(&calls))
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedToolLLM{responses: []provider.ToolResponse{
		{ToolCalls: []provider.ToolCall{{ID: "c1", Type: "function", Function: provider.ToolCallFunction{Name: ToolMemorySearch, Arguments: `{"args":"x"}`}}}},
		{Text: "好"},
	}}
	agent, err := NewNative(llm, box, Options{MaxSteps: 3, Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 预算只够第一条 user，不允许无限膨胀；至少要能完成或明确 budget 降级。
	got, err := agent.RunNative(context.Background(), Input{System: "人设", Question: "问", PromptBudget: 256})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text == "" && !got.Degraded {
		t.Fatalf("预算场景应有正文或降级：%+v", got)
	}
}
