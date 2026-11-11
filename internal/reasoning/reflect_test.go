package reasoning

// 本文件测批 I：三层 thought（ThinkDeep）与收尾反思（Reflect）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestThinkDeepAddsThreeLayerThoughtInstruction 弹幕通道的协议要求 thought 写完三层；
// 未开启时保持原协议，避免评论/私信被加长。
func TestThinkDeepAddsThreeLayerThoughtInstruction(t *testing.T) {
	agent := newTestAgent(t, &scriptedLLM{}, DefaultOptions(), (&countingTool{name: "noop"}).tool())

	deep := agent.systemPrompt(Input{System: "人设", ThinkDeep: true})
	for _, want := range []string{"①字面", "②关联", "③表达", "｜"} {
		if !strings.Contains(deep, want) {
			t.Fatalf("ThinkDeep prompt 缺少 %q：%s", want, deep)
		}
	}
	plain := agent.systemPrompt(Input{System: "人设"})
	if strings.Contains(plain, "①字面") {
		t.Error("未开启 ThinkDeep 不应出现三层要求")
	}
}

// TestReflectRewritesDraft 反思成功时用改写稿替换原稿，并把反思计入步数。
func TestReflectRewritesDraft(t *testing.T) {
	llm := &scriptedLLM{replies: []string{
		`{"thought":"直接回答","action":"final","final":"原稿"}`,
		"改写后的回复",
	}}
	agent := newTestAgent(t, llm, DefaultOptions(), (&countingTool{name: "noop"}).tool())

	res, err := agent.Run(context.Background(), Input{Question: "在吗", Reflect: true})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if res.Text != "改写后的回复" {
		t.Fatalf("反思应替换原稿：%q", res.Text)
	}
	if res.Steps != 2 {
		t.Errorf("反思应计一步：steps=%d", res.Steps)
	}
	if res.Degraded {
		t.Errorf("反思成功不应降级：%+v", res)
	}
	if llm.calls != 2 {
		t.Fatalf("应恰好调用两次模型（首轮+反思）：%d", llm.calls)
	}
	if res.PromptBytes <= 0 {
		t.Error("反思调用应记录 PromptBytes")
	}
	if !strings.Contains(llm.prompts[1], "草稿：") || !strings.Contains(llm.prompts[1], "原稿") {
		t.Errorf("反思 prompt 未带草稿：%s", llm.prompts[1])
	}
	if !strings.Contains(llm.prompts[1], "只输出改写后的回复正文") {
		t.Errorf("反思 prompt 缺少输出约束：%s", llm.prompts[1])
	}
	if strings.Contains(res.Text, "thought") {
		t.Errorf("反思正文不能夹带内部字段：%q", res.Text)
	}
}

// TestReflectKeepsDraftOnFailure 反思报错保留原稿，不降级、不影响已发弹幕。
func TestReflectKeepsDraftOnFailure(t *testing.T) {
	llm := &scriptedLLM{
		replies: []string{`{"thought":"直接回答","action":"final","final":"原稿"}`},
		errs:    map[int]error{1: errors.New("模型挂了")},
	}
	agent := newTestAgent(t, llm, DefaultOptions(), (&countingTool{name: "noop"}).tool())

	res, err := agent.Run(context.Background(), Input{Question: "在吗", Reflect: true})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if res.Text != "原稿" {
		t.Fatalf("反思失败应保留原稿：%q", res.Text)
	}
	if res.Degraded {
		t.Error("反思失败不应把整条回复标记为降级")
	}
	if res.Steps != 2 {
		t.Errorf("失败但已发起的反思也应计一步：steps=%d", res.Steps)
	}
}

