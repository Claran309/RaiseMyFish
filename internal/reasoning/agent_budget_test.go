package reasoning

// 本文件测 ReAct 调用预算（F04）：
//   - 每次实际调用前都按 PromptBudget 校验；
//   - 超限时按整条观察从旧到新裁剪，当前问题与资料段不动；
//   - 连基础 prompt 都放不下时不发模型请求，返回 budget 降级；
//   - Prepare 预留在最坏工具输出下仍覆盖一条完整观察。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/brain/provider"
)

// budgetLLM 记录每次 system/user 并按脚本回复，便于逐次核对实际 prompt 大小。
type budgetLLM struct {
	mu      sync.Mutex
	replies []string
	systems []string
	users   []string
}

func (l *budgetLLM) Complete(_ context.Context, system, user string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.systems = append(l.systems, system)
	l.users = append(l.users, user)
	if len(l.replies) == 0 {
		return "", errors.New("脚本用尽")
	}
	next := l.replies[0]
	l.replies = l.replies[1:]
	return next, nil
}

func (l *budgetLLM) promptSize(i int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return provider.MessageOverhead + len(l.systems[i]) + len(l.users[i])
}

func (l *budgetLLM) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.users)
}

// TestRunTrimsOldestObservationsAtEvidenceBoundary F04：
// 预算只够放一条观察时，第二轮保留第一条；第三轮丢掉最旧的整条观察，
// 保留最新证据、当前问题与资料段，且每次实际调用都不超预算。
func TestRunTrimsOldestObservationsAtEvidenceBoundary(t *testing.T) {
	const (
		oldResult = "旧结果" + "旧旧旧旧旧旧旧旧旧旧旧旧旧旧旧旧"
		newResult = "新结果" + "新新新新新新新新新新新新新新新新"
	)
	tool := Tool{Name: ToolHistory, Desc: "查历史", Run: func(_ context.Context, args string) (string, error) {
		switch args {
		case "old":
			return oldResult, nil
		case "new":
			return newResult, nil
		}
		return "其他", nil
	}}
	llm := &budgetLLM{replies: []string{
		`{"thought":"先查旧","action":"history","args":"old"}`,
		`{"thought":"再查新","action":"history","args":"new"}`,
		`{"thought":"够了","action":"final","final":"好"}`,
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool)

	in := Input{System: "人设", Context: "资料段", Question: "问题正文"}
	base := agent.Estimate(in)
	obsOne := observationHeader + fmt.Sprintf("- %s(%s) → %s\n", ToolHistory, "old", oldResult)
	// 只够一条观察 + 少量余量：第二条观察进来时必须丢掉最旧的那条。
	in.PromptBudget = base - len("（还没有查证）") + len(obsOne) + 8

	got, err := agent.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if !strings.Contains(got.Text, "好") || got.Degraded {
		t.Fatalf("应正常收尾：text=%q degraded=%v reason=%q", got.Text, got.Degraded, got.Reason)
	}
	if llm.count() != 3 {
		t.Fatalf("模型调用 %d 次，期望 3（两次工具 + 收尾）", llm.count())
	}
	for i := 0; i < llm.count(); i++ {
		if size := llm.promptSize(i); size > in.PromptBudget {
			t.Fatalf("第 %d 次实际调用超预算：%d > %d", i+1, size, in.PromptBudget)
		}
	}
	if !strings.Contains(llm.users[1], "history(old)") || !strings.Contains(llm.users[1], oldResult) {
		t.Fatalf("第二轮应带上第一条观察：\n%s", llm.users[1])
	}
	last := llm.users[2]
	if strings.Contains(last, "history(old)") || strings.Contains(last, oldResult) {
		t.Fatalf("最旧的观察未按整条裁剪：\n%s", last)
	}
	if !strings.Contains(last, "history(new)") || !strings.Contains(last, newResult) {
		t.Fatalf("最新观察被误删：\n%s", last)
	}
	for i := 0; i < llm.count(); i++ {
		if !strings.Contains(llm.users[i], "问题正文") || !strings.Contains(llm.users[i], "资料段") {
			t.Fatalf("第 %d 次调用丢失当前问题或资料段：\n%s", i+1, llm.users[i])
		}
	}
	if got.PromptBytes != llm.promptSize(2) {
		t.Fatalf("PromptBytes=%d，期望最后一次实际调用大小 %d", got.PromptBytes, llm.promptSize(2))
	}
}

// TestRunBudgetBlockedMakesNoModelCall 基础 prompt 都放不下时不发请求，交给调用方回落。
func TestRunBudgetBlockedMakesNoModelCall(t *testing.T) {
	tool := &countingTool{name: ToolHistory, out: "结果"}
	llm := &budgetLLM{}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool.tool())
	in := Input{System: "人设", Context: "资料", Question: "问题"}
	in.PromptBudget = agent.Estimate(in) - 1

	got, err := agent.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("预算不足不应上抛错误：%v", err)
	}
	if !got.Degraded || got.Reason != DegradeBudget || got.Text != "" {
		t.Fatalf("应为 budget 降级且无正文：%+v", got)
	}
	if llm.count() != 0 || tool.calls != 0 {
		t.Fatalf("预算不足仍发起调用：llm=%d tool=%d", llm.count(), tool.calls)
	}
	if got.PromptBytes != 0 {
		t.Fatalf("未调用模型时 PromptBytes 应为 0，实际 %d", got.PromptBytes)
	}
}

// TestObservationReserveCoversWorstCaseLine 预留必须覆盖「60 字符参数 + 400 字符结果」的整条观察，
// 这样 Prepare 阶段扣掉预留后，第一次工具查证的结果不会在下一轮被裁掉。
func TestObservationReserveCoversWorstCaseLine(t *testing.T) {
	args := strings.Repeat("参", maxObservationArgsRunes)
	result := strings.Repeat("长", maxObservationRunes)
	tool := Tool{Name: ToolHistory, Desc: "查历史", Run: func(_ context.Context, _ string) (string, error) {
		return result, nil
	}}
	llm := &budgetLLM{replies: []string{
		fmt.Sprintf(`{"thought":"查","action":"history","args":%q}`, args),
		`{"thought":"够了","action":"final","final":"好"}`,
	}}
	agent := newTestAgent(t, llm, Options{MaxSteps: 3, Timeout: 2 * time.Second}, tool)
	reserve := agent.ObservationReserve()

	if _, err := agent.Run(context.Background(), Input{System: "人设", Context: "资料", Question: "问题"}); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if llm.count() != 2 {
		t.Fatalf("模型调用 %d 次，期望 2", llm.count())
	}
	line := fmt.Sprintf("- %s(%s) → %s\n", ToolHistory, args, result)
	if actual := len(observationHeader) + len(line); reserve < actual {
		t.Fatalf("观察预留不足：reserve=%d actual=%d", reserve, actual)
	}
}
