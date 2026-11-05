package main

// 本文件测运行时控制面：房间/oid/mid 解析、采集器启停与重建、后台循环生命周期。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"RaiseMyFish/internal/collector"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/presence"
	"go.uber.org/zap"
)

// recordingBus 只实现 Publish；控制面测试不跑调度。
type recordingBus struct {
	mu   sync.Mutex
	seen []event.Event
}

func (f *recordingBus) Publish(_ context.Context, ev event.Event) (bool, error) {
	f.mu.Lock()
	f.seen = append(f.seen, ev)
	f.mu.Unlock()
	return true, nil
}

func (f *recordingBus) ClaimPending(context.Context, int) ([]event.Claim, error) { return nil, nil }
func (f *recordingBus) Mark(context.Context, event.Claim, []event.Status, event.Status) error {
	return nil
}
func (f *recordingBus) Defer(context.Context, event.Claim, time.Time) error { return nil }
func (f *recordingBus) Renew(context.Context, event.Claim) (event.Claim, error) {
	return event.Claim{}, nil
}
func (f *recordingBus) Recover(context.Context, int) (int, error) { return 0, nil }
func (f *recordingBus) Notify() <-chan struct{}                   { return nil }

func (f *recordingBus) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// fakeCollector 发一条事件后阻塞到取消，模拟长跑采集。
type fakeCollector struct {
	name    string
	publish bool
	started atomic.Int32
}

func (f *fakeCollector) Name() string { return f.name }

func (f *fakeCollector) Start(ctx context.Context, out chan<- event.Event, _ chan<- error) error {
	f.started.Add(1)
	if f.publish {
		select {
		case out <- event.Event{ID: f.name + ":1", Channel: event.ChLive}:
		case <-ctx.Done():
			return nil
		}
	}
	<-ctx.Done()
	return nil
}

func (f *fakeCollector) Health() collector.Health { return collector.Health{OK: true} }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func TestParseRoomID(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"30308220", "30308220", true},
		{"0030308220", "30308220", true},
		{" https://live.bilibili.com/30308220?spm=1 ", "30308220", true},
		{"live.bilibili.com/30308220", "30308220", true},
		{"https://foo.live.bilibili.com/123/news", "123", true},
		{"", "", false},
		{"0", "", false},
		{"https://live.bilibili.com/000", "", false},
		{"https://example.com/123", "", false},
		{"not a room", "", false},
	}
	for _, tc := range cases {
		got, err := parseRoomID(tc.raw)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("%q = %q, %v；期望 %q", tc.raw, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("%q 应报错，实际 %q", tc.raw, got)
		}
	}
}

func TestNormalizeVideos(t *testing.T) {
	got, err := normalizeVideos([]string{" 12 ", "0012", "34"})
	if err != nil || len(got) != 2 || got[0] != "12" || got[1] != "34" {
		t.Fatalf("规范化不符：%v %v", got, err)
	}
	for _, bad := range [][]string{{}, {"abc"}, {"0"}} {
		if _, err := normalizeVideos(bad); err == nil {
			t.Errorf("%v 应报错", bad)
		}
	}
}

func TestNormalizeMIDs(t *testing.T) {
	got, err := normalizeMIDs([]string{" 42 ", "42", "", "77"})
	if err != nil || len(got) != 2 || got[0] != "42" || got[1] != "77" {
		t.Fatalf("规范化不符：%v %v", got, err)
	}
	for _, bad := range [][]string{{}, {"abc", "1"}} {
		if _, err := normalizeMIDs(bad); err == nil {
			t.Errorf("%v 应报错", bad)
		}
	}
}

func TestInitialAllowedMIDs(t *testing.T) {
	cfg := config.Config{Sender: &config.Sender{DM: config.DMSender{AllowedMIDs: []string{"42"}}}}
	got := initialAllowedMIDs(cfg)
	if len(got) != 2 || got[0] != "42" || got[1] != defaultOwnerMID {
		t.Fatalf("白名单应含配置项与主人：%v", got)
	}
	if got := initialAllowedMIDs(config.Config{}); len(got) != 1 || got[0] != defaultOwnerMID {
		t.Fatalf("无配置时只保留主人：%v", got)
	}
}

