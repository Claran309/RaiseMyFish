package extractworker

// 本文件测 #93 重复度触发：实体抽取 → 窗口计数 → 提升事实 → 防抖/纠正/归档/证据护栏。
// 全部用假事件来源与假事实仓储，不连 PG、不调模型。

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// repeatNow 是所有重复度用例的「当前时刻」。
var repeatNow = baseTime.Add(2 * time.Hour)

// fakeRepeatEvents 模拟 database.EventRepo.PublicIncomingSince。
type fakeRepeatEvents struct {
	events []event.Event
	err    error
	calls  int
	since  time.Time
	limit  int
}

func (f *fakeRepeatEvents) PublicIncomingSince(_ context.Context, since time.Time, limit int) ([]event.Event, error) {
	f.calls++
	f.since, f.limit = since, limit
	if f.err != nil {
		return nil, f.err
	}
	out := make([]event.Event, 0, len(f.events))
	for _, ev := range f.events {
		if ev.Private || ev.Channel == event.ChDM || ev.Dir != event.DirIn || ev.At.Before(since) {
			continue
		}
		out = append(out, ev)
	}
	// 生产 SQL 是 at DESC, id DESC；候选内部还会再排一次，这里保持同序方便断言。
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type fakeRepeatAliases []memory.RepeatAlias

func (f fakeRepeatAliases) ConfirmedAliases() []memory.RepeatAlias { return f }

// fakeRepeatStore 模拟 store.RepeatFactStore，并复刻 ON CONFLICT 的三种结果：
// 新插入 / 摘要变化且时间更晚才更新 / 其余不写。
type fakeRepeatStore struct {
	mu          sync.Mutex
	records     map[string]store.MemoryRecord
	order       []string
	archived    map[string]bool
	downweights map[string][2]float64
	err         error
}

func newFakeRepeatStore() *fakeRepeatStore {
	return &fakeRepeatStore{
		records:     map[string]store.MemoryRecord{},
		archived:    map[string]bool{},
		downweights: map[string][2]float64{},
	}
}

func (f *fakeRepeatStore) InsertRecord(_ context.Context, rec store.MemoryRecord) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	// 持久化校验必须真实生效：范围/证据非法要在这里炸出来。
	if err := rec.Validate(); err != nil {
		return false, err
	}
	existing, ok := f.records[rec.ID]
	if !ok {
		f.records[rec.ID] = rec
		f.order = append(f.order, rec.ID)
		return true, nil
	}
	if existing.Summary == rec.Summary || !rec.ValidFrom.After(existing.ValidFrom) {
		return false, nil
	}
	existing.Summary = rec.Summary
	existing.Confidence = rec.Confidence
	existing.EventIDs = append([]string(nil), rec.EventIDs...)
	existing.Evidence = append([]store.MemoryEvidence(nil), rec.Evidence...)
	existing.ValidFrom = rec.ValidFrom
	f.records[rec.ID] = existing
	return true, nil
}

func (f *fakeRepeatStore) RepeatFactByTitle(_ context.Context, title string) (store.RepeatFactState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.RepeatFactState{}, false, f.err
	}
	var best store.MemoryRecord
	found := false
	for _, rec := range f.records {
		if rec.Kind != "fact" || rec.Title != title || rec.Private {
			continue
		}
		if !found || rec.ValidFrom.After(best.ValidFrom) {
			best, found = rec, true
		}
	}
	if !found {
		return store.RepeatFactState{}, false, nil
	}
	return store.RepeatFactState{
		ID: best.ID, Title: best.Title, Summary: best.Summary,
		Importance: best.Importance, Confidence: best.Confidence,
		ValidFrom: best.ValidFrom, Archived: f.archived[best.ID],
	}, true, nil
}

