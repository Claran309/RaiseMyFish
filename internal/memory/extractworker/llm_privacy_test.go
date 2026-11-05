package extractworker

// M02：未授权私密事件必须在任何模型调用之前被拦截。
//
// 长私信 / @ 会命中 LLM 触发策略；若只在规则版里判断 AllowPrivate，
// 私密正文仍会进入 Complete 并可能被写成记忆记录。

import (
	"context"
	"testing"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

// countingLLM 只统计调用次数；不该被调用时 reply 也不会被使用。
type countingLLM struct {
	calls int
	reply string
}

func (m *countingLLM) Complete(context.Context, string, string) (string, error) {
	m.calls++
	return m.reply, nil
}

// countingRecent 统计上下文查询次数：未授权私密事件不应触达这一步。
type countingRecent struct{ calls int }

func (c *countingRecent) RecentEvents(context.Context, memory.Scope, int) ([]event.Event, error) {
	c.calls++
	return nil, nil
}

// TestPrivateEventSkippedBeforeLLM 未授权 + 长私信（>20 字且含 @）：
// LLM 调用 0 次、上下文查询 0 次、落库 0 条，且水位继续推进。
func TestPrivateEventSkippedBeforeLLM(t *testing.T) {
	ev := mkEvent("dm:42:3101", baseTime,
		"@主人 我今天遇到好多事情，想慢慢跟你说一件很长的私事，千万别告诉别人",
		"路人甲", true, event.ChDM, "42")
	src := &fakeSource{events: []event.Event{ev}}
	repo := &fakeRepo{}
	wm := NewMemoryWatermark()

	llm := &countingLLM{reply: `{"archive":true,"records":[{"kind":"fact","title":"私事","summary":"观众深夜倾诉","importance":0.7}],"confidence":0.9}`}
	extractor, err := memory.NewLLMExtractor(llm, memory.LLMExtractOptions{MaxPerHour: 10})
	if err != nil {
		t.Fatalf("构造 LLM 提炼器失败：%v", err)
	}
	recent := &countingRecent{}

	w := newWorker(t, src, repo, wm, Options{
		Extractor: extractor,
		Recent:    recent,
		// AllowPrivate 零值 = false：默认拒绝
	})
	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Scanned != 1 || stats.Skipped != 1 || stats.Extracted != 0 || stats.Inserted != 0 {
		t.Errorf("统计 = %+v，期望 Scanned=1 Skipped=1 Extracted=0 Inserted=0", stats)
	}
	if llm.calls != 0 {
		t.Errorf("未授权私密事件不应调用模型，实际 %d 次", llm.calls)
	}
	if recent.calls != 0 {
		t.Errorf("未授权私密事件不应查询上下文，实际 %d 次", recent.calls)
	}
	if n := len(repo.all()); n != 0 {
		t.Errorf("未授权私密事件不应落库，实际 %d 条", n)
	}

	cursor, err := wm.Load(context.Background())
	if err != nil {
		t.Fatalf("读水位失败：%v", err)
	}
	if cursor.IsZero() {
		t.Error("跳过的私密事件也必须推进水位，否则下一轮会重复扫描")
	}
}