// TestCollectorControllerRestartsWithNewInstance 采集器停掉后必须新建实例再启动。
func TestCollectorControllerRestartsWithNewInstance(t *testing.T) {
	bus := &recordingBus{}
	initial := &fakeCollector{name: "live", publish: true}
	var factoryCalls atomic.Int32
	ctl := newCollectorController("live", context.Background(), bus, time.Second, zap.NewNop(), time.Now,
		initial, func() (collector.Collector, error) {
			factoryCalls.Add(1)
			return &fakeCollector{name: "live", publish: true}, nil
		})

	if err := ctl.set(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return ctl.status().Running && bus.count() == 1 })
	if st := ctl.status(); !st.Enabled || !st.Running || !st.Available || st.LastError != "" {
		t.Fatalf("启动后状态不符：%+v", st)
	}
	if initial.started.Load() != 1 {
		t.Fatalf("初始实例应被消费一次：%d", initial.started.Load())
	}
	if err := ctl.set(false); err != nil {
		t.Fatal(err)
	}
	if st := ctl.status(); st.Running || st.Enabled {
		t.Fatalf("停止后状态不符：%+v", st)
	}
	if err := ctl.set(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return ctl.status().Running && bus.count() == 2 })
	if factoryCalls.Load() != 1 {
		t.Fatalf("重启应新建实例一次，实际 %d", factoryCalls.Load())
	}
	_ = ctl.set(false)
}

// TestLiveCollectorStartResetsRoomAndWatching 采集启停必须同步权威房间并清掉旧 watching。
func TestLiveCollectorStartResetsRoomAndWatching(t *testing.T) {
	cfg := config.Config{}
	cfg.Collector.Live.RoomID = "222"
	rooms := &liveRoomState{}
	table := presence.NewTable()
	table.Set(presence.State{Kind: presence.KindWatching, Channel: string(event.ChLive), Detail: "111", TTL: time.Hour})

	control, err := newRuntimeControl(context.Background(), cfg, runtimeDeps{
		Bus:      &recordingBus{},
		Timeout:  time.Second,
		Live:     &fakeCollector{name: "live"},
		Rooms:    rooms,
		Presence: table,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	if err := control.SetLiveCollect(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rooms.Current() == "222" })
	if got := table.Active(time.Now()); len(got) != 0 {
		t.Fatalf("启动采集后旧 watching 应被清空：%+v", got)
	}

	if err := control.SetLiveCollect(false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rooms.Current() == "" })
	if got := table.Active(time.Now()); len(got) != 0 {
		t.Fatalf("停止采集后不应残留 watching：%+v", got)
	}
}

func TestCollectorControllerFactoryError(t *testing.T) {
	ctl := newCollectorController("dm", context.Background(), &recordingBus{}, time.Second, zap.NewNop(), time.Now, nil,
		func() (collector.Collector, error) { return nil, errors.New("未配置凭据") })
	err := ctl.set(true)
	if err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("应返回装配错误：%v", err)
	}
	st := ctl.status()
	if st.Enabled || st.Running || st.LastError == "" {
		t.Fatalf("失败后状态不符：%+v", st)
	}
}

func TestBackgroundControllerLifecycle(t *testing.T) {
	var starts, stops atomic.Int32
	ctl := newBackgroundController("test", context.Background(), zap.NewNop(), true,
		func(context.Context) (func(), error) {
			starts.Add(1)
			return func() { stops.Add(1) }, nil
		})
	if err := ctl.set(true); err != nil {
		t.Fatal(err)
	}
	if err := ctl.set(true); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || !ctl.status().Running {
		t.Fatalf("重复启动应幂等：starts=%d status=%+v", starts.Load(), ctl.status())
	}
	if err := ctl.set(false); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 1 || ctl.status().Running {
		t.Fatalf("停止不符：stops=%d status=%+v", stops.Load(), ctl.status())
	}
	if err := ctl.set(true); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 2 {
		t.Fatalf("再次启动应重新装配：%d", starts.Load())
	}
	_ = ctl.set(false)

	failing := newBackgroundController("bad", context.Background(), zap.NewNop(), true,
		func(context.Context) (func(), error) { return nil, errors.New("装配失败") })
	if err := failing.set(true); err == nil {
		t.Fatal("启动失败应返回错误")
	}
	if st := failing.status(); st.Enabled || st.LastError != "装配失败" {
		t.Fatalf("失败状态不符：%+v", st)
	}
}

func TestWatchControllerRejectsBadRoom(t *testing.T) {
	ctl := &watchController{ctx: context.Background(), log: zap.NewNop(), now: time.Now}
	if err := ctl.set(true, "https://example.com/1"); err == nil {
		t.Fatal("非 B 站链接应报错")
	}
	st := ctl.status()
	if st.Enabled || st.Running || st.LastError == "" {
		t.Fatalf("失败状态不符：%+v", st)
	}
	if err := ctl.set(false, ""); err != nil {
		t.Fatalf("关闭应幂等：%v", err)
	}
}