// TestReflectKeepsDraftOnEmpty 反思回空/回内部格式时保留原稿。
func TestReflectKeepsDraftOnEmpty(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"空串", ""},
		{"代码块", "```\n改写\n```"},
		{"JSON", `{"action":"final","final":"改写"}`},
		{"夹带thought", `{"thought":"内部推理"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm := &scriptedLLM{replies: []string{
				`{"thought":"直接回答","action":"final","final":"原稿"}`,
				tc.reply,
			}}
			agent := newTestAgent(t, llm, DefaultOptions(), (&countingTool{name: "noop"}).tool())
			res, err := agent.Run(context.Background(), Input{Question: "在吗", Reflect: true})
			if err != nil {
				t.Fatalf("Run 失败：%v", err)
			}
			if res.Text != "原稿" {
				t.Errorf("坏反思输出应保留原稿：%q", res.Text)
			}
		})
	}
}

// TestReflectSkipsWhenStepsExhausted 步数预算用尽时不再反思：
// MaxSteps=1 时首轮 final 后必须直接收尾。
func TestReflectSkipsWhenStepsExhausted(t *testing.T) {
	llm := &scriptedLLM{replies: []string{
		`{"thought":"直接回答","action":"final","final":"原稿"}`,
		"不该被调用",
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 1, Timeout: time.Second}, (&countingTool{name: "noop"}).tool())

	res, err := agent.Run(context.Background(), Input{Question: "在吗", Reflect: true})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if res.Text != "原稿" || res.Steps != 1 || llm.calls != 1 {
		t.Fatalf("步数用尽应跳过反思：text=%q steps=%d calls=%d", res.Text, res.Steps, llm.calls)
	}
}

// TestReflectSkipsWhenTimeLow 剩余时间不足 reflectMinRemaining 时跳过反思。
func TestReflectSkipsWhenTimeLow(t *testing.T) {
	llm := &scriptedLLM{replies: []string{
		`{"thought":"直接回答","action":"final","final":"原稿"}`,
		"不该被调用",
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: time.Second}, (&countingTool{name: "noop"}).tool())

	res, err := agent.Run(context.Background(), Input{Question: "在吗", Reflect: true})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if res.Text != "原稿" || res.Steps != 1 || llm.calls != 1 {
		t.Fatalf("剩余时间不足应跳过反思：text=%q steps=%d calls=%d", res.Text, res.Steps, llm.calls)
	}
}

// TestReflectSkipsWhenPromptOverBudget 反思 prompt 超出调用预算时跳过，不发请求。
func TestReflectSkipsWhenPromptOverBudget(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"不该被调用"}}
	agent := newTestAgent(t, llm, DefaultOptions(), (&countingTool{name: "noop"}).tool())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	in := Input{Question: "在吗", Reflect: true, PromptBudget: 10}
	text, called, size := agent.reflect(ctx, in, "原稿", 1, nil)
	if called || text != "" || size != 0 {
		t.Fatalf("超预算应跳过反思：text=%q called=%v size=%d", text, called, size)
	}
	if llm.calls != 0 {
		t.Errorf("跳过时不应调用模型：%d", llm.calls)
	}
}

// TestReflectAlsoRunsOnPlainReply 模型直接回自然语言（未按 JSON 协议）时也要反思。
func TestReflectAlsoRunsOnPlainReply(t *testing.T) {
	llm := &scriptedLLM{replies: []string{"直接给的原稿", "改写后的回复"}}
	agent := newTestAgent(t, llm, DefaultOptions(), (&countingTool{name: "noop"}).tool())

	res, err := agent.Run(context.Background(), Input{Question: "在吗", Reflect: true})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if res.Text != "改写后的回复" || res.Steps != 2 {
		t.Fatalf("自然语言收尾也应反思：text=%q steps=%d", res.Text, res.Steps)
	}
}

// TestReflectOffByDefault 未开启 Reflect 时不追加自检调用。
func TestReflectOffByDefault(t *testing.T) {
	llm := &scriptedLLM{replies: []string{
		`{"thought":"直接回答","action":"final","final":"原稿"}`,
		"不该被调用",
	}}
	agent := newTestAgent(t, llm, DefaultOptions(), (&countingTool{name: "noop"}).tool())

	res, err := agent.Run(context.Background(), Input{Question: "在吗"})
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if res.Text != "原稿" || res.Steps != 1 || llm.calls != 1 {
		t.Fatalf("未开启 Reflect 不应追加调用：text=%q steps=%d calls=%d", res.Text, res.Steps, llm.calls)
	}
}

// TestHasTool 调用方据此决定「主动看直播」还是「被动注入」。
func TestHasTool(t *testing.T) {
	live := &countingTool{name: ToolWatchLive}
	agent := newTestAgent(t, &scriptedLLM{}, DefaultOptions(), live.tool(), (&countingTool{name: "noop"}).tool())
	if !agent.HasTool(ToolWatchLive) {
		t.Error("注册了 watch_live 应返回 true")
	}
	if agent.HasTool(ToolVision) {
		t.Error("未注册的 vision 不应返回 true")
	}

	other := newTestAgent(t, &scriptedLLM{}, DefaultOptions(), (&countingTool{name: "noop"}).tool())
	if other.HasTool(ToolWatchLive) {
		t.Error("未注册 watch_live 应返回 false")
	}
	var nilAgent *Agent
	if nilAgent.HasTool(ToolWatchLive) {
		t.Error("nil Agent 应返回 false")
	}
}
