package extractworker

// 本文件是 0.9b 提炼 worker 的测试，先于实现编写（先红后绿）。
// 全部用例用假 source / 假 repo / 内存或临时文件水位，不连 PG、不调用模型。

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// baseTime 是所有用例的基准时刻，避免用 time.Now() 让断言漂移。
var baseTime = time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)

// mkEvent 造一条已完成事件。
func mkEvent(id string, at time.Time, text, fromName string, private bool, ch event.Channel, session string) event.Event {
	return event.Event{
		ID:       id,
		Channel:  ch,
		Dir:      event.DirIn,
		Session:  session,
		FromID:   "uid-" + fromName,
		FromName: fromName,
		Text:     text,
		Private:  private,
		At:       at,
		Status:   event.StatusDone,
	}
}

// cursorAfter 判断事件是否排在游标之后（按 At、再按 ID）；零游标表示从头开始。
func cursorAfter(after Cursor, ev event.Event) bool {
	if after.IsZero() {
		return true
	}
	if ev.At.After(after.At) {
		return true
	}
	return ev.At.Equal(after.At) && ev.ID > after.ID
}

// fakeSource 是受控的「done 事件」来源。
type fakeSource struct {
	mu     sync.Mutex
	events []event.Event
	err    error
	calls  int
}

func (f *fakeSource) ListDoneAfter(_ context.Context, after Cursor, limit int) ([]event.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []event.Event
	for _, ev := range f.events {
		if !cursorAfter(after, ev) {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeSource) add(evs ...event.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evs...)
}

// fakeRepo 模拟 store.MemoryRepo：按主键去重，并真实调用 Validate 拦非法输入。
//
// 真实实现是 INSERT ... ON CONFLICT (id) DO NOTHING，这里用 id 集合等价模拟，
// 这样「幂等」用例验证的是与生产一致的语义，而不是假通过。
type fakeRepo struct {
	mu      sync.Mutex
	records []store.MemoryRecord
	ids     map[string]struct{}
	err     error
}

func (f *fakeRepo) InsertRecord(_ context.Context, rec store.MemoryRecord) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return false, f.err
	}
	// 让持久化层的校验真实生效：worker 产出非法 kind/范围时这里会直接失败
	if err := rec.Validate(); err != nil {
		return false, err
	}
	if f.ids == nil {
		f.ids = make(map[string]struct{})
	}
	if _, dup := f.ids[rec.ID]; dup {
		return false, nil
	}
	f.ids[rec.ID] = struct{}{}
	f.records = append(f.records, rec)
	return true, nil
}

func (f *fakeRepo) Recent(context.Context, memory.Scope, int) ([]memory.Message, error) {
	return nil, nil
}

func (f *fakeRepo) CrossChannel(context.Context, memory.Scope, int) ([]memory.Message, error) {
	return nil, nil
}

func (f *fakeRepo) Records(context.Context, memory.Scope, int) ([]memory.Record, error) {
	return nil, nil
}

func (f *fakeRepo) all() []store.MemoryRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.MemoryRecord, len(f.records))
	copy(out, f.records)
	return out
}

// newWorker 是测试用的构造助手，任何错误都直接终止用例。
func newWorker(t *testing.T, src DoneSource, repo store.MemoryRepo, wm Watermark, opts Options) *Worker {
	t.Helper()
	w, err := New(src, repo, wm, opts)
	if err != nil {
		t.Fatalf("构造 worker 失败：%v", err)
	}
	return w
}

