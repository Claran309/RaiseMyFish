package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type scriptedViewerSummaryWorker struct {
	mu    sync.Mutex
	calls int
	seen  chan int
}

func (w *scriptedViewerSummaryWorker) RunOnce(context.Context) (viewer.SummaryStats, error) {
	w.mu.Lock()
	w.calls++
	n := w.calls
	w.mu.Unlock()
	select {
	case w.seen <- n:
	default:
	}
	return viewer.SummaryStats{Candidates: 2, Summarized: 1, Fallback: 1}, nil
}

func TestRunViewerSummaryImmediateAndStop(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	worker := &scriptedViewerSummaryWorker{seen: make(chan int, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 5ms 间隔：验证启动立即跑一轮且 ticker 会继续触发。
		runViewerSummary(ctx, worker, 5*time.Millisecond, viewerSummaryFreeze{}, nil, zap.New(core))
	}()
	for i := 1; i <= 2; i++ {
		select {
		case n := <-worker.seen:
			if n != i {
				cancel()
				<-done
				t.Fatalf("第 %d 轮实际收到 %d", i, n)
			}
		case <-time.After(2 * time.Second):
			cancel()
			<-done
			t.Fatalf("只执行了 %d 轮", i-1)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后侧写循环未退出")
	}
	if logs.FilterMessage("观众侧写完成").Len() < 2 {
		t.Fatalf("缺少完成日志：%v", logs.All())
	}
}

// TestStartViewerSummaryDisabled interval=0 只停调度：不装配、不记日志，已有摘要保留。
func TestStartViewerSummaryDisabled(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := config.Config{
		Memory: config.Memory{Enabled: true, ViewerSummaryIntervalSeconds: 0},
		Viewer: config.Viewer{Enabled: true},
	}
	stop := startViewerSummary(context.Background(), cfg, nil, knowledgeAssembly{}, "", nil, zap.New(core))
	stop()
	if logs.Len() != 0 {
		t.Fatalf("关闭时不应启动或记日志：%v", logs.All())
	}
}

func TestStartViewerSummaryInvalidInterval(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := config.Config{
		Memory: config.Memory{Enabled: true, ViewerSummaryIntervalSeconds: 30},
		Viewer: config.Viewer{Enabled: true},
	}
	stop := startViewerSummary(context.Background(), cfg, nil, knowledgeAssembly{}, "", nil, zap.New(core))
	stop()
	if logs.FilterMessage("观众侧写间隔无效，跳过").Len() == 0 {
		t.Fatalf("非法间隔未记跳过日志：%v", logs.All())
	}
}

// TestStartViewerSummaryHonorsSwitch R07：记忆停用/观众档案停用/配置冻结都不启动侧写。
func TestStartViewerSummaryHonorsSwitch(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "记忆停用",
			cfg: config.Config{
				Memory: config.Memory{Enabled: false, ViewerSummaryIntervalSeconds: 3600},
				Viewer: config.Viewer{Enabled: true},
			},
			want: "观众侧写停用：记忆提炼未启用",
		},
		{
			name: "观众档案停用",
			cfg: config.Config{
				Memory: config.Memory{Enabled: true, ViewerSummaryIntervalSeconds: 3600},
				Viewer: config.Viewer{Enabled: false},
			},
			want: "观众侧写停用：观众档案未启用",
		},
		{
			name: "配置冻结",
			cfg: config.Config{
				Memory: config.Memory{Enabled: true, Frozen: true, ViewerSummaryIntervalSeconds: 3600},
				Viewer: config.Viewer{Enabled: true},
			},
			want: "观众侧写停用：记忆已冻结",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.InfoLevel)
			// db=nil：门控必须先于装配生效，否则会记「装配失败」。
			stop := startViewerSummary(context.Background(), tc.cfg, nil, knowledgeAssembly{}, "", nil, zap.New(core))
			stop()
			if logs.FilterMessage(tc.want).Len() != 1 {
				t.Fatalf("缺少停用日志 %q：%v", tc.want, logs.All())
			}
			if logs.FilterMessage("观众侧写已启动").Len() != 0 {
				t.Fatalf("停用后仍启动：%v", logs.All())
			}
		})
	}
}

