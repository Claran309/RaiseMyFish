package reasoning

// 本文件测 ReAct 循环：工具回填、步数耗尽降级、超时降级、解析失败降级。
// 全部用 mock LLM，不调真实模型。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// scriptedLLM 按顺序回放预设输出，并记录每次调用。
type scriptedLLM struct {
	mu      sync.Mutex
	replies []string
	errs    map[int]error
	calls   int
	prompts []string
	// block 在返回前调用，用来模拟卡住（超时用例）。
	block func(ctx context.Context) error
}

func (m *scriptedLLM) Complete(ctx context.Context, system, user string) (string, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.mu.Unlock()
	m.prompts = append(m.prompts, system+"\n---\n"+user)

	if m.block != nil {
		if err := m.block(ctx); err != nil {
			return "", err
		}
	}
	if err, ok := m.errs[idx]; ok && err != nil {
		return "", err
	}
	if idx < len(m.replies) {
		return m.replies[idx], nil
	}
	return "", errors.New("脚本用尽")
}

// countingTool 记录调用次数并返回固定观察。
type countingTool struct {
	name  string
	out   string
	err   error
	calls int
	args  []string
}

func (c *countingTool) tool() Tool {
	return Tool{Name: c.name, Desc: "测试工具", Run: func(_ context.Context, args string) (string, error) {
		c.calls++
		c.args = append(c.args, args)
		if c.err != nil {
			return "", c.err
		}
		return c.out, nil
	}}
}

func newTestAgent(t *testing.T, llm LLM, opts Options, tools ...Tool) *Agent {
	t.Helper()
	box, err := NewToolbox(tools...)
	if err != nil {
		t.Fatalf("装配工具失败：%v", err)
	}
	agent, err := New(llm, box, opts, zap.NewNop())
	if err != nil {
		t.Fatalf("装配 Agent 失败：%v", err)
	}
	return agent
}

