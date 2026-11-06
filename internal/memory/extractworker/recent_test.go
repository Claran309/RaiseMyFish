package extractworker

// 本文件测 M07：提炼上下文必须以「被处理事件」为上界。
//
// 回归背景：补提炼历史时，RecentEvents 取的是「现在最新的 N 条」，
// 会把晚于当前事件的发言当成前文，回放结果不可复现。

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

// fakeRecentBefore 实现了带时间上界的查询，并记录收到的上界参数。
type fakeRecentBefore struct {
	rows     []event.Event
	gotAt    time.Time
	gotID    string
	gotLimit int
	gotScope memory.Scope
	calls    int
}

func (f *fakeRecentBefore) RecentEvents(context.Context, memory.Scope, int) ([]event.Event, error) {
	f.calls++
	return f.rows, nil
}

func (f *fakeRecentBefore) RecentEventsBefore(_ context.Context, scope memory.Scope, at time.Time, id string, limit int) ([]event.Event, error) {
	f.calls++
	f.gotScope, f.gotAt, f.gotID, f.gotLimit = scope, at, id, limit
	return f.rows, nil
}

// plainRecent 只实现「最近 N 条」，用于验证客户端兜底过滤。
type plainRecent struct{ rows []event.Event }

func (p *plainRecent) RecentEvents(context.Context, memory.Scope, int) ([]event.Event, error) {
	return p.rows, nil
}

// captureExtractor 记录提炼器实际收到的上下文。
type captureExtractor struct {
	seen []event.Event
	ran  bool
}

func (c *captureExtractor) Extract(_ context.Context, _ event.Event, opts memory.ExtractOptions) ([]memory.Candidate, error) {
	c.ran = true
	c.seen = opts.Context
	return nil, nil
}

// TestRecentContextUsesEventUpperBound 来源支持上界时，直接把事件时间与 ID 传下去。
func TestRecentContextUsesEventUpperBound(t *testing.T) {
	ev := mkEvent("live:1:4001", baseTime, "我保证明天带话", "路人甲", false, event.ChLive, "1")
	past := mkEvent("live:1:4000", baseTime.Add(-time.Minute), "前面说的话", "路人乙", false, event.ChLive, "1")

	recent := &fakeRecentBefore{rows: []event.Event{past}}
	cap := &captureExtractor{}
	w := newWorker(t, &fakeSource{events: []event.Event{ev}}, &fakeRepo{}, NewMemoryWatermark(), Options{
		Recent:    recent,
		Extractor: cap,
	})

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if !recent.gotAt.Equal(ev.At) || recent.gotID != ev.ID {
		t.Fatalf("上界参数不符：at=%v id=%q（期望 %v / %q）", recent.gotAt, recent.gotID, ev.At, ev.ID)
	}
	if recent.gotLimit != 6 {
		t.Errorf("上下文条数 = %d，期望 ContextLimit+1=6", recent.gotLimit)
	}
	if recent.gotScope.Channel != ev.Channel || recent.gotScope.Session != ev.Session {
		t.Errorf("查询范围不符：%+v", recent.gotScope)
	}
	if !cap.ran || len(cap.seen) != 1 || cap.seen[0].ID != past.ID {
		t.Fatalf("上下文未进入提炼器：%+v", cap.seen)
	}
}

// TestRecentContextFallbackFiltersFuture 来源没有上界能力时，客户端必须剔除未来事件。
func TestRecentContextFallbackFiltersFuture(t *testing.T) {
	now := baseTime
	ev := mkEvent("live:1:4010", now, "我保证明天带话", "路人甲", false, event.ChLive, "1")
	future := mkEvent("live:1:4011", now.Add(time.Minute), "后来说的话", "路人丙", false, event.ChLive, "1")
	sameTimeLater := mkEvent("live:1:4012", now, "同一时刻但 id 更大", "路人丁", false, event.ChLive, "1")
	past := mkEvent("live:1:4009", now.Add(-time.Minute), "之前说的话", "路人乙", false, event.ChLive, "1")
	current := mkEvent("live:1:4010", now, "我保证明天带话", "路人甲", false, event.ChLive, "1")

	cap := &captureExtractor{}
	w := newWorker(t, &fakeSource{events: []event.Event{ev}}, &fakeRepo{}, NewMemoryWatermark(), Options{
		// RecentEvents 返回顺序是倒序（新→旧），这里刻意混入未来与当前事件
		Recent:    &plainRecent{rows: []event.Event{future, sameTimeLater, current, past}},
		Extractor: cap,
	})

	if _, err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if len(cap.seen) != 1 || cap.seen[0].ID != past.ID {
		t.Fatalf("只应保留严格早于当前事件的历史：%+v", cap.seen)
	}
}

// TestKeepBefore 边界：晚于 / 等于 / 早于当前事件的判定。
func TestKeepBefore(t *testing.T) {
	at := baseTime
	rows := []event.Event{
		{ID: "a", At: at.Add(time.Second)},  // 未来
		{ID: "b", At: at},                   // 同一时刻
		{ID: "c", At: at.Add(-time.Second)}, // 更早
	}
	got := keepBefore(rows, at, "b")
	if len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("过滤结果不符：%+v", got)
	}
}