func (f *fakeRepeatStore) DownweightFact(_ context.Context, id string, importance, confidence float64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.records[id]
	if !ok || f.archived[id] {
		return false, nil
	}
	if importance < rec.Importance {
		rec.Importance = importance
	}
	if confidence < rec.Confidence {
		rec.Confidence = confidence
	}
	f.records[id] = rec
	f.downweights[id] = [2]float64{importance, confidence}
	return true, nil
}

func (f *fakeRepeatStore) all() []store.MemoryRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.MemoryRecord, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.records[id])
	}
	return out
}

func (f *fakeRepeatStore) archive(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archived[id] = true
}

// newRepeatWorker 装配一个只跑重复度触发的 worker（事件扫描为空）。
func newRepeatWorker(t *testing.T, facts *fakeRepeatStore, events *fakeRepeatEvents, aliases []memory.RepeatAlias, now time.Time, opts Options) *Worker {
	t.Helper()
	opts.Repeats = &RepeatPromotion{
		Events: events, Aliases: fakeRepeatAliases(aliases), Facts: facts,
		Threshold: 5, Window: 7 * 24 * time.Hour, Debounce: 24 * time.Hour,
		IgnoreNames: []string{"大肥鱼"},
	}
	opts.Now = func() time.Time { return now }
	return newWorker(t, &fakeSource{}, &fakeRepo{}, NewMemoryWatermark(), opts)
}

// mentionEvents 造 n 条提到 text 的公开弹幕，按时间从旧到新编号。
func mentionEvents(n int, text string, at time.Time) []event.Event {
	out := make([]event.Event, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, mkEvent("live:room:"+strconv.Itoa(i), at.Add(time.Duration(i)*time.Minute), text, "路人"+strconv.Itoa(i), false, event.ChLive, "room"))
	}
	return out
}

var miracleAliases = []memory.RepeatAlias{
	{Alias: "奇迹鱼", UID: "9009", Name: "X奇迹与你X"},
}

