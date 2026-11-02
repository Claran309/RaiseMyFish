package presence

import (
	"sync"
	"testing"
	"time"
)

func TestEmptyTable(t *testing.T) {
	tbl := NewTable()
	now := time.Now()

	if got := tbl.Active(now); len(got) != 0 {
		t.Fatalf("空表 Active = %+v，期望空列表", got)
	}
	if got := tbl.Summary(now); got != "" {
		t.Fatalf("空表 Summary = %q，期望空字符串", got)
	}
	tbl.Clear(KindWatching, "live")
}

func TestActiveSortAndSummaryLabels(t *testing.T) {
	tbl := NewTable()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	tbl.Set(State{Kind: KindWatching, Channel: "live", Detail: "30308220", Since: now.Add(-5 * time.Minute), TTL: 10 * time.Minute})
	tbl.Set(State{Kind: KindReplying, Channel: "live", Since: now.Add(-4 * time.Minute), TTL: 10 * time.Minute})
	tbl.Set(State{Kind: KindSending, Channel: "live", Since: now.Add(-3 * time.Minute), TTL: 10 * time.Minute})
	tbl.Set(State{Kind: KindGift, Channel: "live", Detail: "小花花", Since: now.Add(-2 * time.Minute), TTL: 10 * time.Minute})
	tbl.Set(State{Kind: KindEditing, Channel: "live", Since: now.Add(-time.Minute), TTL: 10 * time.Minute})

	active := tbl.Active(now)
	wantKinds := []string{KindWatching, KindReplying, KindSending, KindGift, KindEditing}
	if len(active) != len(wantKinds) {
		t.Fatalf("Active 数量 = %d，期望 %d：%+v", len(active), len(wantKinds), active)
	}
	for i, want := range wantKinds {
		if active[i].Kind != want {
			t.Errorf("第 %d 项 Kind = %q，期望 %q（按 Since 升序）", i, active[i].Kind, want)
		}
	}

	wantSummary := "在看直播 30308220 / 在回弹幕 / 在发弹幕 / 在送礼 小花花 / 在剪视频"
	if got := tbl.Summary(now); got != wantSummary {
		t.Errorf("Summary = %q，期望 %q", got, wantSummary)
	}
}

func TestActiveFiltersExpired(t *testing.T) {
	tbl := NewTable()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	// 已过期：6 分钟前开始，TTL 5 分钟
	tbl.Set(State{Kind: KindWatching, Channel: "live", Detail: "30308220", Since: now.Add(-6 * time.Minute), TTL: 5 * time.Minute})
	// 恰好到期：Since+TTL == now，同样不应返回
	tbl.Set(State{Kind: KindReplying, Channel: "live", Since: now.Add(-time.Minute), TTL: time.Minute})
	// 未过期
	tbl.Set(State{Kind: KindSending, Channel: "live", Since: now.Add(-59 * time.Second), TTL: time.Minute})

	active := tbl.Active(now)
	if len(active) != 1 || active[0].Kind != KindSending {
		t.Fatalf("Active = %+v，期望只剩 sending", active)
	}
	if got := tbl.Summary(now); got != "在发弹幕" {
		t.Errorf("Summary = %q，期望只含未过期状态", got)
	}
}

func TestSetDefaultsSinceAndTTL(t *testing.T) {
	tbl := NewTable()
	before := time.Now()
	tbl.Set(State{Kind: KindWatching, Channel: "live"})
	after := time.Now()

	active := tbl.Active(after)
	if len(active) != 1 {
		t.Fatalf("Active = %+v，期望 1 条", active)
	}
	got := active[0]
	if got.TTL != DefaultTTL {
		t.Errorf("TTL = %v，期望默认 %v", got.TTL, DefaultTTL)
	}
	if got.Since.Before(before) || got.Since.After(after) {
		t.Errorf("Since = %v，期望落在 [%v, %v]", got.Since, before, after)
	}
	if summary := tbl.Summary(after); summary != "在看直播" {
		t.Errorf("Summary = %q，期望「在看直播」", summary)
	}

	// 默认 TTL 也从 Since 起算
	if expired := tbl.Active(got.Since.Add(DefaultTTL)); len(expired) != 0 {
		t.Errorf("到期边界仍返回：%+v", expired)
	}
}