// TestRunToolThenFinal 模型先调工具、拿到结果后再给 final。
func TestRunToolThenFinal(t *testing.T) {
	tool := &countingTool{name: ToolMemorySearch, out: "上次说想吃鱼"}
	llm := &scriptedLLM{replies: []string{
		`{"thought":"要查上次说过什么","action":"memory_search","args":"上次"}`,
		`{"thought":"够了","action":"final","final":"记得，你说想吃鱼"}`,
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())

	got, err := agent.Run(context.Background(), Input{System: "你是大肥鱼", Question: "上次我说想吃啥"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if got.Text != "记得，你说想吃鱼" {
		t.Errorf("回复 = %q", got.Text)
	}
	if tool.calls != 1 || len(tool.args) != 1 || tool.args[0] != "上次" {
		t.Errorf("工具调用不符：calls=%d args=%v", tool.calls, tool.args)
	}
	if got.Steps != 2 || got.Degraded {
		t.Errorf("步数/降级不符：steps=%d degraded=%v", got.Steps, got.Degraded)
	}
	if len(got.Tools) != 1 || got.Tools[0] != ToolMemorySearch {
		t.Errorf("工具记录不符：%v", got.Tools)
	}
	// 第二轮 prompt 必须带上观察结果，否则模型无从利用
	if !strings.Contains(llm.prompts[1], "上次说想吃鱼") {
		t.Errorf("第二轮 prompt 未包含观察：%s", llm.prompts[1])
	}
}

// TestStepsExhaustedDegrades 一直调工具不收尾 → 步数耗尽后降级生成。
func TestStepsExhaustedDegrades(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "弹幕一 · 弹幕二"}
	llm := &scriptedLLM{replies: []string{
		`{"thought":"再查一次","action":"history","args":"10"}`,
		`{"thought":"再查一次","action":"history","args":"10"}`,
		`{"thought":"再查一次","action":"history","args":"10"}`,
		`直接回答：刚才在聊吃的`, // 降级那次
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())

	got, err := agent.Run(context.Background(), Input{Question: "刚聊什么了"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if !got.Degraded || got.Reason != DegradeSteps {
		t.Errorf("应为步数耗尽降级，实际 degraded=%v reason=%q", got.Degraded, got.Reason)
	}
	if got.Text != "直接回答：刚才在聊吃的" {
		t.Errorf("降级回复 = %q", got.Text)
	}
	if tool.calls != 3 {
		t.Errorf("工具调用 %d 次，期望 3（等于 MaxSteps）", tool.calls)
	}
}

// TestTimeoutDegrades 模型卡住 → 总超时后降级，且不再多调模型。
func TestTimeoutDegrades(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "x"}
	llm := &scriptedLLM{block: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 50 * time.Millisecond}, tool.tool())

	started := time.Now()
	got, err := agent.Run(context.Background(), Input{Question: "问点啥"})
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("超时应降级而不是报错：%v", err)
	}
	if !got.Degraded || got.Reason != DegradeTimeout {
		t.Errorf("应为超时降级，实际 degraded=%v reason=%q", got.Degraded, got.Reason)
	}
	if got.Text != "" {
		t.Errorf("预算耗尽时不应再生成文本，实际 %q", got.Text)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("降级耗时 %v，超出预算太多", elapsed)
	}
	if llm.calls != 1 {
		t.Errorf("模型调用 %d 次，期望 1（超时后不再重试）", llm.calls)
	}
}

// TestParseFailureDegrades 模型输出不是合法 JSON → 解析失败降级。
func TestParseFailureDegrades(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "x"}
	llm := &scriptedLLM{replies: []string{
		`我觉得应该先查一下历史（没有 JSON）`,
		`直接回答：不知道`,
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())

	got, err := agent.Run(context.Background(), Input{Question: "随便问问"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if !got.Degraded || got.Reason != DegradeParse {
		t.Errorf("应为解析失败降级，实际 degraded=%v reason=%q", got.Degraded, got.Reason)
	}
	if got.Text != "直接回答：不知道" {
		t.Errorf("降级回复 = %q", got.Text)
	}
	if tool.calls != 0 {
		t.Errorf("解析失败不应执行工具，实际 %d 次", tool.calls)
	}
}

// TestUnknownToolBecomesObservation 调了不存在的工具 → 变成一条观察，循环继续。
func TestUnknownToolBecomesObservation(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "弹幕一"}
	llm := &scriptedLLM{replies: []string{
		`{"thought":"试试","action":"no_such_tool","args":""}`,
		`{"thought":"换一个","action":"history","args":"5"}`,
		`{"thought":"够了","action":"final","final":"刚在聊吃的"}`,
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())

	got, err := agent.Run(context.Background(), Input{Question: "聊啥了"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if got.Text != "刚在聊吃的" || got.Degraded {
		t.Errorf("未按预期收尾：%+v", got)
	}
	if !strings.Contains(llm.prompts[1], "工具失败") {
		t.Errorf("未知工具应变成观察喂回去：%s", llm.prompts[1])
	}
}

// TestLLMErrorDegrades 模型直接报错 → 降级原因记为 llm_error。
func TestLLMErrorDegrades(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "x"}
	llm := &scriptedLLM{errs: map[int]error{0: errors.New("provider down"), 1: errors.New("provider down")}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())

	got, err := agent.Run(context.Background(), Input{Question: "问"})
	if err != nil {
		t.Fatalf("模型报错应降级而不是上抛：%v", err)
	}
	if !got.Degraded || got.Reason != DegradeLLMError {
		t.Errorf("应为 llm_error 降级，实际 %+v", got)
	}
}

// TestParseStepToleratesFences 容忍代码块包裹与前后废话。
func TestParseStepToleratesFences(t *testing.T) {
	cases := []string{
		"```json\n{\"thought\":\"t\",\"action\":\"final\",\"final\":\"好\"}\n```",
		"先说一句废话 {\"thought\":\"t\",\"action\":\"final\",\"final\":\"好\"} 尾巴",
		`{"thought":"t","action":"final","final":"好"}`,
	}
	for _, raw := range cases {
		step, err := ParseStep(raw)
		if err != nil {
			t.Fatalf("解析失败 %q：%v", raw, err)
		}
		if step.Action != "final" || step.Final != "好" {
			t.Errorf("解析结果不符 %q：%+v", raw, step)
		}
	}

	if _, err := ParseStep(`{"thought":"没有 action"}`); err == nil {
		t.Error("缺少 action 应当报错")
	}
	if _, err := ParseStep(""); err == nil {
		t.Error("空输出应当报错")
	}
}

// TestNewRejectsBadOptions 预算参数非法时构造失败。
func TestNewRejectsBadOptions(t *testing.T) {
	box, err := NewToolbox(Tool{Name: "t", Desc: "d", Run: func(context.Context, string) (string, error) { return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedLLM{}
	if _, err := New(llm, box, Options{MaxSteps: 0, Timeout: time.Second}, zap.NewNop()); err == nil {
		t.Error("步数 0 应报错")
	}
	if _, err := New(llm, box, Options{MaxSteps: 3, Timeout: 0}, zap.NewNop()); err == nil {
		t.Error("超时 0 应报错")
	}
	if _, err := New(nil, box, Options{MaxSteps: 3, Timeout: time.Second}, zap.NewNop()); err == nil {
		t.Error("缺少模型应报错")
	}
	if _, err := New(llm, nil, Options{MaxSteps: 3, Timeout: time.Second}, zap.NewNop()); err == nil {
		t.Error("缺少工具应报错")
	}
}

// TestDefaultOptions 默认预算与派发单一致：3 步 / 8 秒。
func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions()
	if o.MaxSteps != 3 || o.Timeout != 8*time.Second {
		t.Errorf("默认预算不符：%+v", o)
	}
}
