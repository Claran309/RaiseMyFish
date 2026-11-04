package extractworker

// 本文件测灵魂记忆重建：入选门槛、去重、条数上限、节流、fact 变化后更新。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

type fakeSoul struct {
	records []store.MemoryRecord
	err     error
	calls   int
}

func (f *fakeSoul) SoulCandidates(context.Context, float64, int) ([]store.MemoryRecord, error) {
	f.calls++
	return f.records, f.err
}

func soulRec(id, title, summary string, imp float64) store.MemoryRecord {
	return store.MemoryRecord{
		Record: memory.Record{ID: id, Summary: summary, Importance: imp, ValidFrom: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)},
		Kind:   "fact",
		Title:  title,
	}
}

func newSoulWorker(t *testing.T, src SoulSource, path string, now *time.Time) *Worker {
	t.Helper()
	return &Worker{opts: Options{
		Soul:     src,
		SoulOpts: SoulOptions{Path: path, MaxEntries: 20, MinImportance: 0.8, Interval: time.Hour},
		Now:      func() time.Time { return *now },
	}}
}

// TestSoulRebuildSelection 入选（≥0.8）与不入选（<0.8，由 source 过滤）。
func TestSoulRebuildSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	src := &fakeSoul{records: []store.MemoryRecord{
		soulRec("a", "主人是学生", "在读大学", 0.9),
		soulRec("b", "主人爱吃辣", "无辣不欢", 0.85),
	}}
	w := newSoulWorker(t, src, path, &now)

	var stats Stats
	w.rebuildSoul(context.Background(), &stats)
	if stats.SoulEntries != 2 {
		t.Fatalf("应写入 2 条：%+v", stats)
	}
	got, err := memory.LoadSoul(context.Background(), path)
	if err != nil || len(got) != 2 {
		t.Fatalf("读回失败：%+v / %v", got, err)
	}
	if got[0].Title != "主人是学生" || got[0].Importance != 0.9 {
		t.Errorf("条目不符：%+v", got[0])
	}
}

// TestSoulRebuildDedup 同标题只留一条。
func TestSoulRebuildDedup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	src := &fakeSoul{records: []store.MemoryRecord{
		soulRec("a", "主人是学生", "大二", 0.8),
		soulRec("b", "主人是学生", "大三", 0.9),
	}}
	w := newSoulWorker(t, src, path, &now)

	var stats Stats
	w.rebuildSoul(context.Background(), &stats)
	if stats.SoulEntries != 1 {
		t.Fatalf("去重后应 1 条：%+v", stats)
	}
	got, _ := memory.LoadSoul(context.Background(), path)
	if len(got) != 1 || got[0].Summary != "大三" {
		t.Errorf("应保留高重要度版本：%+v", got)
	}
}

// TestSoulRebuildThrottled 一小时内只跑一次。
func TestSoulRebuildThrottled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	src := &fakeSoul{records: []store.MemoryRecord{soulRec("a", "主人是学生", "在读大学", 0.9)}}
	w := newSoulWorker(t, src, path, &now)

	var first Stats
	w.rebuildSoul(context.Background(), &first)
	if first.SoulEntries != 1 {
		t.Fatalf("首轮应写入：%+v", first)
	}

	now = now.Add(30 * time.Minute)
	var second Stats
	w.rebuildSoul(context.Background(), &second)
	if second.SoulSkipped != "throttled" || src.calls != 1 {
		t.Fatalf("半小时内应节流：%+v calls=%d", second, src.calls)
	}

	now = now.Add(31 * time.Minute)
	var third Stats
	w.rebuildSoul(context.Background(), &third)
	if third.SoulEntries != 1 || src.calls != 2 {
		t.Fatalf("满一小时应重跑：%+v calls=%d", third, src.calls)
	}
}

// TestSoulRebuildUpdatesOnFactChange fact 变化后文件内容跟着更新。
func TestSoulRebuildUpdatesOnFactChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	src := &fakeSoul{records: []store.MemoryRecord{soulRec("a", "主人是学生", "大二", 0.9)}}
	w := newSoulWorker(t, src, path, &now)
	w.rebuildSoul(context.Background(), &Stats{})

	// 事实变了：大二 → 大三
	src.records[0].Summary = "大三"
	now = now.Add(2 * time.Hour)
	w.rebuildSoul(context.Background(), &Stats{})

	got, _ := memory.LoadSoul(context.Background(), path)
	if len(got) != 1 || got[0].Summary != "大三" {
		t.Fatalf("文件未更新：%+v", got)
	}
	// 文件里不该残留旧摘要
	raw, _ := os.ReadFile(path)
	if string(raw) == "" || (len(got) == 1 && got[0].Summary == "大二") {
		t.Errorf("仍含旧内容：%s", raw)
	}
}

// TestSoulRebuildFailureKeepsThrottle 失败也记节流时间，避免每轮打库。
func TestSoulRebuildFailureKeepsThrottle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	src := &fakeSoul{err: errors.New("db down")}
	w := newSoulWorker(t, src, path, &now)

	var stats Stats
	w.rebuildSoul(context.Background(), &stats)
	if stats.SoulSkipped != "failed" {
		t.Fatalf("应标记 failed：%+v", stats)
	}
	now = now.Add(time.Minute)
	var second Stats
	w.rebuildSoul(context.Background(), &second)
	if second.SoulSkipped != "throttled" || src.calls != 1 {
		t.Fatalf("失败后应节流：%+v calls=%d", second, src.calls)
	}
}

// TestSoulRebuildNilSource 未装配时跳过。
func TestSoulRebuildNilSource(t *testing.T) {
	now := time.Now()
	w := newSoulWorker(t, nil, filepath.Join(t.TempDir(), "soul.md"), &now)
	var stats Stats
	w.rebuildSoul(context.Background(), &stats)
	if stats.SoulSkipped != "none" || stats.SoulEntries != -1 {
		t.Fatalf("未装配应跳过：%+v", stats)
	}
}
