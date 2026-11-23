package scheduler

import (
	"context"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"golang.org/x/time/rate"
)

// TestChannelConcurrencyQuota 通道软配额生效：live 满了不再拉第二条 live，
// 但其他通道仍能用剩余全局并发。
func TestChannelConcurrencyQuota(t *testing.T) {
	s, b, _, clock := fixture(t)
	s.options.MaxConcurrent = 4
	s.policies[event.ChLive].MaxConcurrent = 1
	s.policies[event.ChDM] = &policyState{ChannelPolicy: ChannelPolicy{
		Private: true, Brain: "openai", RatePerMinute: 600, Burst: 10,
		DailyQuota: 100, MinRunes: 1, RepeatWindow: time.Minute, MaxConcurrent: 1,
	}, limiter: rate.NewLimiter(rate.Limit(10), 10)}
	now := clock.Now()
	live1 := claimFor("live:r1:a", "r1", "第一条弹幕", now)
	live2 := claimFor("live:r2:b", "r2", "第二条弹幕", now.Add(time.Second))
	dm := claimFor("dm:u:1", "u", "私信内容", now.Add(2*time.Second))
	dm.Event.Channel = event.ChDM
	dm.Event.Private = true
	b.add(live1, live2, dm)
	for _, c := range []event.Claim{live1, live2, dm} {
		if err := s.add(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	liveActive, dmActive := 0, 0
	for _, job := range s.active {
		switch job.work.Claim.Event.Channel {
		case event.ChLive:
			liveActive++
		case event.ChDM:
			dmActive++
		}
	}
	if liveActive != 1 || dmActive != 1 {
		t.Fatalf("软配额未生效：live=%d dm=%d active=%d", liveActive, dmActive, len(s.active))
	}
	drainDone(t, s)
}

// TestBackfillRunsAfterFresh 历史补采优先级低于实时事件。
func TestBackfillRunsAfterFresh(t *testing.T) {
	s, b, _, clock := fixture(t)
	s.options.MaxConcurrent = 1
	s.policies[event.ChLive].MaxConcurrent = 0
	now := clock.Now()
	backfill := claimFor("live:r:backfill", "r", "旧弹幕补采", now)
	backfill.Event.CollectMode = "backfill"
	fresh := claimFor("live:r:fresh", "r", "刚收到的弹幕", now.Add(time.Second))
	b.add(backfill, fresh)
	// 故意先加 backfill，验证排序不是「先到先得」。
	if err := s.add(context.Background(), backfill); err != nil {
		t.Fatal(err)
	}
	if err := s.add(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.active) != 1 {
		t.Fatalf("并发上限未生效：%d", len(s.active))
	}
	for _, job := range s.active {
		if job.work.Claim.Event.ID != fresh.Event.ID {
			t.Fatalf("先跑了 backfill：%+v", job.work.Claim.Event)
		}
	}
	drainDone(t, s)
}

// mergeRepo 在 fakeRepo 上补「同用户更新事件」判断。
type mergeRepo struct {
	*fakeRepo
	newer bool
}

func (m mergeRepo) NewerSameUser(context.Context, event.Claim, time.Duration) (bool, error) {
	return m.newer, nil
}

// TestMergeWindowSkipsEarlier 同用户短窗口内后一条待处理时，较早事件直接 skip。
func TestMergeWindowSkipsEarlier(t *testing.T) {
	s, b, _, clock := fixture(t)
	s.repo = mergeRepo{fakeRepo: s.repo.(*fakeRepo), newer: true}
	s.policies[event.ChLive].MergeWindow = 30 * time.Second
	now := clock.Now()
	a := claimFor("live:r:first", "r", "第一条", now)
	b.add(a)
	if err := s.add(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.marked(a.Event.ID) != event.StatusSkipped {
		t.Fatalf("较早事件未被合并跳过：%s", b.marked(a.Event.ID))
	}
	if len(s.active) != 0 {
		t.Fatalf("合并后仍派发：%d", len(s.active))
	}
}

// TestMergeWindowOffKeepsOldBehavior 关闭窗口时正常派发。
func TestMergeWindowOffKeepsOldBehavior(t *testing.T) {
	s, b, _, clock := fixture(t)
	s.repo = mergeRepo{fakeRepo: s.repo.(*fakeRepo), newer: true}
	a := claimFor("live:r:only", "r", "唯一一条", clock.Now())
	b.add(a)
	if err := s.add(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := s.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.active) != 1 {
		t.Fatalf("关闭合并后未派发：%d", len(s.active))
	}
	drainDone(t, s)
}