// TestPromoteRepeatsFourteenMentions 验收：14 条「奇迹鱼」→ 恰好 1 条 fact，字段与证据完整。
func TestPromoteRepeatsFourteenMentions(t *testing.T) {
	events := &fakeRepeatEvents{events: mentionEvents(14, "奇迹鱼来了", baseTime)}
	facts := newFakeRepeatStore()
	w := newRepeatWorker(t, facts, events, miracleAliases, repeatNow, Options{})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.RepeatScanned != 14 || stats.RepeatCandidates != 1 || stats.RepeatInserted != 1 {
		t.Fatalf("重复度统计不符：%+v", stats)
	}
	if stats.RepeatSkipped != "none" {
		t.Errorf("正常跑完应标记 none：%q", stats.RepeatSkipped)
	}

	recs := facts.all()
	if len(recs) != 1 {
		t.Fatalf("事实条数 = %d，期望 1：%+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.Title != "称呼：奇迹鱼 = X奇迹与你X" {
		t.Errorf("Title = %q", rec.Title)
	}
	if rec.Summary != "弹幕里「奇迹鱼」指的是 X奇迹与你X（7 天内提及 14 次）" {
		t.Errorf("Summary = %q", rec.Summary)
	}
	if rec.Kind != "fact" || rec.Source != "content" || rec.Subject != memory.SoulSubjectViewer {
		t.Errorf("来源/主体不符：%+v", rec)
	}
	if rec.Importance != 0.5 || rec.Confidence != 0.8 {
		t.Errorf("权重 = %v/%v，期望 0.5/0.8", rec.Importance, rec.Confidence)
	}
	// 共享事实查询要求 channel/session 非空，否则鱼召回不到。
	if rec.Channel != event.ChLive || rec.Session != "global" || rec.Private {
		t.Errorf("范围字段不符：channel=%q session=%q private=%v", rec.Channel, rec.Session, rec.Private)
	}
	if !rec.ValidFrom.Equal(repeatNow) {
		t.Errorf("ValidFrom = %v，期望 %v", rec.ValidFrom, repeatNow)
	}
	if !strings.HasPrefix(rec.ID, "fact:public:repeat:") {
		t.Errorf("ID 前缀不符：%q", rec.ID)
	}
	wantIDs := []string{"live:room:14", "live:room:13", "live:room:12", "live:room:11", "live:room:10"}
	if strings.Join(rec.EventIDs, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("代表事件 = %v，期望最新 5 条 %v", rec.EventIDs, wantIDs)
	}
	if len(rec.Evidence) != 2 {
		t.Fatalf("evidence 条数 = %d，期望 2：%+v", len(rec.Evidence), rec.Evidence)
	}
	if rec.Evidence[0].Field != "mention_count" || rec.Evidence[0].Text != "7 天内公开提及 14 次" {
		t.Errorf("次数证据不符：%+v", rec.Evidence[0])
	}
	if rec.Evidence[1].Field != "event_ids" || rec.Evidence[1].Text != strings.Join(wantIDs, ",") {
		t.Errorf("事件证据不符：%+v", rec.Evidence[1])
	}
	for _, e := range rec.Evidence {
		if e.Type != store.ViewerSummaryBasisObserved {
			t.Errorf("证据类型应为 observed：%+v", e)
		}
	}
}

// TestPromoteRepeatsBelowThreshold 未达标不写：4 条仍然只是闲聊。
func TestPromoteRepeatsBelowThreshold(t *testing.T) {
	events := &fakeRepeatEvents{events: mentionEvents(4, "奇迹鱼来了", baseTime)}
	facts := newFakeRepeatStore()
	w := newRepeatWorker(t, facts, events, miracleAliases, repeatNow, Options{})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.RepeatScanned != 4 || stats.RepeatCandidates != 0 || stats.RepeatInserted != 0 {
		t.Errorf("未达标统计不符：%+v", stats)
	}
	if n := len(facts.all()); n != 0 {
		t.Errorf("未达阈値不应写库，实际 %d 条", n)
	}
}

// TestPromoteRepeatsDebounceAndCountUpdate 24h 防抖；过窗后次数变化只更新 summary。
func TestPromoteRepeatsDebounceAndCountUpdate(t *testing.T) {
	events := &fakeRepeatEvents{events: mentionEvents(14, "奇迹鱼来了", baseTime)}
	facts := newFakeRepeatStore()

	first, err := newRepeatWorker(t, facts, events, miracleAliases, repeatNow, Options{}).RunOnce(context.Background())
	if err != nil || first.RepeatInserted != 1 {
		t.Fatalf("首轮失败：%+v / %v", first, err)
	}

	// 1 小时内再跑：命中防抖，不重写。
	second, err := newRepeatWorker(t, facts, events, miracleAliases, repeatNow.Add(30*time.Minute), Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if second.RepeatDebounced != 1 || second.RepeatInserted != 0 {
		t.Errorf("防抖统计不符：%+v", second)
	}
	if n := len(facts.all()); n != 1 {
		t.Fatalf("防抖期间不应新增，实际 %d 条", n)
	}

	// 过 24h 且出现新提及：只更新计数，不新增行。
	events.events = append(events.events, mkEvent("live:room:15", baseTime.Add(24*time.Hour), "又见奇迹鱼", "路人乙", false, event.ChLive, "room"))
	third, err := newRepeatWorker(t, facts, events, miracleAliases, repeatNow.Add(25*time.Hour), Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第三轮失败：%v", err)
	}
	if third.RepeatInserted != 1 {
		t.Errorf("过防抖后应更新：%+v", third)
	}
	recs := facts.all()
	if len(recs) != 1 {
		t.Fatalf("更新不应新增行，实际 %d 条", len(recs))
	}
	if !strings.Contains(recs[0].Summary, "15 次") {
		t.Errorf("summary 计数未更新：%q", recs[0].Summary)
	}
}

// TestPromoteRepeatsOwnerCorrectionDownweights 被主人纠正 → 只降权，不重写计数。
func TestPromoteRepeatsOwnerCorrectionDownweights(t *testing.T) {
	events := &fakeRepeatEvents{events: mentionEvents(14, "奇迹鱼来了", baseTime)}
	// 主人纠正：明确说叫错了，不是疑问句。
	events.events = append(events.events, mkEvent("live:room:owner", baseTime.Add(time.Minute), "别乱说，奇迹鱼不是X奇迹与你", "Claran", false, event.ChLive, "room"))
	events.events[len(events.events)-1].FromID = "owner-1"

	facts := newFakeRepeatStore()
	if _, err := facts.InsertRecord(context.Background(), store.MemoryRecord{
		Record: memory.Record{
			ID: "fact:public:repeat:old", Channel: event.ChLive, Session: "global",
			Summary: "旧摘要", Importance: 0.5, Confidence: 0.8,
			EventIDs: []string{"live:room:1"}, ValidFrom: baseTime,
		},
		Source: "content", Kind: "fact", Title: "称呼：奇迹鱼 = X奇迹与你X",
		DerivedBy: "rule:repeat/v1",
	}); err != nil {
		t.Fatalf("预置事实失败：%v", err)
	}

	w := newRepeatWorker(t, facts, events, miracleAliases, repeatNow, Options{OwnerUID: "owner-1"})
	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.RepeatCorrected != 1 || stats.RepeatInserted != 0 {
		t.Fatalf("纠正统计不符：%+v", stats)
	}
	rec := facts.all()[0]
	if rec.Importance != 0.2 || rec.Confidence != 0.5 {
		t.Errorf("纠正后权重 = %v/%v，期望 0.2/0.5", rec.Importance, rec.Confidence)
	}
	if rec.Summary != "旧摘要" {
		t.Errorf("纠正不应改写摘要：%q", rec.Summary)
	}
}

// TestPromoteRepeatsCorrectionWithoutFactWritesNothing 没有既有事实时，纠正只阻止写入。
func TestPromoteRepeatsCorrectionWithoutFactWritesNothing(t *testing.T) {
	events := &fakeRepeatEvents{events: mentionEvents(14, "奇迹鱼来了", baseTime)}
	events.events = append(events.events, mkEvent("live:room:owner", baseTime.Add(time.Minute), "记错了，不叫奇迹鱼", "Claran", false, event.ChLive, "room"))
	events.events[len(events.events)-1].FromID = "owner-1"

	facts := newFakeRepeatStore()
	stats, err := newRepeatWorker(t, facts, events, miracleAliases, repeatNow, Options{OwnerUID: "owner-1"}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.RepeatCorrected != 1 || stats.RepeatInserted != 0 {
		t.Errorf("统计不符：%+v", stats)
	}
	if n := len(facts.all()); n != 0 {
		t.Errorf("被纠正的实体不应写入，实际 %d 条", n)
	}
}

// TestPromoteRepeatsArchivedSkipped 已归档事实不再重新提升。
func TestPromoteRepeatsArchivedSkipped(t *testing.T) {
	events := &fakeRepeatEvents{events: mentionEvents(14, "奇迹鱼来了", baseTime)}
	facts := newFakeRepeatStore()
	if _, err := facts.InsertRecord(context.Background(), store.MemoryRecord{
		Record: memory.Record{
			ID: "fact:public:repeat:arch", Channel: event.ChLive, Session: "global",
			Summary: "旧摘要", Importance: 0.5, Confidence: 0.8,
			EventIDs: []string{"live:room:1"}, ValidFrom: baseTime.Add(-48 * time.Hour),
		},
		Source: "content", Kind: "fact", Title: "称呼：奇迹鱼 = X奇迹与你X",
	}); err != nil {
		t.Fatalf("预置事实失败：%v", err)
	}
	facts.archive("fact:public:repeat:arch")

	stats, err := newRepeatWorker(t, facts, events, miracleAliases, repeatNow, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.RepeatArchived != 1 || stats.RepeatInserted != 0 {
		t.Errorf("归档统计不符：%+v", stats)
	}
}

// TestPromoteRepeatsProfileName 观众档案名/@ 目标也能提升（无指向时只写称呼）。
func TestPromoteRepeatsProfileName(t *testing.T) {
	meta, err := json.Marshal(map[string]string{"reply_to_name": "夜猫子甲"})
	if err != nil {
		t.Fatal(err)
	}
	facts := newFakeRepeatStore()
	events := &fakeRepeatEvents{}
	for i := 1; i <= 5; i++ {
		ev := mkEvent("live:room:"+strconv.Itoa(i), baseTime.Add(time.Duration(i)*time.Minute), "来了来了", "路人"+strconv.Itoa(i), false, event.ChLive, "room")
		ev.Meta = meta
		events.events = append(events.events, ev)
	}

	opts := Options{}
	w := newRepeatWorker(t, facts, events, nil, repeatNow, opts)
	w.opts.Repeats.Names = fakeRepeatNames{{UID: "9101", Name: "夜猫子甲"}}

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.RepeatInserted != 1 {
		t.Fatalf("档案名应提升：%+v", stats)
	}
	rec := facts.all()[0]
	if rec.Title != "称呼：夜猫子甲" {
		t.Errorf("Title = %q", rec.Title)
	}
	if rec.Summary != "弹幕里多次提及「夜猫子甲」（7 天内共 5 次）" {
		t.Errorf("Summary = %q", rec.Summary)
	}
}

type fakeRepeatNames []store.ViewerName

func (f fakeRepeatNames) RepeatViewerNames(context.Context, time.Time, int) ([]store.ViewerName, error) {
	return f, nil
}

// TestPromoteRepeatsRequiresEvidence 无事件证据的候选一律不写。
func TestPromoteRepeatsRequiresEvidence(t *testing.T) {
	facts := newFakeRepeatStore()
	w := newRepeatWorker(t, facts, &fakeRepeatEvents{}, miracleAliases, repeatNow, Options{})
	stats := Stats{}
	c := memory.RepeatCandidate{
		Entity: memory.RepeatEntity{Alias: "奇迹鱼", Target: "X奇迹与你X", UID: "9009", Source: "alias"},
		Count:  14,
	}
	if err := w.promoteRepeat(context.Background(), facts, c, nil, 7, 24*time.Hour, repeatNow, &stats); err != nil {
		t.Fatalf("promoteRepeat 失败：%v", err)
	}
	if n := len(facts.all()); n != 0 {
		t.Errorf("无证据不应写入，实际 %d 条", n)
	}
}

// TestPromoteRepeatsDisabledByThreshold 阈值 <2 视为关闭：不读库、不改事实。
func TestPromoteRepeatsDisabledByThreshold(t *testing.T) {
	events := &fakeRepeatEvents{events: mentionEvents(14, "奇迹鱼来了", baseTime)}
	facts := newFakeRepeatStore()
	w := newRepeatWorker(t, facts, events, miracleAliases, repeatNow, Options{})
	w.opts.Repeats.Threshold = 0

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.RepeatScanned != -1 || stats.RepeatSkipped != "none" {
		t.Errorf("关闭状态统计不符：%+v", stats)
	}
	if events.calls != 0 {
		t.Errorf("关闭时不应查询事件窗口：%d", events.calls)
	}
}

// TestPromoteRepeatsFailureOnlyMarksStats 子任务失败不影响主提炼。
func TestPromoteRepeatsFailureOnlyMarksStats(t *testing.T) {
	facts := newFakeRepeatStore()
	facts.err = context.DeadlineExceeded
	events := &fakeRepeatEvents{events: mentionEvents(14, "奇迹鱼来了", baseTime)}
	w := newRepeatWorker(t, facts, events, miracleAliases, repeatNow, Options{})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("重复度失败不应让 RunOnce 报错：%v", err)
	}
	if stats.RepeatSkipped != "failed" {
		t.Errorf("RepeatSkipped = %q，期望 failed", stats.RepeatSkipped)
	}
}
