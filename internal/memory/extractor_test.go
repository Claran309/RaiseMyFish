package memory

// 本文件测 LLM 提炼器：触发策略、JSON 解析、降级、限流。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

type mockLLM struct {
	reply   string
	err     error
	calls   int
	gotUser string
}

func (m *mockLLM) Complete(_ context.Context, _, user string) (string, error) {
	m.calls++
	m.gotUser = user
	if m.err != nil {
		return "", m.err
	}
	return m.reply, nil
}

func llmEvent(text string) event.Event {
	return event.Event{
		ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "2002", FromName: "熟客乙", Text: text, At: time.Now(),
	}
}

func newLLM(t *testing.T, llm LLM) *LLMExtractor {
	t.Helper()
	e, err := NewLLMExtractor(llm, LLMExtractOptions{MaxPerHour: 30, ContextLimit: 5})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestLLMExtractorParsesJSON 模型返回合法 JSON → 直接用模型结果。
func TestLLMExtractorParsesJSON(t *testing.T) {
	llm := &mockLLM{reply: `{"archive":true,"records":[{"kind":"fact","title":"他是学生","summary":"我今年大二，学计算机","importance":0.7}],"confidence":0.9}`}
	e := newLLM(t, llm)

	got, err := e.Extract(context.Background(), llmEvent("我今年大二，学计算机专业，平时喜欢写代码，周末喜欢打游戏和钓鱼，偶尔也看看直播放松一下"), ExtractOptions{})
	if err != nil {
		t.Fatalf("提取失败：%v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("应调用模型 1 次，实际 %d", llm.calls)
	}
	if len(got) != 1 || got[0].Kind != KindFact || got[0].Title != "他是学生" {
		t.Fatalf("模型结果未生效：%+v", got)
	}
	if got[0].Importance != 0.7 || got[0].Confidence != 0.9 {
		t.Errorf("权重没透传：%+v", got[0])
	}
	if !strings.Contains(llm.gotUser, "熟客乙") {
		t.Errorf("输入应带昵称：%s", llm.gotUser)
	}
}

// TestLLMExtractorFallsBackOnBadJSON 解析失败 → 回落规则版结果。
func TestLLMExtractorFallsBackOnBadJSON(t *testing.T) {
	llm := &mockLLM{reply: "我觉得他可能是个学生（不是 JSON）"}
	e := newLLM(t, llm)

	got, err := e.Extract(context.Background(), llmEvent("我是学生，今天也在上课，早上八点就有课，一直上到中午才下课，累死了"), ExtractOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if len(got) == 0 || got[0].Kind != KindFact {
		t.Fatalf("应回落规则版（fact）：%+v", got)
	}
	if !strings.HasPrefix(got[0].Title, "事实") {
		t.Errorf("规则版标题不符：%q", got[0].Title)
	}
}

// TestLLMExtractorFallsBackOnModelError 模型报错 → 回落规则版。
func TestLLMExtractorFallsBackOnModelError(t *testing.T) {
	llm := &mockLLM{err: errors.New("model down")}
	e := newLLM(t, llm)

	got, err := e.Extract(context.Background(), llmEvent("我是学生"), ExtractOptions{})
	if err != nil || len(got) == 0 {
		t.Fatalf("模型报错应回落规则：%+v / %v", got, err)
	}
}

// TestLLMExtractorTriggerPolicy 普通弹幕不调模型。
func TestLLMExtractorTriggerPolicy(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"我是学生", false},     // 短句，规则够用
		{"@大肥鱼 你觉得呢", true}, // @ 鱼
		{"我今年大二学计算机专业平时喜欢写代码周末打游戏", true}, // 长文本 >20 字
		{"摸摸头", true}, // 强互动
		{"", false},
	}
	for _, tc := range cases {
		llm := &mockLLM{reply: `{"archive":false,"records":[],"confidence":0}`}
		e := newLLM(t, llm)
		_, _ = e.Extract(context.Background(), llmEvent(tc.text), ExtractOptions{})
		if got := llm.calls == 1; got != tc.want {
			t.Errorf("%q 是否调用模型 = %v，期望 %v", tc.text, got, tc.want)
		}
	}
}

// TestLLMExtractorRateLimit 超过每小时上限后回落规则版，且不再调模型。
func TestLLMExtractorRateLimit(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	llm := &mockLLM{reply: `{"archive":true,"records":[{"kind":"fact","title":"t","summary":"s","importance":0.6}],"confidence":0.8}`}
	e, err := NewLLMExtractor(llm, LLMExtractOptions{MaxPerHour: 2, ContextLimit: 3, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ev := llmEvent("我今年大二，学计算机专业，平时喜欢写代码，周末喜欢打游戏和钓鱼，偶尔也看看直播放松一下")

	for i := 0; i < 2; i++ {
		if _, err := e.Extract(context.Background(), ev, ExtractOptions{}); err != nil {
			t.Fatalf("第 %d 次失败：%v", i+1, err)
		}
	}
	if llm.calls != 2 {
		t.Fatalf("前两次应调用模型，实际 %d", llm.calls)
	}
	// 第三次：限流，回落规则
	got, err := e.Extract(context.Background(), ev, ExtractOptions{})
	if err != nil {
		t.Fatalf("限流不应报错：%v", err)
	}
	if llm.calls != 2 {
		t.Errorf("限流后不应再调模型：%d", llm.calls)
	}
	if len(got) == 0 || !strings.HasPrefix(got[0].Title, "事实") {
		t.Errorf("限流应回落规则版：%+v", got)
	}

	// 过一小时后额度恢复
	now = now.Add(61 * time.Minute)
	if _, err := e.Extract(context.Background(), ev, ExtractOptions{}); err != nil {
		t.Fatal(err)
	}
	if llm.calls != 3 {
		t.Errorf("窗口滑过后应恢复：%d", llm.calls)
	}
}

// TestRuleExtractorMatchesPureFunction 规则版与纯函数结果一致。
func TestRuleExtractorMatchesPureFunction(t *testing.T) {
	ev := llmEvent("我是学生")
	viaInterface, err := NewRuleExtractor().Extract(context.Background(), ev, ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	direct := Extract(ev, ExtractOptions{})
	if len(viaInterface) != len(direct) {
		t.Fatalf("条数不一致：%d vs %d", len(viaInterface), len(direct))
	}
	for i := range direct {
		if viaInterface[i] != direct[i] {
			t.Errorf("第 %d 条不一致：%+v vs %+v", i, viaInterface[i], direct[i])
		}
	}
}

// TestLLMExtractorNilLLM 没接模型时退化成规则版。
func TestLLMExtractorNilLLM(t *testing.T) {
	e := newLLM(t, nil)
	got, err := e.Extract(context.Background(), llmEvent("我是学生"), ExtractOptions{})
	if err != nil || len(got) == 0 {
		t.Fatalf("应直接走规则版：%+v / %v", got, err)
	}
}

// TestLLMExtractorPrivateRequiresAuthorization 未授权私密事件必须在模型调用之前被拦截。
//
// 回归背景：规则版 Extract 会返回空，但 LLM 分支此前继续执行，
// 导致长私信 / @ 命中触发策略时，私密正文仍被送进 Complete。
func TestLLMExtractorPrivateRequiresAuthorization(t *testing.T) {
	llm := &mockLLM{reply: `{"archive":true,"records":[{"kind":"fact","title":"私事","summary":"观众深夜倾诉","importance":0.7}],"confidence":0.9}`}
	e := newLLM(t, llm)

	ev := llmEvent("@大肥鱼 我今天遇到好多事情，想慢慢跟你说一件很长的私事，千万别告诉别人")
	ev.Channel = event.ChDM
	ev.Session = "dm-a"
	ev.Private = true

	// 未授权（AllowPrivate 零值）：零调用、零结果、不拼 prompt。
	got, err := e.Extract(context.Background(), ev, ExtractOptions{})
	if err != nil || len(got) != 0 {
		t.Fatalf("未授权私密事件应直接跳过：%+v / %v", got, err)
	}
	if llm.calls != 0 {
		t.Fatalf("未授权私密事件不应调用模型，实际 %d 次", llm.calls)
	}
	if llm.gotUser != "" {
		t.Fatalf("未授权私密事件不应拼 prompt：%q", llm.gotUser)
	}

	// 显式授权后同一事件才允许进入模型。
	got, err = e.Extract(context.Background(), ev, ExtractOptions{AllowPrivate: true})
	if err != nil || len(got) != 1 || got[0].Title != "私事" {
		t.Fatalf("授权后应使用模型结果：%+v / %v", got, err)
	}
	if llm.calls != 1 {
		t.Errorf("授权后应调用模型 1 次，实际 %d", llm.calls)
	}
}
