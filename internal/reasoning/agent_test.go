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
	// blockAt 非 nil 时只在指定调用序号（0 起）阻塞，用于「先查工具再卡住」。
	blockAt func(idx int) bool
}

func (m *scriptedLLM) Complete(ctx context.Context, system, user string) (string, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.mu.Unlock()
	m.prompts = append(m.prompts, system+"\n---\n"+user)

	if m.block != nil && (m.blockAt == nil || m.blockAt(idx)) {
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

// TestRunRecordsSentDanmaku 单轮可连发多条；已发弹幕记进 Result，且不算查证证据。
func TestRunRecordsSentDanmaku(t *testing.T) {
	sender := &fakeDanmakuSender{}
	box, err := BuildDanmakuTool(DanmakuDeps{Sender: sender, Now: steppingClock()})
	if err != nil {
		t.Fatalf("装配弹幕工具失败：%v", err)
	}
	llm := &scriptedLLM{replies: []string{
		`{"thought":"先吐槽","action":"send_danmaku","args":"{\"text\":\"第一条\"}"}`,
		`{"thought":"补一句","action":"send_danmaku","args":"{\"text\":\"第二条\"}"}`,
		`{"thought":"够了","action":"final","final":"收尾"}`,
	}}
	agent, err := New(llm, box, DefaultOptions(), zap.NewNop())
	if err != nil {
		t.Fatalf("装配 Agent 失败：%v", err)
	}
	res, err := agent.Run(context.Background(), Input{Question: "在吗"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if len(res.Danmaku) != 2 || res.Danmaku[0] != "第一条" || res.Danmaku[1] != "第二条" {
		t.Fatalf("Result.Danmaku 不符：%+v", res.Danmaku)
	}
	if res.Text != "收尾" {
		t.Errorf("final 正文应保留：%q", res.Text)
	}
	if len(res.Evidence) != 0 {
		t.Errorf("发送弹幕不是查证，不应进证据：%+v", res.Evidence)
	}
	if got := sender.sent(); len(got) != 2 {
		t.Errorf("实际发送 %d 条，期望 2：%+v", len(got), got)
	}
}

// TestRunDegradeKeepsSentDanmaku 降级不能抹掉已经发出去的弹幕。
func TestRunDegradeKeepsSentDanmaku(t *testing.T) {
	sender := &fakeDanmakuSender{}
	box, err := BuildDanmakuTool(DanmakuDeps{Sender: sender, Now: steppingClock()})
	if err != nil {
		t.Fatalf("装配弹幕工具失败：%v", err)
	}
	llm := &scriptedLLM{
		replies: []string{`{"thought":"先发","action":"send_danmaku","args":"{\"text\":\"已发的那条\"}"}`},
		errs:    map[int]error{1: errors.New("模型挂了")},
	}
	agent, err := New(llm, box, Options{MaxSteps: 2, Timeout: time.Second}, zap.NewNop())
	if err != nil {
		t.Fatalf("装配 Agent 失败：%v", err)
	}
	res, err := agent.Run(context.Background(), Input{Question: "在吗"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if !res.Degraded || res.Reason != DegradeLLMError {
		t.Fatalf("应走模型错误降级：%+v", res)
	}
	if len(res.Danmaku) != 1 || res.Danmaku[0] != "已发的那条" {
		t.Fatalf("降级后仍应保留已发弹幕：%+v", res.Danmaku)
	}
}

func TestSystemPromptDistinguishesWhoIsWho(t *testing.T) {
	tool := &countingTool{name: ToolViewerSearch, out: "档案"}
	agent := newTestAgent(t, &scriptedLLM{}, DefaultOptions(), tool.tool())
	prompt := agent.systemPrompt(Input{System: "人设"})
	for _, want := range []string{
		"「XX 是谁」不一定是在问观众",
		"current_event.question_subject",
		"kind=room_candidate",
		"kind=unresolved",
		"viewer_search",
		"「你是谁」才是问你自己",
		"memory_search 的 args 传昵称或关键词",
		"corrections",
		"搜索状态：已完成（已联网）",
		"正确示例",
		"错误示例",
		"args 必须是字符串",
		"第一个字符必须是 {",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("ReAct system prompt 缺少规则 %q：%s", want, prompt)
		}
	}
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
	if len(got.Evidence) != 1 || got.Evidence[0].Tool != ToolMemorySearch || !strings.Contains(got.Evidence[0].Result, "想吃鱼") {
		t.Errorf("成功工具结果应进证据：%+v", got.Evidence)
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

// TestTimeoutAfterToolKeepsEvidence B2/N03 验收：
// 工具已经查到结果、随后模型卡住超时，降级结果仍携带可复用证据（不含思维文本）。
func TestTimeoutAfterToolKeepsEvidence(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "刚才在聊吃鱼"}
	llm := &scriptedLLM{
		replies: []string{`{"thought":"查历史","action":"history","args":"5"}`},
		block: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		blockAt: func(idx int) bool { return idx >= 1 },
	}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 50 * time.Millisecond}, tool.tool())

	got, err := agent.Run(context.Background(), Input{Question: "刚才聊什么了"})
	if err != nil {
		t.Fatalf("超时应降级而不是报错：%v", err)
	}
	if !got.Degraded || got.Reason != DegradeTimeout {
		t.Fatalf("应为超时降级，实际 degraded=%v reason=%q", got.Degraded, got.Reason)
	}
	if got.Text != "" {
		t.Errorf("超时后不应再有正文，实际 %q", got.Text)
	}
	if tool.calls != 1 {
		t.Fatalf("工具调用 %d 次，期望 1", tool.calls)
	}
	if len(got.Evidence) != 1 {
		t.Fatalf("超时降级必须保留已查到的证据：%+v", got.Evidence)
	}
	if ev := got.Evidence[0]; ev.Tool != ToolHistory || ev.Args != "5" || !strings.Contains(ev.Result, "吃鱼") {
		t.Errorf("证据不符：%+v", ev)
	}
	for _, text := range []string{"查历史", "thought"} {
		if strings.Contains(got.Evidence[0].Result, text) {
			t.Errorf("证据不应包含思维文本：%+v", got.Evidence[0])
		}
	}
}

// TestParseFailureUsesPlainText 模型直接给自然语言（没按 JSON 协议）→ 当作最终回复，
// 不再多花一次降级调用，回复仍是正常文本。
func TestParseFailureUsesPlainText(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "x"}
	llm := &scriptedLLM{replies: []string{
		`我觉得应该先查一下历史（没有 JSON）`,
		`不应该走到这一步`,
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())

	got, err := agent.Run(context.Background(), Input{Question: "随便问问"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if got.Text != "我觉得应该先查一下历史（没有 JSON）" {
		t.Errorf("自然语言应直接作为回复：%q", got.Text)
	}
	if got.Degraded || got.Reason != "" {
		t.Errorf("自然语言收尾不应记为降级：%+v", got)
	}
	if llm.calls != 1 {
		t.Errorf("不应再发起降级调用，实际 %d 次", llm.calls)
	}
	if tool.calls != 0 {
		t.Errorf("解析失败不应执行工具，实际 %d 次", tool.calls)
	}
}

// TestParseFailureInternalJSONDegrades JSON 形状/含内部字段的输出不能当正文：
// 走降级调用，降级提示要求正常文本，最终回复可用。
func TestParseFailureInternalJSONDegrades(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "x"}
	llm := &scriptedLLM{replies: []string{
		`{"thought":"内部推理","action":"history","args":"10"`, // 截断 JSON，缺收尾括号
		`在吃面包`,
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())

	got, err := agent.Run(context.Background(), Input{Question: "问"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if !got.Degraded || got.Reason != DegradeParse {
		t.Errorf("内部 JSON 应走 parse 降级：%+v", got)
	}
	if got.Text != "在吃面包" {
		t.Errorf("降级回复应为正常文本：%q", got.Text)
	}
	if tool.calls != 0 {
		t.Errorf("解析失败不应执行工具，实际 %d 次", tool.calls)
	}
	// 降级提示不能再要求 ReAct JSON 协议，否则模型仍可能回协议块。
	last := llm.prompts[len(llm.prompts)-1]
	if !strings.Contains(last, "不要 JSON") || strings.Contains(last, "每一步只输出一个 JSON 对象") {
		t.Errorf("降级提示应改为正常文本要求：%s", last)
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
	// 未知工具失败只进观察；之后成功的 history 才是证据。
	if len(got.Evidence) != 1 || got.Evidence[0].Tool != ToolHistory || got.Evidence[0].Result != "弹幕一" {
		t.Errorf("证据应只保留成功工具：%+v", got.Evidence)
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
		// 尾巴里带 } 也不能把截取带到最后一个括号
		`{"thought":"t","action":"final","final":"好"} 后面还有 } 符号`,
		// 代码块前面有废话
		"说明：```JSON\n{\"thought\":\"t\",\"action\":\"final\",\"final\":\"好\"}\n```",
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
	if _, err := ParseStep(`{"thought":"没闭合","action":"final","final":"好"`); err == nil {
		t.Error("残缺 JSON 不应靠猜闭合来解析")
	}
}

// TestParseStepRepairsCommonFormats 常见格式问题要修好，而不是降级。
func TestParseStepRepairsCommonFormats(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Step
	}{
		{
			name: "尾逗号",
			raw:  `{"thought":"t","action":"final","final":"好",}`,
			want: Step{Thought: "t", Action: "final", Final: "好"},
		},
		{
			name: "字符串裸换行",
			raw:  "{\"thought\":\"t\",\"action\":\"final\",\"final\":\"第一行\n第二行\"}",
			want: Step{Thought: "t", Action: "final", Final: "第一行\n第二行"},
		},
		{
			name: "args是对象",
			raw:  `{"thought":"t","action":"history","args":{"keyword":"鱼"}}`,
			want: Step{Thought: "t", Action: "history", Args: `{"keyword":"鱼"}`},
		},
		{
			name: "text别名",
			raw:  `{"thought":"t","action":"final","text":"好"}`,
			want: Step{Thought: "t", Action: "final", Final: "好"},
		},
		{
			name: "漏action但有final",
			raw:  `{"thought":"t","final":"好"}`,
			want: Step{Thought: "t", Action: "final", Final: "好"},
		},
		{
			name: "Final大小写",
			raw:  `{"thought":"t","action":"FINAL","final":"好"}`,
			want: Step{Thought: "t", Action: "final", Final: "好"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStep(tc.raw)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if got != tc.want {
				t.Errorf("解析结果不符：got=%+v want=%+v", got, tc.want)
			}
		})
	}
}

// TestDegradeOnlyAcceptsFinalBody（T17）降级只认 action=final 的正文：
// 工具步的 args/thought、残缺 JSON、空 final 一概不外发。
func TestDegradeOnlyAcceptsFinalBody(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "弹幕一"}
	cases := []struct {
		name    string
		degrade string
		want    string
	}{
		{"非final工具步", `{"thought":"内部推理：先查历史","action":"history","args":"10"}`, ""},
		{"非final却带final字段", `{"thought":"内部推理","action":"history","final":"内部内容","args":"10"}`, ""},
		{"残缺JSON", `{"thought":"内部推理","action":"final","final":"答案"`, ""},
		{"空final", `{"thought":"内部推理","action":"final","final":"  "}`, ""},
		{"final正文", `{"thought":"内部推理","action":"final","final":"在吃面包"}`, "在吃面包"},
		{"final的args正文", `{"thought":"内部推理","action":"final","args":"在吃面包"}`, "在吃面包"},
		{"纯文本", "直接回答：在吃面包", "直接回答：在吃面包"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm := &scriptedLLM{replies: []string{
				`{"thought":"查一下","action":"history","args":"10"}`,
				tc.degrade,
			}}
			agent := newTestAgent(t, llm, Options{MaxSteps: 1, Timeout: 2 * time.Second}, tool.tool())
			got, err := agent.Run(context.Background(), Input{Question: "问"})
			if err != nil {
				t.Fatalf("Run 失败：%v", err)
			}
			if !got.Degraded || got.Reason != DegradeSteps {
				t.Errorf("应步数耗尽降级：%+v", got)
			}
			if got.Text != tc.want {
				t.Errorf("降级正文 = %q，期望 %q", got.Text, tc.want)
			}
		})
	}
}

// TestFinalBodyRejectsJSON 普通收尾同样过正文校验：
// 正文像协议 JSON 时降级重问，最终只发人话；final 的 args 仍兼容。
func TestFinalBodyRejectsJSON(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "x"}
	llm := &scriptedLLM{replies: []string{
		`{"thought":"想好了","action":"final","final":"{\"thought\":\"内部\",\"action\":\"history\",\"args\":\"10\"}"}`,
		`{"action":"final","final":"在吃面包"}`,
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())
	got, err := agent.Run(context.Background(), Input{Question: "问"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if !got.Degraded || got.Reason != DegradeEmpty {
		t.Errorf("JSON 形状正文应判为空正文并降级：%+v", got)
	}
	if got.Text != "在吃面包" {
		t.Errorf("降级后正文 = %q", got.Text)
	}

	// final 的 args 是协议允许的正文位置
	llm2 := &scriptedLLM{replies: []string{`{"action":"final","args":"在吃面包"}`}}
	agent2 := newTestAgent(t, llm2, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())
	got2, err := agent2.Run(context.Background(), Input{Question: "问"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if got2.Text != "在吃面包" || got2.Degraded {
		t.Errorf("final 的 args 正文应直接采用：%+v", got2)
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

// TestUserPromptIncludesContextSnapshot Input.Context 进入 user 段且位于问题之前；
// 空 Context 不产生多余空行，保持旧格式。
func TestUserPromptIncludesContextSnapshot(t *testing.T) {
	a := &Agent{}
	got := a.userPrompt(Input{
		Context:  " <context>{\"recent\":[]}</context>\n[当前状态] 在看直播 ",
		Question: "记得吗",
	}, nil)
	wantPrefix := "<context>{\"recent\":[]}</context>\n[当前状态] 在看直播\n弹幕：记得吗"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("Context 未进入 user 段或位置不对：%q", got)
	}

	got = a.userPrompt(Input{Question: "在吗"}, nil)
	if !strings.HasPrefix(got, "弹幕：在吗\n") {
		t.Fatalf("空 Context 不应改变旧 prompt 格式：%q", got)
	}
}