// TestRunOnceInsertsCandidate 正常路径：扫描到 done 事件 → 提炼 → 落库字段正确。
func TestRunOnceInsertsCandidate(t *testing.T) {
	ev := mkEvent("live:1:1001", baseTime, "我保证明天带话", "路人甲", false, event.ChLive, "1")
	src := &fakeSource{events: []event.Event{ev}}
	repo := &fakeRepo{}
	wm := NewMemoryWatermark()

	stats, err := newWorker(t, src, repo, wm, Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Scanned != 1 || stats.Extracted != 1 || stats.Inserted != 1 || stats.Duplicated != 0 {
		t.Errorf("统计不符：%+v", stats)
	}

	recs := repo.all()
	if len(recs) != 1 {
		t.Fatalf("写入记录数 = %d，期望 1", len(recs))
	}
	rec := recs[0]

	if rec.Kind != "promise" {
		t.Errorf("Kind = %q，期望 promise", rec.Kind)
	}
	if rec.Source == "" {
		t.Error("Source 为空，无法满足 memory_records.source 外键")
	}
	if rec.Title == "" || rec.Summary == "" {
		t.Errorf("Title/Summary 为空：%q / %q", rec.Title, rec.Summary)
	}
	if rec.Importance != 0.9 || rec.Confidence != 0.95 {
		t.Errorf("评分 = %v/%v，期望 0.9/0.95", rec.Importance, rec.Confidence)
	}
	if len(rec.EventIDs) != 1 || rec.EventIDs[0] != ev.ID {
		t.Errorf("EventIDs = %v，期望 [%s]", rec.EventIDs, ev.ID)
	}
	if !rec.ValidFrom.Equal(baseTime) {
		t.Errorf("ValidFrom = %v，期望事件时间 %v", rec.ValidFrom, baseTime)
	}
	if rec.Channel != event.ChLive || rec.Session != "1" || rec.Private {
		t.Errorf("范围字段不符：channel=%q session=%q private=%v", rec.Channel, rec.Session, rec.Private)
	}
}

// TestRunOnceIdempotent 幂等：同一批事件重扫不产生重复行（按 event_id+kind+title 去重）。
func TestRunOnceIdempotent(t *testing.T) {
	ev := mkEvent("live:1:1002", baseTime, "我答应明天带话", "路人甲", false, event.ChLive, "1")
	src := &fakeSource{events: []event.Event{ev}}
	repo := &fakeRepo{}

	// 第一次：从零水位扫
	first, err := newWorker(t, src, repo, NewMemoryWatermark(), Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第一次 RunOnce 失败：%v", err)
	}
	if first.Inserted != 1 {
		t.Fatalf("第一次 Inserted = %d，期望 1", first.Inserted)
	}

	// 第二次：换一个全新的零水位 worker，等价于「重扫同一批事件」
	second, err := newWorker(t, src, repo, NewMemoryWatermark(), Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二次 RunOnce 失败：%v", err)
	}
	if second.Inserted != 0 || second.Duplicated != 1 {
		t.Errorf("第二次统计 = %+v，期望 Inserted=0 Duplicated=1", second)
	}
	if n := len(repo.all()); n != 1 {
		t.Errorf("记录数 = %d，期望仍是 1（幂等）", n)
	}
}

// TestPrivateSkippedByDefault 私密事件默认不提炼，但水位必须继续推进（否则会卡死）。
func TestPrivateSkippedByDefault(t *testing.T) {
	ev := mkEvent("dm:42:2001", baseTime, "我答应明天陪你", "主人", true, event.ChDM, "42")
	src := &fakeSource{events: []event.Event{ev}}
	repo := &fakeRepo{}
	wm := NewMemoryWatermark()

	stats, err := newWorker(t, src, repo, wm, Options{OwnerName: "主人"}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Scanned != 1 || stats.Extracted != 0 || stats.Skipped != 1 || stats.Inserted != 0 {
		t.Errorf("统计 = %+v，期望 Scanned=1 Extracted=0 Skipped=1 Inserted=0", stats)
	}
	if n := len(repo.all()); n != 0 {
		t.Errorf("私密事件不应落库，实际 %d 条", n)
	}

	cursor, err := wm.Load(context.Background())
	if err != nil {
		t.Fatalf("读水位失败：%v", err)
	}
	if cursor.IsZero() {
		t.Error("跳过的私密事件也必须推进水位，否则下一轮会重复扫描")
	}
}

// TestPrivateAllowed 显式授权后，私密事件能提炼并按 dm/private 落库。
func TestPrivateAllowed(t *testing.T) {
	ev := mkEvent("dm:42:2002", baseTime, "我答应明天陪你", "主人", true, event.ChDM, "42")
	src := &fakeSource{events: []event.Event{ev}}
	repo := &fakeRepo{}

	w := newWorker(t, src, repo, NewMemoryWatermark(), Options{OwnerName: "主人", AllowPrivate: true})
	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 2 {
		t.Fatalf("Inserted = %d，期望 2（承诺 + 主人相关）", stats.Inserted)
	}

	for _, rec := range repo.all() {
		if !rec.Private {
			t.Errorf("私密事件的记录必须 private=true：%+v", rec)
		}
		if rec.Channel != event.ChDM || rec.Session != "42" {
			t.Errorf("范围字段不符：channel=%q session=%q", rec.Channel, rec.Session)
		}
	}
}

// TestWatermarkResumeAfterRestart 重启后从水位补提炼：已处理的跳过，新事件继续。
func TestWatermarkResumeAfterRestart(t *testing.T) {
	ctx := context.Background()

	e1 := mkEvent("live:1:3001", baseTime, "我保证明天带话", "路人甲", false, event.ChLive, "1")
	e2 := mkEvent("live:1:3002", baseTime.Add(time.Minute), "主人今天心情不错", "路人乙", false, event.ChLive, "1")
	src := &fakeSource{events: []event.Event{e1, e2}}
	repo := &fakeRepo{}
	wm := NewMemoryWatermark()

	if _, err := newWorker(t, src, repo, wm, Options{}).RunOnce(ctx); err != nil {
		t.Fatalf("第一轮失败：%v", err)
	}
	if n := len(repo.all()); n != 2 {
		t.Fatalf("第一轮记录数 = %d，期望 2", n)
	}

	// 模拟重启：新 worker 实例，复用同一份水位，repo 不变
	w2 := newWorker(t, src, repo, wm, Options{})
	stats, err := w2.RunOnce(ctx)
	if err != nil {
		t.Fatalf("重启后 RunOnce 失败：%v", err)
	}
	if stats.Scanned != 0 || stats.Inserted != 0 {
		t.Errorf("重启后不应重复扫描：%+v", stats)
	}

	// 追加一条新事件，重启后的 worker 应只处理这一条
	e3 := mkEvent("live:1:3003", baseTime.Add(2*time.Minute), "一定帮你带话", "路人丙", false, event.ChLive, "1")
	src.add(e3)
	stats, err = w2.RunOnce(ctx)
	if err != nil {
		t.Fatalf("补提炼失败：%v", err)
	}
	if stats.Scanned != 1 || stats.Inserted != 1 {
		t.Errorf("补提炼统计 = %+v，期望 Scanned=1 Inserted=1", stats)
	}
}

// TestCursorAdvancesWithoutCandidates 没有命中任何规则的事件也要推进水位，避免死循环。
func TestCursorAdvancesWithoutCandidates(t *testing.T) {
	ev := mkEvent("live:1:4001", baseTime, "今天天气不错", "路人甲", false, event.ChLive, "1")
	src := &fakeSource{events: []event.Event{ev}}
	repo := &fakeRepo{}
	wm := NewMemoryWatermark()
	w := newWorker(t, src, repo, wm, Options{})

	stats, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Scanned != 1 || stats.Extracted != 0 || stats.Skipped != 1 {
		t.Errorf("统计 = %+v，期望 Scanned=1 Extracted=0 Skipped=1", stats)
	}

	// 再跑一轮，不应再次返回同一条
	stats, err = w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	if stats.Scanned != 0 {
		t.Errorf("第二轮 Scanned = %d，期望 0（水位已推进）", stats.Scanned)
	}
}

// TestBatchLoopConsumesAll 一次 RunOnce 要按批循环，直到把积压事件消费完。
func TestBatchLoopConsumesAll(t *testing.T) {
	var evs []event.Event
	for i := 0; i < 5; i++ {
		id := "live:1:" + string(rune('a'+i))
		evs = append(evs, mkEvent(id, baseTime.Add(time.Duration(i)*time.Minute), "我保证明天带话", "路人", false, event.ChLive, "1"))
	}
	src := &fakeSource{events: evs}
	repo := &fakeRepo{}

	stats, err := newWorker(t, src, repo, NewMemoryWatermark(), Options{BatchSize: 2}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Scanned != 5 || stats.Inserted != 5 {
		t.Errorf("统计 = %+v，期望 Scanned=5 Inserted=5", stats)
	}
}

// TestSourceErrorPropagates 来源报错要原样返回，且不推进水位。
func TestSourceErrorPropagates(t *testing.T) {
	boom := errors.New("来源炸了")
	src := &fakeSource{err: boom}
	repo := &fakeRepo{}
	wm := NewMemoryWatermark()

	_, err := newWorker(t, src, repo, wm, Options{}).RunOnce(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("错误未透传：%v", err)
	}
	if cursor, _ := wm.Load(context.Background()); !cursor.IsZero() {
		t.Errorf("来源报错时水位不应推进：%+v", cursor)
	}
}

// TestRepoErrorKeepsCursor 落库报错时不越过失败事件，保证重试能补上。
func TestRepoErrorKeepsCursor(t *testing.T) {
	boom := errors.New("写库炸了")
	src := &fakeSource{events: []event.Event{
		mkEvent("live:1:5001", baseTime, "我保证明天带话", "路人甲", false, event.ChLive, "1"),
	}}
	repo := &fakeRepo{err: boom}
	wm := NewMemoryWatermark()

	_, err := newWorker(t, src, repo, wm, Options{}).RunOnce(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("错误未透传：%v", err)
	}
	if cursor, _ := wm.Load(context.Background()); !cursor.IsZero() {
		t.Errorf("落库失败时水位不应越过失败事件：%+v", cursor)
	}
}

// TestZeroAtEventSkipped 事件时间为零的脏数据要跳过，不能写出 valid_from=0 的记录。
func TestZeroAtEventSkipped(t *testing.T) {
	ev := mkEvent("live:1:6001", time.Time{}, "我保证明天带话", "路人甲", false, event.ChLive, "1")
	repo := &fakeRepo{}

	stats, err := newWorker(t, &fakeSource{events: []event.Event{ev}}, repo, NewMemoryWatermark(), Options{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Inserted != 0 || stats.Skipped != 1 {
		t.Errorf("统计 = %+v，期望 Inserted=0 Skipped=1", stats)
	}
}

// TestNewRejectsInvalidDeps 依赖缺失必须在构造期失败，不拖到运行期。
func TestNewRejectsInvalidDeps(t *testing.T) {
	repo := &fakeRepo{}
	src := &fakeSource{}

	if _, err := New(nil, repo, NewMemoryWatermark(), Options{}); err == nil {
		t.Error("source 为 nil 应报错")
	}
	if _, err := New(src, nil, NewMemoryWatermark(), Options{}); err == nil {
		t.Error("repo 为 nil 应报错")
	}
	if _, err := New(src, repo, nil, Options{}); err == nil {
		t.Error("watermark 为 nil 应报错")
	}
}

// TestRunStopsOnCancel 定时循环在 ctx 取消后要干净退出，不留 goroutine。
func TestRunStopsOnCancel(t *testing.T) {
	src := &fakeSource{}
	repo := &fakeRepo{}
	w := newWorker(t, src, repo, NewMemoryWatermark(), Options{ScanInterval: 5 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("取消后应返回 nil，实际 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在取消后退出")
	}
}

// TestFileWatermarkPersistsAcrossInstances 文件水位能跨实例读回（真正的「重启」）。
func TestFileWatermarkPersistsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "watermark.json")

	wm, err := NewFileWatermark(path)
	if err != nil {
		t.Fatalf("创建文件水位失败：%v", err)
	}
	// 首次读取：文件不存在 → 零游标，不算错误
	if c, err := wm.Load(ctx); err != nil || !c.IsZero() {
		t.Fatalf("空水位 = %+v, err=%v；期望零值且无错误", c, err)
	}

	want := Cursor{At: baseTime, ID: "live:1:7001"}
	if err := wm.Save(ctx, want); err != nil {
		t.Fatalf("保存水位失败：%v", err)
	}

	// 新实例模拟重启
	wm2, err := NewFileWatermark(path)
	if err != nil {
		t.Fatalf("重启后创建失败：%v", err)
	}
	got, err := wm2.Load(ctx)
	if err != nil {
		t.Fatalf("重启后读取失败：%v", err)
	}
	if !got.At.Equal(want.At) || got.ID != want.ID {
		t.Errorf("水位 = %+v，期望 %+v", got, want)
	}
}

// TestStoreKindMapping 候选类型要映射到 store 允许的记忆类型，且映射后必须过得了持久化校验。
func TestStoreKindMapping(t *testing.T) {
	cases := map[string]string{
		memory.KindPromise:    "promise",
		memory.KindPreference: "preference",
		memory.KindFact:       "fact",
		memory.KindFirstSeen:  "note", // store 校验没有 first_seen，落到最贴近的 note
		"未来新增的类型":             "note",
	}
	for in, want := range cases {
		if got := storeKind(in); got != want {
			t.Errorf("storeKind(%q) = %q，期望 %q", in, got, want)
		}
	}

	// 关键回归：映射结果必须能被 store.MemoryRecord.Validate 接受，
	// 否则 worker 会在生产上因「记忆类型错误」整批失败。
	for _, kind := range []string{memory.KindPromise, memory.KindPreference, memory.KindFirstSeen, memory.KindFact} {
		rec := store.MemoryRecord{
			Record: memory.Record{
				ID:         "mem_x",
				Channel:    event.ChLive,
				Session:    "1",
				Summary:    "摘要",
				Importance: 0.5,
				Confidence: 0.5,
				EventIDs:   []string{"live:1:x"},
				ValidFrom:  baseTime,
			},
			Source: defaultSource(kind),
			Kind:   storeKind(kind),
			Title:  "标题",
		}
		if err := rec.Validate(); err != nil {
			t.Errorf("%q 映射后仍过不了持久化校验：%v", kind, err)
		}
	}
}

// TestRecordIDStableAndDistinct 主键由 event_id + kind + title 派生：同输入稳定、换维度就不同。
func TestRecordIDStableAndDistinct(t *testing.T) {
	base := recordID("live:1:1", memory.KindPromise, "承诺：带话")

	if again := recordID("live:1:1", memory.KindPromise, "承诺：带话"); again != base {
		t.Errorf("同一输入两次派生不同 ID：%q vs %q", base, again)
	}

	others := map[string]string{
		"换事件":    recordID("live:1:2", memory.KindPromise, "承诺：带话"),
		"换 kind": recordID("live:1:1", memory.KindPreference, "承诺：带话"),
		"换标题":    recordID("live:1:1", memory.KindPromise, "承诺：别的话"),
	}
	for name, other := range others {
		if other == base {
			t.Errorf("%s 后 ID 未变化：%q", name, other)
		}
	}

	// 拼接歧义：分隔符必须挡住 「a|b|c」 与 「ab||c」 撞车
	if recordID("a", "b", "c") == recordID("ab", "", "c") {
		t.Error("分隔符不足以区分拼接歧义")
	}
}