// TestRunViewerSummaryRespectsFreezeFile 运行时冻结文件存在时跳过；删除后下一轮自动恢复。
func TestRunViewerSummaryRespectsFreezeFile(t *testing.T) {
	dir := t.TempDir()
	freezePath := filepath.Join(dir, "memory.frozen")
	if err := memory.Freeze(freezePath); err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zapcore.InfoLevel)
	worker := &scriptedViewerSummaryWorker{seen: make(chan int, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runViewerSummary(ctx, worker, 5*time.Millisecond, viewerSummaryFreeze{path: freezePath}, nil, zap.New(core))
	}()
	select {
	case n := <-worker.seen:
		cancel()
		<-done
		t.Fatalf("冻结期间不应执行侧写：第 %d 轮", n)
	case <-time.After(60 * time.Millisecond):
	}
	if logs.FilterMessage("记忆已冻结，观众侧写本轮跳过").Len() == 0 {
		cancel()
		<-done
		t.Fatalf("冻结跳过未记日志：%v", logs.All())
	}
	if err := os.Remove(freezePath); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	select {
	case <-worker.seen:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("解冻后未恢复侧写")
	}
	cancel()
	<-done
}

// TestViewerSummaryFreezeErrors 读取冻结状态失败按跳过处理，不带着错误状态继续写。
func TestViewerSummaryFreezeErrors(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	worker := &scriptedViewerSummaryWorker{seen: make(chan int, 1)}
	g := viewerSummaryFreeze{
		path:     "state/memory.frozen",
		isFrozen: func(string) (bool, error) { return false, os.ErrPermission },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runViewerSummary(ctx, worker, 5*time.Millisecond, g, nil, zap.New(core))
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && logs.FilterMessage("读取记忆冻结状态失败，观众侧写本轮跳过").Len() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if len(worker.seen) != 0 {
		t.Fatal("冻结状态读取失败仍执行了侧写")
	}
	if logs.FilterMessage("读取记忆冻结状态失败，观众侧写本轮跳过").Len() == 0 {
		t.Fatalf("缺少读取失败日志：%v", logs.All())
	}
}

// TestViewerSummaryOptionsMapping R09 配置必须映射到侧写参数。
func TestViewerSummaryOptionsMapping(t *testing.T) {
	cfg := config.Config{
		Viewer: config.Viewer{MinHits: 4},
		Memory: config.Memory{
			ViewerSummaryMaxPerRun:        6,
			ViewerSummaryBackfillLimit:    1,
			ViewerSummaryMinNewMessages:   5,
			ViewerSummaryRecentTTLSeconds: 86400,
			ViewerSummaryDailyViewers:     20,
		},
	}
	opts := viewerSummaryOptions(cfg)
	if opts.MinHits != 4 || opts.MaxPerRun != 6 || opts.BackfillLimit != 1 ||
		opts.MinNewMessages != 5 || opts.RecentTTL != 24*time.Hour || opts.DailyViewers != 20 {
		t.Fatalf("侧写参数映射不符：%+v", opts)
	}
}

func TestNewViewerSummarizerNeedsDB(t *testing.T) {
	if _, _, err := newViewerSummarizer(config.Config{}, nil, knowledgeAssembly{}, nil); err == nil {
		t.Fatal("缺少数据库应拒绝装配")
	}
}

func viewerTestProfile() store.ViewerProfile {
	lastSeen := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return store.ViewerProfile{
		UID: "42", Name: "白饭哥", Hits: 12, Replies: 3, LastSeen: lastSeen,
		RecentTexts:    []string{"一", "二", "三", "四", "五", "六", "七", "八"},
		RecentReplies:  []string{"甲", "乙", "丙", "丁"},
		ProfileSummary: "性格：话痨；偏好：猫猫；口头禅：绷不住了；关系：常被鱼回；最近话题：白饭",
	}
}

func TestFormatViewerProfileIncludesSummary(t *testing.T) {
	got := formatViewerProfile(viewerTestProfile())
	if !strings.Contains(got, "【对方档案 · 老熟人】") || !strings.Contains(got, "说过 12 次") {
		t.Fatalf("档案头或统计丢失：%q", got)
	}
	if !strings.Contains(got, "- 侧写：性格：话痨；偏好：猫猫") {
		t.Fatalf("侧写未注入：%q", got)
	}
	// 有侧写时原始样本缩减为 5 条发言、3 条回复。
	if strings.Contains(got, "- 六") || strings.Contains(got, "- 丁") {
		t.Fatalf("有侧写时原始样本未缩减：%q", got)
	}
	if !strings.Contains(got, "- 五") || !strings.Contains(got, "- 丙") {
		t.Fatalf("缩减后样本缺失：%q", got)
	}
	if len([]rune(got)) > 400 {
		t.Fatalf("注入片段超过 400 字：%d", len([]rune(got)))
	}
}

func TestFormatViewerProfileWithoutSummary(t *testing.T) {
	profile := viewerTestProfile()
	profile.ProfileSummary = ""
	got := formatViewerProfile(profile)
	if strings.Contains(got, "侧写：") {
		t.Fatalf("空侧写不应显示：%q", got)
	}
	if !strings.Contains(got, "- 八") || !strings.Contains(got, "- 丁") {
		t.Fatalf("无侧写时应保留完整样本：%q", got)
	}
}

func TestFormatViewerProfileCompactsSummary(t *testing.T) {
	profile := viewerTestProfile()
	profile.ProfileSummary = "性格：话痨\r\n偏好：猫猫\n\n口头禅：" + strings.Repeat("绷", 300)
	got := formatViewerProfile(profile)
	if strings.ContainsAny(got, "\r") {
		t.Fatalf("侧写换行未折叠：%q", got)
	}
	if !strings.Contains(got, "性格：话痨 偏好：猫猫") {
		t.Fatalf("侧写空白未折叠：%q", got)
	}
	line := ""
	for _, part := range strings.Split(got, "\n") {
		if strings.HasPrefix(part, "- 侧写：") {
			line = strings.TrimPrefix(part, "- 侧写：")
		}
	}
	if n := len([]rune(line)); n == 0 || n > viewerSummaryInjectRunes {
		t.Fatalf("侧写长度不符：%d", n)
	}
}

// TestRunViewerSummaryPausedSkipsGeneration R08：暂停只跳过生成，恢复后下一轮自动继续。
func TestRunViewerSummaryPausedSkipsGeneration(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	worker := &scriptedViewerSummaryWorker{seen: make(chan int, 8)}
	state := &viewerSummaryRuntime{}
	state.SetPaused(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runViewerSummary(ctx, worker, 5*time.Millisecond, viewerSummaryFreeze{}, state, zap.New(core))
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && logs.FilterMessage("观众侧写已暂停，本轮跳过").Len() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if logs.FilterMessage("观众侧写已暂停，本轮跳过").Len() == 0 {
		cancel()
		<-done
		t.Fatalf("暂停未记跳过日志：%v", logs.All())
	}
	if len(worker.seen) != 0 {
		cancel()
		<-done
		t.Fatal("暂停期间仍执行了侧写")
	}
	state.SetPaused(false)
	select {
	case <-worker.seen:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("恢复后未继续侧写")
	}
	cancel()
	<-done
	if snap := state.snapshot(); snap.lastRunAt.IsZero() || snap.lastStats.Candidates != 2 {
		t.Fatalf("运行快照未记录：%+v", snap)
	}
}

// TestViewerSummaryRuntimeRecord 快照区分成功与失败：失败不清空最后成功时间。
func TestViewerSummaryRuntimeRecord(t *testing.T) {
	state := &viewerSummaryRuntime{}
	state.setRunning(true)
	first := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	state.record(viewer.SummaryStats{Candidates: 3, Summarized: 2}, nil, first)
	snap := state.snapshot()
	if !snap.running || !snap.lastRunAt.Equal(first) || !snap.lastSuccessAt.Equal(first) ||
		snap.lastStats.Candidates != 3 || snap.lastError != "" {
		t.Fatalf("成功快照不符：%+v", snap)
	}
	second := first.Add(time.Hour)
	state.record(viewer.SummaryStats{Candidates: 1, Failed: 1}, errors.New("模型暂时不可用"), second)
	snap = state.snapshot()
	if !snap.lastRunAt.Equal(second) || !snap.lastSuccessAt.Equal(first) || snap.lastError == "" ||
		snap.lastStats.Failed != 1 {
		t.Fatalf("失败快照不符：%+v", snap)
	}
	state.setRunning(false)
	if state.snapshot().running {
		t.Fatal("停止后仍标记运行中")
	}
}

// TestRuntimeControlViewerSummaryControl 面板控制面映射配置、快照与冻结状态。
func TestRuntimeControlViewerSummaryControl(t *testing.T) {
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	state := &viewerSummaryRuntime{}
	state.setRunning(true)
	state.record(viewer.SummaryStats{Candidates: 4, Summarized: 3, Fallback: 1, BusySkipped: 2}, nil, at)
	cfg := config.Config{
		Memory: config.Memory{Enabled: true, ViewerSummaryIntervalSeconds: 3600},
		Viewer: config.Viewer{Enabled: true},
	}
	c := &runtimeControl{
		base: cfg, log: zap.NewNop(), now: func() time.Time { return at },
		viewerSummary: state,
	}
	st := c.ViewerSummaryStatus(context.Background())
	if !st.Enabled {
		t.Fatalf("启用状态不符：%+v", st)
	}
	if !st.Running || st.Paused || st.IntervalSeconds != 3600 ||
		st.LastSuccessAt != "2026-11-10T12:00:00Z" || st.LastRun.Candidates != 4 ||
		st.LastRun.BusySkipped != 2 {
		t.Fatalf("运行快照映射不符：%+v", st)
	}
	// 没有 DB 仓储：可用性为 false 且给出安全错误文案。
	if st.Available || st.Error == "" {
		t.Fatalf("缺少仓储应标记不可用：%+v", st)
	}

	if err := c.SetViewerSummaryPaused(true); err != nil || !state.Paused() {
		t.Fatalf("暂停失败：%v", err)
	}
	if err := c.SetViewerSummaryPaused(false); err != nil || state.Paused() {
		t.Fatalf("恢复失败：%v", err)
	}
	if err := c.RebuildViewerSummary(context.Background(), "42"); err == nil {
		t.Fatal("缺少仓储时重建应报错")
	}
	if err := c.RevokeViewerSummary(context.Background(), "42"); err == nil {
		t.Fatal("缺少仓储时撤回应报错")
	}

	// 配置冻结优先于运行时间隔。
	frozen := c
	frozen.base.Memory.Frozen = true
	frozenSt := frozen.ViewerSummaryStatus(context.Background())
	if !frozenSt.Frozen || frozenSt.FrozenReason != "config" {
		t.Fatalf("配置冻结未反映：%+v", frozenSt)
	}
	// 记忆/观众档案任一停用都视为未启用（R07）。
	disabled := c
	disabled.base.Viewer.Enabled = false
	if disabled.ViewerSummaryStatus(context.Background()).Enabled {
		t.Fatal("观众档案停用后仍标记启用")
	}
}