func TestProactiveControllerRejectsBadInterval(t *testing.T) {
	ctl := &proactiveController{ctx: context.Background(), log: zap.NewNop(), now: time.Now}
	if err := ctl.set(true, 2); err == nil {
		t.Fatal("间隔过小应报错")
	}
	if err := ctl.set(false, 0); err != nil {
		t.Fatalf("关闭应幂等：%v", err)
	}
}

// TestRuntimeControlStatus GiftGate/白名单/看门狗状态字段必须同步。
func TestRuntimeControlStatus(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		Runtime: config.Runtime{StateFile: filepath.Join(dir, "status.json")},
		Gift:    config.Gift{Enabled: true, GiftID: "31036", DailyLimit: 5, MaxNum: 2},
		Watchdog: config.Watchdog{
			Enabled: true, StaleSeconds: 60,
			HeartbeatPath: filepath.Join(dir, "heartbeat.json"),
			HistoryPath:   filepath.Join(dir, "history.jsonl"),
		},
		Sender: &config.Sender{DM: config.DMSender{AllowedMIDs: []string{"42"}}},
	}
	cfg.Collector.Comment.AutoDiscover = true
	gate := &atomic.Bool{}
	control, err := newRuntimeControl(context.Background(), cfg, runtimeDeps{
		Bus:         &recordingBus{},
		Timeout:     time.Second,
		GiftGate:    gate,
		Now:         time.Now,
		DMWhitelist: nil,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Load() {
		t.Fatal("礼物开关应按配置初始化")
	}
	control.Close()

	st := control.Status()
	if !st.Live.Available || st.Live.Enabled {
		t.Errorf("直播采集状态不符：%+v", st.Live)
	}
	if len(st.DM.AllowedMIDs) != 2 || st.DM.WhitelistAvailable || !st.DM.AllFollowers {
		t.Errorf("私信状态不符：%+v", st.DM)
	}
	if !st.Comment.AutoDiscover {
		t.Errorf("评论自动发现状态不符：%+v", st.Comment)
	}
	if !st.Gift.Enabled || st.Gift.DailyLimit != 5 || st.Gift.Day == "" {
		t.Errorf("礼物状态不符：%+v", st.Gift)
	}
	if st.Gift.Error != "" {
		t.Errorf("额度账本应可用：%s", st.Gift.Error)
	}
	if st.Memory.Frozen {
		t.Error("默认不应冻结")
	}
	if !st.Watchdog.Enabled || st.Watchdog.StaleSeconds != 60 || st.Watchdog.RestartCommandConfigured {
		t.Errorf("看门狗状态不符：%+v", st.Watchdog)
	}
	if st.Watchdog.Heartbeat.Exists {
		t.Error("无心跳文件时不应标记存在")
	}
	if err := control.SetGift(false); err != nil || control.Status().Gift.Enabled {
		t.Fatalf("关闭礼物开关失败：%v", err)
	}
}

// TestSetCommentAllowsEmptyVideosWhenAutoDiscover 自动发现开启时允许手动列表为空。
func TestSetCommentAllowsEmptyVideosWhenAutoDiscover(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	build := func(auto bool) *runtimeControl {
		cfg := config.Config{}
		cfg.Collector.Comment.AutoDiscover = auto
		ctrl := &runtimeControl{base: cfg, log: zap.NewNop(), now: time.Now}
		ctrl.comment = newCollectorController("comment", ctx, &recordingBus{}, time.Second, zap.NewNop(), time.Now,
			&fakeCollector{name: "comment-test"}, nil)
		return ctrl
	}

	ctrl := build(true)
	if err := ctrl.SetComment(true, nil); err != nil {
		t.Fatalf("自动发现开启时应允许空视频列表：%v", err)
	}
	if !ctrl.comment.isRunning() {
		t.Error("采集器应已启动")
	}
	if err := ctrl.comment.set(false); err != nil {
		t.Fatal(err)
	}

	strict := build(false)
	if err := strict.SetComment(true, nil); err == nil {
		t.Error("关闭自动发现时空视频列表应报错")
	}
}

// TestRuntimeControlCloseIdempotent Close 不能因内部锁自锁而卡死。
func TestRuntimeControlCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{Runtime: config.Runtime{StateFile: filepath.Join(dir, "status.json")}}
	control, err := newRuntimeControl(context.Background(), cfg, runtimeDeps{Bus: &recordingBus{}, Timeout: time.Second}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		control.Close()
		control.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 卡死")
	}
}
