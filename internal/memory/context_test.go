package memory

// 本文件测提炼上下文：Context 会进 LLM prompt，且不影响规则版。

import (
	"context"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

// TestLLMPromptIncludesContext 最近 N 条上下文拼进 user 段。
func TestLLMPromptIncludesContext(t *testing.T) {
	llm := &mockLLM{reply: `{"archive":false,"records":[],"confidence":0}`}
	e := newLLM(t, llm)

	opts := ExtractOptions{Context: []event.Event{
		{FromName: "甲", Text: "我昨天说我喜欢猫"},
		{FromName: "乙", Text: "我也喜欢猫"},
	}}
	_, _ = e.Extract(context.Background(), llmEvent("我今年大二学计算机专业平时喜欢写代码周末打游戏"), opts)

	if !strings.Contains(llm.gotUser, "最近上下文") {
		t.Fatalf("prompt 缺少上下文段：%s", llm.gotUser)
	}
	if !strings.Contains(llm.gotUser, "我昨天说我喜欢猫") {
		t.Errorf("上下文内容未带入：%s", llm.gotUser)
	}
}

// TestRuleExtractorIgnoresContext 规则版不因上下文改变结果。
func TestRuleExtractorIgnoresContext(t *testing.T) {
	ev := llmEvent("我是学生")
	with, err := NewRuleExtractor().Extract(context.Background(), ev, ExtractOptions{
		Context: []event.Event{{FromName: "甲", Text: "随便聊聊", At: time.Now()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	without := Extract(ev, ExtractOptions{})
	if len(with) != len(without) {
		t.Fatalf("上下文不应影响规则版：%d vs %d", len(with), len(without))
	}
	// 候选要带主体
	if len(with) > 0 && (with[0].FromID != "2002" || with[0].FromName != "熟客乙") {
		t.Errorf("候选未带主体：%+v", with[0])
	}
}