func TestSetOverwritesSameKindAndChannel(t *testing.T) {
	tbl := NewTable()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	tbl.Set(State{Kind: KindWatching, Channel: "live", Detail: "旧", Since: now.Add(-2 * time.Minute), TTL: 5 * time.Minute})
	tbl.Set(State{Kind: KindWatching, Channel: "live", Detail: "新", Since: now.Add(-time.Minute), TTL: 5 * time.Minute})
	tbl.Set(State{Kind: KindWatching, Channel: "comment", Detail: "另一通道", Since: now.Add(-3 * time.Minute), TTL: 5 * time.Minute})

	active := tbl.Active(now)
	if len(active) != 2 {
		t.Fatalf("Active = %+v，期望同 Kind+Channel 覆盖后只剩 2 条", active)
	}
	if active[0].Channel != "comment" || active[1].Detail != "新" {
		t.Errorf("覆盖结果不符：%+v", active)
	}
}

func TestClearRemovesOnlyMatchingState(t *testing.T) {
	tbl := NewTable()
	now := time.Now()

	tbl.Set(State{Kind: KindWatching, Channel: "live"})
	tbl.Set(State{Kind: KindReplying, Channel: "live"})
	tbl.Clear(KindWatching, "live")

	active := tbl.Active(now)
	if len(active) != 1 || active[0].Kind != KindReplying {
		t.Fatalf("Active = %+v，期望只移除 watching/live", active)
	}

	tbl.Clear(KindReplying, "live")
	if got := tbl.Active(now); len(got) != 0 {
		t.Fatalf("清空后仍有状态：%+v", got)
	}
	if got := tbl.Summary(now); got != "" {
		t.Errorf("清空后 Summary = %q，期望空字符串", got)
	}
}

func TestSummaryDedupesIdenticalPhrases(t *testing.T) {
	tbl := NewTable()
	now := time.Now()

	tbl.Set(State{Kind: KindReplying, Channel: "live", Since: now.Add(-2 * time.Minute), TTL: 5 * time.Minute})
	tbl.Set(State{Kind: KindReplying, Channel: "dm", Since: now.Add(-time.Minute), TTL: 5 * time.Minute})

	if got := tbl.Summary(now); got != "在回弹幕" {
		t.Errorf("Summary = %q，期望相同短语去重", got)
	}
}

func TestActiveDeterministicOnEqualSince(t *testing.T) {
	tbl := NewTable()
	since := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	tbl.Set(State{Kind: KindWatching, Channel: "live", Since: since, TTL: 5 * time.Minute})
	tbl.Set(State{Kind: KindReplying, Channel: "live", Since: since, TTL: 5 * time.Minute})
	tbl.Set(State{Kind: KindWatching, Channel: "comment", Since: since, TTL: 5 * time.Minute})

	active := tbl.Active(since)
	want := []struct{ kind, channel string }{
		{KindReplying, "live"},
		{KindWatching, "comment"},
		{KindWatching, "live"},
	}
	if len(active) != len(want) {
		t.Fatalf("Active 数量 = %d，期望 %d", len(active), len(want))
	}
	for i := range want {
		if active[i].Kind != want[i].kind || active[i].Channel != want[i].channel {
			t.Errorf("第 %d 项 = %s/%s，期望 %s/%s", i, active[i].Kind, active[i].Channel, want[i].kind, want[i].channel)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	tbl := NewTable()
	kinds := []string{KindWatching, KindReplying, KindSending, KindGift, KindEditing}
	channels := []string{"live", "comment", "dm", "qq"}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kind := kinds[i%len(kinds)]
			channel := channels[i%len(channels)]
			for j := 0; j < 200; j++ {
				tbl.Set(State{Kind: kind, Channel: channel, Detail: "x", Since: time.Now().Add(-time.Duration(j%10) * time.Second), TTL: time.Minute})
				_ = tbl.Active(time.Now())
				_ = tbl.Summary(time.Now())
				if j%17 == 0 {
					tbl.Clear(kind, channel)
				}
			}
		}(i)
	}
	wg.Wait()
}
