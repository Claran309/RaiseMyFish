package extractworker

// 本文件测观众档案子任务的节流与活跃判定。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/store"
)

type fakeProfiles struct {
	active       bool
	profiles     int
	revoked      []string
	activeErr    error
	rebuildErr   error
	activeCalls  int
	rebuildCalls int
}

func (f *fakeProfiles) ActiveSince(context.Context, time.Time) (bool, error) {
	f.activeCalls++
	return f.active, f.activeErr
}

func (f *fakeProfiles) RebuildProfiles(context.Context, store.ViewerRebuildOptions) (store.ViewerRebuildStats, error) {
	f.rebuildCalls++
	return store.ViewerRebuildStats{Profiles: f.profiles, Revoked: f.revoked}, f.rebuildErr
}

type fakeProjection struct {
	purged []string
	err    error
	calls  int
}

func (f *fakeProjection) PurgeViewers(_ context.Context, uids []string) error {
	f.calls++
	f.purged = append(f.purged, uids...)
	return f.err
}

func newProfileWorker(p ProfileSource, now *time.Time) *Worker {
	return &Worker{opts: Options{
		Profiles:             p,
		ProfileInterval:      time.Hour,
		ProfileMinHits:       2,
		ProfileRecentTexts:   15,
		ProfileRecentReplies: 10,
		ProfileActiveWindow:  5 * time.Minute,
		Now:                  func() time.Time { return *now },
	}}
}

// TestRebuildProfilesThrottled 一小时内只跑一次。
func TestRebuildProfilesThrottled(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	src := &fakeProfiles{active: true, profiles: 3}
	w := newProfileWorker(src, &now)

	var first Stats
	w.rebuildProfiles(context.Background(), &first)
	if first.ProfilesRebuilt != 3 || src.rebuildCalls != 1 {
		t.Fatalf("首轮应重建 3 个：%+v calls=%d", first, src.rebuildCalls)
	}

	// 30 分钟后：被节流
	now = now.Add(30 * time.Minute)
	var second Stats
	w.rebuildProfiles(context.Background(), &second)
	if second.ProfileSkipped != "throttled" || src.rebuildCalls != 1 {
		t.Fatalf("半小时内应节流：%+v calls=%d", second, src.rebuildCalls)
	}

	// 满一小时后：再跑
	now = now.Add(31 * time.Minute)
	var third Stats
	w.rebuildProfiles(context.Background(), &third)
	if third.ProfilesRebuilt != 3 || src.rebuildCalls != 2 {
		t.Fatalf("满一小时应重建：%+v calls=%d", third, src.rebuildCalls)
	}
}

// TestRebuildProfilesIdle 直播不活跃（最近 5 分钟无 in 事件）不跑。
func TestRebuildProfilesIdle(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	src := &fakeProfiles{active: false}
	w := newProfileWorker(src, &now)

	var stats Stats
	w.rebuildProfiles(context.Background(), &stats)
	if stats.ProfileSkipped != "idle" || src.rebuildCalls != 0 {
		t.Fatalf("不活跃时不应重建：%+v calls=%d", stats, src.rebuildCalls)
	}
	// 不活跃不算一次执行：之后活跃了应立刻能跑
	src.active = true
	now = now.Add(time.Minute)
	var second Stats
	w.rebuildProfiles(context.Background(), &second)
	if src.rebuildCalls != 1 {
		t.Fatalf("活跃后应立即重建：calls=%d", src.rebuildCalls)
	}
}

// TestRebuildProfilesFailureKeepsThrottle 重建失败也记节流时间，避免每轮重试打库。
func TestRebuildProfilesFailureKeepsThrottle(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	src := &fakeProfiles{active: true, rebuildErr: errors.New("db down")}
	w := newProfileWorker(src, &now)

	var stats Stats
	w.rebuildProfiles(context.Background(), &stats)
	if stats.ProfileSkipped != "failed" {
		t.Fatalf("失败应标记 failed：%+v", stats)
	}
	now = now.Add(time.Minute)
	var second Stats
	w.rebuildProfiles(context.Background(), &second)
	if second.ProfileSkipped != "throttled" || src.rebuildCalls != 1 {
		t.Fatalf("失败后应节流：%+v calls=%d", second, src.rebuildCalls)
	}
}

// TestRebuildProfilesNilSource 没装配档案源时不跑、不报错。
func TestRebuildProfilesNilSource(t *testing.T) {
	now := time.Now()
	w := newProfileWorker(nil, &now)
	var stats Stats
	w.rebuildProfiles(context.Background(), &stats)
	if stats.ProfileSkipped != "none" || stats.ProfilesRebuilt != -1 {
		t.Fatalf("未装配时应跳过：%+v", stats)
	}
}

// TestRebuildProfilesPurgesRevoked 撤回档案后同步清理公开知识条目；失败不影响重建结果。
func TestRebuildProfilesPurgesRevoked(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	src := &fakeProfiles{active: true, profiles: 2, revoked: []string{"42", "43"}}
	projection := &fakeProjection{}
	w := newProfileWorker(src, &now)
	w.opts.Projection = projection

	var stats Stats
	w.rebuildProfiles(context.Background(), &stats)
	if stats.ProfilesRebuilt != 2 || stats.ProfilesRevoked != 2 || stats.ProfileProjection != "done" {
		t.Fatalf("撤回统计不符：%+v", stats)
	}
	if projection.calls != 1 || len(projection.purged) != 2 || projection.purged[0] != "42" {
		t.Fatalf("未清理撤回观众的知识条目：%+v", projection)
	}

	// 投影失败：重建结果保留，只标记 failed，下轮由侧写循环补投。
	now = now.Add(2 * time.Hour)
	projection.err = errors.New("文件坏了")
	src.revoked = []string{"44"}
	var second Stats
	w.rebuildProfiles(context.Background(), &second)
	if second.ProfilesRebuilt != 2 || second.ProfilesRevoked != 1 || second.ProfileProjection != "failed" {
		t.Fatalf("投影失败不应影响重建：%+v", second)
	}

	// 没装配投影：照样记录撤回数量，标记 none。
	now = now.Add(2 * time.Hour)
	w.opts.Projection = nil
	src.revoked = []string{"45"}
	var third Stats
	w.rebuildProfiles(context.Background(), &third)
	if third.ProfilesRevoked != 1 || third.ProfileProjection != "none" {
		t.Fatalf("未装配投影时统计不符：%+v", third)
	}

	// 没有撤回：不调用投影。
	now = now.Add(2 * time.Hour)
	src.revoked = nil
	projection2 := &fakeProjection{}
	w.opts.Projection = projection2
	var fourth Stats
	w.rebuildProfiles(context.Background(), &fourth)
	if fourth.ProfilesRevoked != 0 || projection2.calls != 0 {
		t.Fatalf("无撤回不应调用投影：%+v calls=%d", fourth, projection2.calls)
	}
}
