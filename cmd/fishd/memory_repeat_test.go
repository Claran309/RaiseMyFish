package main

// 本文件测 #93 重复度触发的 fishd 装配：阈值开关、别名适配、自名过滤与 worker 接线。

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/memory/extractworker"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
)

// repeatTestEvents 模拟公开事件窗口查询。
type repeatTestEvents struct {
	events []event.Event
}

func (f *repeatTestEvents) PublicIncomingSince(_ context.Context, since time.Time, limit int) ([]event.Event, error) {
	out := make([]event.Event, 0, len(f.events))
	for _, ev := range f.events {
		if ev.At.Before(since) {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// repeatTestFacts 是 store.RepeatFactStore 的桩，记录写入次数。
type repeatTestFacts struct {
	records  []store.MemoryRecord
	inserted int
}

func (f *repeatTestFacts) InsertRecord(_ context.Context, rec store.MemoryRecord) (bool, error) {
	if err := rec.Validate(); err != nil {
		return false, err
	}
	f.records = append(f.records, rec)
	f.inserted++
	return true, nil
}

func (f *repeatTestFacts) RepeatFactByTitle(context.Context, string) (store.RepeatFactState, bool, error) {
	return store.RepeatFactState{}, false, nil
}

func (f *repeatTestFacts) DownweightFact(context.Context, string, float64, float64) (bool, error) {
	return false, nil
}

// repeatTestAliases 模拟 viewer.AliasSource 的热更新快照。
type repeatTestAliases struct {
	index viewer.AliasIndex
}

func (f repeatTestAliases) AliasIndex() viewer.AliasIndex { return f.index }

// TestBuildRepeatPromotionDisabled 阈值关闭时不装配。
func TestBuildRepeatPromotionDisabled(t *testing.T) {
	got := buildRepeatPromotion(config.Config{Memory: config.Memory{RepeatPromoteThreshold: 0}}, &repeatTestEvents{}, nil, &repeatTestFacts{}, nil, zap.NewNop())
	if got != nil {
		t.Fatalf("阈值 0 应关闭，实际 %+v", got)
	}
}

// TestBuildRepeatPromotionWiresParams 装配参数与别名适配正确。
func TestBuildRepeatPromotionWiresParams(t *testing.T) {
	aliases := repeatTestAliases{index: viewer.AliasIndex{Confirmed: []viewer.Alias{
		{Alias: "奇迹鱼", UID: "9009", Name: "X奇迹与你X"},
		{Alias: "仙主", UID: "7007", Name: "终南仙主"},
	}}}
	cfg := config.Config{
		Memory: config.Memory{RepeatPromoteThreshold: 5},
		Scheduler: &config.Scheduler{Channels: map[event.Channel]config.ChannelPolicy{
			event.ChLive:    {SelfNames: []string{"大肥鱼", " 鱼鱼 "}},
			event.ChComment: {SelfNames: []string{"大肥鱼", ""}},
		}},
	}
	facts := &repeatTestFacts{}
	got := buildRepeatPromotion(cfg, &repeatTestEvents{}, nil, facts, aliases, zap.NewNop())
	if got == nil {
		t.Fatal("阈值 5 应装配")
	}
	if got.Threshold != 5 || got.Facts != facts {
		t.Errorf("参数未接线：%+v", got)
	}
	if got.Window != memory.RepeatWindowDays*24*time.Hour || got.Debounce != 24*time.Hour {
		t.Errorf("窗口/防抖不符：window=%v debounce=%v", got.Window, got.Debounce)
	}
	if got.Aliases == nil {
		t.Fatal("别名来源应接线")
	}
	confirmed := got.Aliases.ConfirmedAliases()
	if len(confirmed) != 2 || confirmed[0].Alias != "奇迹鱼" || confirmed[0].UID != "9009" || confirmed[0].Name != "X奇迹与你X" {
		t.Errorf("别名转换不符：%+v", confirmed)
	}
	// 自名去重 + 去空白，用来把「叫鱼」排除出观众称呼统计。
	ignore := map[string]bool{}
	for _, name := range got.IgnoreNames {
		ignore[name] = true
	}
	if len(got.IgnoreNames) != 2 || !ignore["大肥鱼"] || !ignore["鱼鱼"] {
		t.Errorf("自名过滤不符：%+v", got.IgnoreNames)
	}
}

// TestBuildRepeatPromotionRequiresCoreDeps 事件或事实仓储缺失时降级为不跑。
func TestBuildRepeatPromotionRequiresCoreDeps(t *testing.T) {
	cfg := config.Config{Memory: config.Memory{RepeatPromoteThreshold: 5}}
	if got := buildRepeatPromotion(cfg, nil, nil, &repeatTestFacts{}, nil, zap.NewNop()); got != nil {
		t.Errorf("缺少事件来源应返回 nil：%+v", got)
	}
	if got := buildRepeatPromotion(cfg, &repeatTestEvents{}, nil, nil, nil, zap.NewNop()); got != nil {
		t.Errorf("缺少事实仓储应返回 nil：%+v", got)
	}
}

// TestRepeatAliasAdapterNilSource 没有别名表时返回空，不 panic。
func TestRepeatAliasAdapterNilSource(t *testing.T) {
	var adapter repeatAliasAdapter
	if got := adapter.ConfirmedAliases(); got != nil {
		t.Errorf("空适配器应返回 nil：%+v", got)
	}
}

// TestRepeatIgnoreNamesNilScheduler 没有 [scheduler] 段时没有自名可过滤。
func TestRepeatIgnoreNamesNilScheduler(t *testing.T) {
	if got := repeatIgnoreNames(nil); got != nil {
		t.Errorf("nil 调度配置应返回 nil：%+v", got)
	}
}

// TestNewMemoryWorkerWiresRepeatPromotion deps.Repeats 必须真的进 worker：
// 一轮 RunOnce 后写出一条称呼事实。
func TestNewMemoryWorkerWiresRepeatPromotion(t *testing.T) {
	now := time.Now().UTC().Add(-time.Hour)
	events := &repeatTestEvents{}
	for i := 1; i <= 5; i++ {
		n := strconv.Itoa(i)
		events.events = append(events.events, event.Event{
			ID: "live:room:" + n, Channel: event.ChLive, Dir: event.DirIn,
			Session: "room", FromID: "uid-" + n, FromName: "路人",
			Text: "奇迹鱼来了", At: now.Add(time.Duration(i) * time.Minute),
		})
	}
	facts := &repeatTestFacts{}
	worker, err := newMemoryWorker(memoryWorkerDeps{
		Config:        config.Memory{Enabled: true, IntervalSeconds: 300, RepeatPromoteThreshold: 5},
		Source:        &memoryTestSource{},
		Repo:          &memoryTestRepo{},
		WatermarkPath: filepath.Join(t.TempDir(), "watermark.json"),
		Repeats: &extractworker.RepeatPromotion{
			Events:    events,
			Aliases:   repeatAliasAdapter{source: repeatTestAliases{index: viewer.AliasIndex{Confirmed: []viewer.Alias{{Alias: "奇迹鱼", UID: "9009", Name: "X奇迹与你X"}}}}},
			Facts:     facts,
			Threshold: 5,
			Window:    memory.RepeatWindowDays * 24 * time.Hour,
			Debounce:  24 * time.Hour,
		},
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	stats, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.RepeatInserted != 1 || facts.inserted != 1 {
		t.Fatalf("重复度未接线：stats=%+v facts=%d", stats, facts.inserted)
	}
	if len(facts.records) != 1 || facts.records[0].Title != "称呼：奇迹鱼 = X奇迹与你X" {
		t.Errorf("写入事实不符：%+v", facts.records)
	}
}
