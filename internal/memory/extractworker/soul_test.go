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

// TestSoulRebuildKeepsManualEntries 手写条目在重建后仍在，自动条目被替换。
func TestSoulRebuildKeepsManualEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	// 预置：一条手写 + 一条旧自动
	manual := memory.SoulMemory{Title: "本鱼是鱼", Summary: "手写设定", Importance: 0.9,
		ValidFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Manual: true}
	oldAutp := memory.SoulMemory{Title: "旧自动", Summary: "将被替换", Importance: 0.8,
		ValidFrom: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	if err := memory.WriteSoulAtomic(context.Background(), path, []memory.SoulMemory{manual, oldAutp}); err != nil {
		t.Fatal(err)
	}

	src := &fakeSoul{records: []store.MemoryRecord{soulRec("a", "主人是学生", "在读大学", 0.9)}}
	w := newSoulWorker(t, src, path, &now)
	var stats Stats
	w.rebuildSoul(context.Background(), &stats)
	if stats.SoulEntries != 2 {
		t.Fatalf("应 1 手写 + 1 自动 = 2 条：%+v", stats)
	}
	got, err := memory.LoadSoul(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	for _, e := range got {
		titles[e.Title] = true
	}
	if !titles["本鱼是鱼"] {
		t.Error("手写条目被冲掉")
	}
	if !titles["主人是学生"] {
		t.Error("自动条目未写入")
	}
	if titles["旧自动"] {
		t.Error("旧自动条目应被替换掉")
	}
}

// TestSoulRebuildExcludesViewerIdentity 观众自述不能变成全局设定。
func TestSoulRebuildExcludesViewerIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	// 「我是大三学生」来自观众；即使 important 很高也不能进全局。
	viewerFact := soulRec("v1", "事实：我是大三学生", "我是大三学生", 0.95)
	viewerFact.FromID, viewerFact.FromName, viewerFact.Source = "u-42", "小明", "persona"
	// LLM 误标成 preference 的观众自述（不含「主人」）同样要排除。
	viewerPref := soulRec("v2", "事实：我爱吃辣", "我爱吃辣", 0.9)
	viewerPref.Kind = "preference"
	viewerPref.FromID, viewerPref.FromName, viewerPref.Source = "u-43", "小红", "persona"

	src := &fakeSoul{records: []store.MemoryRecord{viewerFact, viewerPref}}
	w := newSoulWorker(t, src, path, &now)
	var stats Stats
	w.rebuildSoul(context.Background(), &stats)
	if stats.SoulEntries != 0 {
		t.Fatalf("观众自述不应写入：%+v", stats)
	}
	got, err := memory.LoadSoul(context.Background(), path)
	if err != nil || len(got) != 0 {
		t.Fatalf("全局设定不应包含观众自述：%+v / %v", got, err)
	}
}

// TestSoulRebuildKeepsSubjectAndSource 主体/来源/发送者在转换时保留。
func TestSoulRebuildKeepsSubjectAndSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	// 主人本人的事实：靠 OwnerName 判定。
	ownerFact := soulRec("o1", "事实：我是学生", "我是学生", 0.9)
	ownerFact.FromID, ownerFact.FromName, ownerFact.Source = "1000", "Claran", "persona"
	// 观众提供的主人设定：主体是 owner，但发送者字段要保留。
	viewerOwnerPref := soulRec("p1", "主人相关：主人爱吃甜", "主人爱吃甜", 0.9)
	viewerOwnerPref.Kind = "preference"
	viewerOwnerPref.FromID, viewerOwnerPref.FromName, viewerOwnerPref.Source = "u-2", "路人", "persona"
	// 无主体标识的系统设定：按本鱼处理。
	fishFact := soulRec("f1", "本鱼是直播鱼", "陪大家聊天", 0.9)
	fishFact.Source = "owner_direct"

	src := &fakeSoul{records: []store.MemoryRecord{ownerFact, viewerOwnerPref, fishFact}}
	w := newSoulWorker(t, src, path, &now)
	w.opts.OwnerName = "Claran"
	var stats Stats
	w.rebuildSoul(context.Background(), &stats)
	if stats.SoulEntries != 3 {
		t.Fatalf("应写入 3 条：%+v", stats)
	}

	got, err := memory.LoadSoul(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]memory.SoulMemory{}
	for _, e := range got {
		byTitle[e.Title] = e
	}

	owner := byTitle["事实：我是学生"]
	if owner.Subject != memory.SoulSubjectOwner || owner.FromID != "1000" ||
		owner.FromName != "Claran" || owner.Source != "persona" || owner.Visibility != memory.SoulVisibilityPublic {
		t.Errorf("主人主体字段不符：%+v", owner)
	}
	pref := byTitle["主人相关：主人爱吃甜"]
	if pref.Subject != memory.SoulSubjectOwner || pref.FromID != "u-2" || pref.FromName != "路人" {
		t.Errorf("主人陈述的主体/说话人不符：%+v", pref)
	}
	fish := byTitle["本鱼是直播鱼"]
	if fish.Subject != memory.SoulSubjectFish || fish.Source != "owner_direct" {
		t.Errorf("本鱼主体字段不符：%+v", fish)
	}
}
