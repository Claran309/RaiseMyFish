package memory

// 本文件测 fact（自我陈述）规则：正例、反例、空文本、去重主键。

import (
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
)

func factEvent(text string) Event {
	return event.Event{
		ID: "live:room:1", Channel: event.ChLive, Dir: event.DirIn, Session: "room",
		FromID: "2002", FromName: "熟客乙", Text: text, At: time.Now(),
	}
}

// factCandidates 只挑 fact 类候选，避免被别的规则干扰。
func factCandidates(text string) []Candidate {
	var out []Candidate
	for _, c := range Extract(factEvent(text), ExtractOptions{}) {
		if c.Kind == KindFact {
			out = append(out, c)
		}
	}
	return out
}

// TestFactSelfStatementsAreExtracted 规则版不再产 fact（factMarkers 清空——「我是梁文峰」这类
// 玩笑/钓鱼会被误收，事实统一交 LLM）；保留用例断言「不产」以防回归。
func TestFactSelfStatementsAreExtracted(t *testing.T) {
	cases := []string{
		"我是学生",
		"我爱吃辣",
		"我叫小王，住在广州",
		"我今年大二",
		"我养了两只猫",
	}
	for _, text := range cases {
		got := factCandidates(text)
		if len(got) != 0 {
			t.Fatalf("%q 规则版不应产 fact（交 LLM），实际 %d 条", text, len(got))
		}
		continue
		c := got[0]
		if c.Importance != factImportance {
			t.Errorf("%q 重要度 = %v，期望 %v", text, c.Importance, factImportance)
		}
		if !strings.HasPrefix(c.Title, "事实") {
			t.Errorf("%q 标题缺少前缀：%q", text, c.Title)
		}
		if len([]rune(c.Title)) > 60 {
			t.Errorf("%q 标题超 60 字：%d", text, len([]rune(c.Title)))
		}
		if len([]rune(c.Summary)) > 200 {
			t.Errorf("%q 摘要超 200 字", text)
		}
	}
}

// TestFactSecondPersonIsNotFact 第二人称提问不是事实。
func TestFactSecondPersonIsNotFact(t *testing.T) {
	cases := []string{
		"你是学生吧",
		"他爱吃辣吗",
		"她是老师",
		"你我是学生", // 触发词紧挨第二人称，不算自我陈述
		"今天天气不错",
	}
	for _, text := range cases {
		if got := factCandidates(text); len(got) != 0 {
			t.Errorf("%q 不应判为事实，实际 %+v", text, got)
		}
	}
}

// TestFactEmptyText 空文本/纯标点不提炼。
func TestFactEmptyText(t *testing.T) {
	for _, text := range []string{"", "   ", "？？？", "。。。"} {
		if got := Extract(factEvent(text), ExtractOptions{}); len(got) != 0 {
			t.Errorf("%q 不应产出候选：%+v", text, got)
		}
	}
}

// TestFactPrivateNeedsOptIn 私密事件仍未授权不提炼（沿用既有 fail-closed）。
func TestFactPrivateNeedsOptIn(t *testing.T) {
	ev := factEvent("我是学生")
	ev.Private = true
	if got := Extract(ev, ExtractOptions{}); len(got) != 0 {
		t.Errorf("私密未授权不应提炼：%+v", got)
	}
}
