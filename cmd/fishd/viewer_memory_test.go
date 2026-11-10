package main

// 本文件测「观众记忆主动注入」：当前发言者的相关记忆随档案直接进快照，
// 不依赖工具调用与关键词召回。
//
// 口径：
//   - 有档案的发言者：档案 + 最近 7 天聊过的 + 跟他相关的记忆；
//   - 记忆查询失败只丢记忆段，档案仍保留；
//   - 陌生人（无档案）与记忆源未实现可选能力时保持原样：不查、不注入。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeViewerRecordReader 在普通记忆读取之外实现 ViewerRecordSource 与 FocusReader。
type fakeViewerRecordReader struct {
	*fakeMemoryReader

	records []memory.Record
	err     error

	focus      []memory.Message
	focusCalls int

	recordCalls int
	gotQuery    memory.ViewerRecordQuery
}

func (f *fakeViewerRecordReader) ViewerRecords(_ context.Context, q memory.ViewerRecordQuery) ([]memory.Record, error) {
	f.recordCalls++
	f.gotQuery = q
	if f.err != nil {
		return nil, f.err
	}
	return f.records, nil
}

func (f *fakeViewerRecordReader) Focus(_ context.Context, _ memory.FocusQuery) ([]memory.Message, error) {
	f.focusCalls++
	return f.focus, nil
}

// viewerMemoryNow 是注入测试的固定「现在」。
func viewerMemoryNow() time.Time {
	return time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
}

func viewerMemoryEvent(at time.Time) event.Event {
	ev := snapshotTestEvent("在吗")
	ev.FromID = "42"
	ev.FromName = "娇娇"
	ev.At = at
	return ev
}

// viewerTextFor 只组装 Viewer 段（不经过完整 BuildContext），供降级口径测试直调。
func viewerTextFor(builder *contextBuilder, ev event.Event) string {
	return builder.viewerText(context.Background(), brain.ContextInput{Event: ev})
}

func viewerMemoryProfile() store.ViewerProfile {
	return store.ViewerProfile{
		UID: "42", Name: "娇娇", Hits: 12, Replies: 3,
		LastSeen:       time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC),
		RecentTexts:    []string{"今天也要白饭"},
		ProfileSummary: "爱吃白饭、常来打招呼",
	}
}

func viewerMemoryRecords(now time.Time) []memory.Record {
	return []memory.Record{
		{ID: "vm-1", Channel: event.ChLive, Session: "30308220", Summary: "昨晚顶号后跟鱼打了招呼", Importance: 0.5, Confidence: 0.9, ValidFrom: now.Add(-24 * time.Hour)},
		{ID: "vm-2", Channel: event.ChLive, Session: "30308220", Summary: "昨天聊到夜宵想吃烤鱼", Importance: 0.55, Confidence: 0.9, ValidFrom: now.Add(-48 * time.Hour)},
		{ID: "vm-3", Channel: event.ChLive, Session: "30308220", Summary: "9月3日给鱼喂过饭", Importance: 0.95, Confidence: 0.9, ValidFrom: now.Add(-60 * 24 * time.Hour)},
	}
}

// viewerMemoryHistoryRecords 是「相关记忆超过展示上限」的样本：
// 6 条高分旧记忆占满相关段，最近 7 天的 2 条落进时间窗段，验证两段并存。
func viewerMemoryHistoryRecords(now time.Time) []memory.Record {
	records := []memory.Record{
		{ID: "vm-1", Channel: event.ChLive, Session: "30308220", Summary: "昨晚顶号后跟鱼打了招呼", Importance: 0.5, Confidence: 0.9, ValidFrom: now.Add(-24 * time.Hour)},
		{ID: "vm-2", Channel: event.ChLive, Session: "30308220", Summary: "昨天聊到夜宵想吃烤鱼", Importance: 0.45, Confidence: 0.9, ValidFrom: now.Add(-48 * time.Hour)},
		{ID: "vm-3", Channel: event.ChLive, Session: "30308220", Summary: "9月3日给鱼喂过饭", Importance: 0.95, Confidence: 0.9, ValidFrom: now.Add(-60 * 24 * time.Hour)},
	}
	for i := 0; i < 5; i++ {
		records = append(records, memory.Record{
			ID: fmt.Sprintf("vm-old-%d", i), Channel: event.ChLive, Session: "30308220",
			Summary: fmt.Sprintf("很久以前第%d次喂饭", i), Importance: 0.9 - float64(i)*0.01,
			Confidence: 0.9, ValidFrom: now.Add(-time.Duration(30+i) * 24 * time.Hour),
		})
	}
	return records
}

// TestViewerTextInjectsViewerMemories 验收核心：
// 「记得/喂过饭/昨天聊天/昨晚顶号」所需记忆在快照里直接可见，不靠工具关键词。
func TestViewerTextInjectsViewerMemories(t *testing.T) {
	now := viewerMemoryNow()
	base, _ := snapshotTestMemory()
	mem := &fakeViewerRecordReader{fakeMemoryReader: base, records: viewerMemoryHistoryRecords(now)}
	builder := newContextBuilder(snapshotTestPack(), mem, nil, &fakeViewerRepo{profile: viewerMemoryProfile()}, nil, nil, zap.NewNop())

	input, _ := buildPassiveInput(context.Background(), builder, viewerMemoryEvent(now))

	if mem.recordCalls != 1 {
		t.Fatalf("当前发言者的相关记忆必须主动查询：calls=%d", mem.recordCalls)
	}
	if mem.gotQuery.UID != "42" || mem.gotQuery.Limit != viewerMemoryQueryLimit {
		t.Fatalf("记忆查询参数不符：%+v", mem.gotQuery)
	}
	if mem.gotQuery.Scope != (memory.Scope{Channel: event.ChLive, Session: "30308220"}) {
		t.Fatalf("记忆查询 Scope 不符：%+v", mem.gotQuery.Scope)
	}

	got := input.Viewer
	for _, want := range []string{
		"【对方档案 · 老熟人】",
		"最近 7 天聊过的（主动注入，新的在前）",
		"昨晚顶号后跟鱼打了招呼",
		"昨天聊到夜宵想吃烤鱼",
		"跟他相关的记忆（主动注入，按重要度）",
		"9月3日给鱼喂过饭",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("注入片段缺少 %q：\n%s", want, got)
		}
	}
	if n := strings.Count(got, "9月3日给鱼喂过饭"); n != 1 {
		t.Errorf("同一条记忆重复注入 %d 次：\n%s", n, got)
	}
	// 相关段只放高分旧记忆，最近 7 天的两条应落在时间窗段。
	relatedPart, recentPart, ok := strings.Cut(got, "## 最近 7 天聊过的")
	if !ok {
		t.Fatalf("缺少时间窗段：\n%s", got)
	}
	if !strings.Contains(relatedPart, "9月3日给鱼喂过饭") || !strings.Contains(relatedPart, "跟他相关的记忆") {
		t.Errorf("相关段内容不符：\n%s", relatedPart)
	}
	for _, fresh := range []string{"昨晚顶号后跟鱼打了招呼", "昨天聊到夜宵想吃烤鱼"} {
		if !strings.Contains(recentPart, fresh) {
			t.Errorf("时间窗段缺少 %q：\n%s", fresh, recentPart)
		}
	}
	if r := utf8.RuneCountInString(got); r > viewerTextMaxRunes {
		t.Errorf("注入片段 %d 字，超过上限 %d", r, viewerTextMaxRunes)
	}

	// 快照必须能通过生成前校验（长度/非法字符），否则注入等于白做。
	built := builder.BuildContext(context.Background(), viewerMemoryEvent(now), "", false)
	task := brain.Task{ID: "task-vm", EventID: "live:30308220:9501", Channel: event.ChLive, Session: "30308220"}
	if _, err := brain.Prepare(context.Background(), task, built, brain.DefaultBudget()); err != nil {
		t.Fatalf("带观众记忆的快照未通过生成校验：%v", err)
	}
}

// TestViewerTextMemoryFailureDegradesOnlyMemories 记忆查询失败只丢记忆段：
// 档案仍保留，并留下可排查日志，不能伪装成陌生人。
func TestViewerTextMemoryFailureDegradesOnlyMemories(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	base, _ := snapshotTestMemory()
	mem := &fakeViewerRecordReader{fakeMemoryReader: base, err: errors.New("记忆库炸了")}
	builder := newContextBuilder(snapshotTestPack(), mem, nil, &fakeViewerRepo{profile: viewerMemoryProfile()}, nil, nil, zap.New(core))

	got := viewerTextFor(builder, viewerMemoryEvent(viewerMemoryNow()))

	if !strings.Contains(got, "【对方档案 · 老熟人】") || !strings.Contains(got, "今天也要白饭") {
		t.Fatalf("记忆失败不应连带丢档案：%q", got)
	}
	if strings.Contains(got, "主动注入") {
		t.Fatalf("记忆查询失败仍注入了记忆段：%q", got)
	}
	if logs.FilterMessage("观众相关记忆查询失败，本轮不注入记忆").Len() != 1 {
		t.Fatalf("记忆失败未记 1 条 Warn：%+v", logs.All())
	}
}

// TestViewerTextSkipsMemoryWithoutProfileOrCapability 保持既有口径：
// 陌生人（ErrNotFound）不查记忆；记忆源没有可选能力时不注入。
func TestViewerTextSkipsMemoryWithoutProfileOrCapability(t *testing.T) {
	t.Run("陌生人", func(t *testing.T) {
		base, _ := snapshotTestMemory()
		mem := &fakeViewerRecordReader{fakeMemoryReader: base, records: viewerMemoryRecords(viewerMemoryNow())}
		viewers := &fakeViewerRepo{err: fmt.Errorf("查档案：%w", store.ErrViewerNotFound)}
		builder := newContextBuilder(snapshotTestPack(), mem, nil, viewers, nil, nil, zap.NewNop())

		if got := viewerTextFor(builder, viewerMemoryEvent(viewerMemoryNow())); got != "" {
			t.Fatalf("陌生人不应注入：%q", got)
		}
		if mem.recordCalls != 0 {
			t.Fatalf("陌生人不应查相关记忆：calls=%d", mem.recordCalls)
		}
	})

	t.Run("记忆源无能力", func(t *testing.T) {
		mem, _ := snapshotTestMemory()
		builder := newContextBuilder(snapshotTestPack(), mem, nil, &fakeViewerRepo{profile: viewerMemoryProfile()}, nil, nil, zap.NewNop())

		got := viewerTextFor(builder, viewerMemoryEvent(viewerMemoryNow()))
		if !strings.Contains(got, "【对方档案 · 老熟人】") {
			t.Fatalf("档案应照常注入：%q", got)
		}
		if strings.Contains(got, "主动注入") {
			t.Fatalf("记忆源无能力时不应注入记忆：%q", got)
		}
	})
}

// TestViewerMemorySegmentsCapsDedupAndBudget 直接测渲染口径：
// 相关最多 6 条、近期最多 4 条、按重要度排序、两段去重、总长受预算约束。
func TestViewerMemorySegmentsCapsDedupAndBudget(t *testing.T) {
	now := viewerMemoryNow()
	records := make([]memory.Record, 0, 20)
	for i := 0; i < 10; i++ {
		records = append(records, memory.Record{
			ID: fmt.Sprintf("recent-%02d", i), Summary: fmt.Sprintf("近期记忆%02d", i),
			Importance: 0.2 + float64(i)*0.01, Confidence: 0.9,
			ValidFrom: now.Add(-time.Duration(i) * 12 * time.Hour),
		})
	}
	for i := 0; i < 10; i++ {
		records = append(records, memory.Record{
			ID: fmt.Sprintf("old-%02d", i), Summary: fmt.Sprintf("旧记忆%02d", i),
			Importance: 0.9 - float64(i)*0.01, Confidence: 0.9,
			ValidFrom: now.Add(-time.Duration(30+i) * 24 * time.Hour),
		})
	}

	base, _ := snapshotTestMemory()
	mem := &fakeViewerRecordReader{fakeMemoryReader: base, records: records}
	builder := newContextBuilder(snapshotTestPack(), mem, nil, &fakeViewerRepo{profile: viewerMemoryProfile()}, nil, nil, zap.NewNop())

	segments := builder.viewerMemorySegments(context.Background(), viewerMemoryEvent(now))
	if len(segments) != 2 {
		t.Fatalf("期望相关/近期两段，实际 %d 段", len(segments))
	}
	got := composeViewerText(viewerTextMaxRunes, "", segments)

	countItems := func(s string) int {
		n := 0
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "- ") {
				n++
			}
		}
		return n
	}
	relatedPart, recentPart, ok := strings.Cut(got, "## 最近 7 天聊过的")
	if !ok {
		t.Fatalf("缺少近期记忆段：\n%s", got)
	}
	if n := countItems(relatedPart); n != viewerMemoryRelatedMax {
		t.Errorf("相关段 %d 条，期望 %d：\n%s", n, viewerMemoryRelatedMax, relatedPart)
	}
	if n := countItems(recentPart); n != viewerMemoryRecentMax {
		t.Errorf("近期段 %d 条，期望 %d：\n%s", n, viewerMemoryRecentMax, recentPart)
	}
	if !strings.Contains(relatedPart, "旧记忆00") {
		t.Errorf("相关段应按重要度带出最高分记忆：\n%s", relatedPart)
	}
	if strings.Contains(recentPart, "旧记忆") {
		t.Errorf("超出 7 天的不应进近期段：\n%s", recentPart)
	}
	for _, record := range records {
		if strings.Count(got, record.Summary) > 1 {
			t.Errorf("记忆 %q 在两段中重复出现：\n%s", record.Summary, got)
		}
	}

	const small = 80
	if short := composeViewerText(small, "", segments); utf8.RuneCountInString(short) > small {
		t.Errorf("预算 %d 未生效，实际 %d 字", small, utf8.RuneCountInString(short))
	}
}

// fakeProfileFinder 是 profileFinder 的测试桩，按昵称返回可控检索结果。
type fakeProfileFinder struct {
	results map[string]viewer.Result
	err     error

	calls    int
	gotQuery string
}

func (f *fakeProfileFinder) Find(_ context.Context, query string, limit int) (viewer.Result, error) {
	f.calls++
	f.gotQuery = query
	if f.err != nil {
		return viewer.Result{}, f.err
	}
	result, ok := f.results[query]
	if !ok {
		return viewer.Result{Status: viewer.StatusNotFound}, nil
	}
	if limit > 0 && len(result.Candidates) > limit {
		result.Candidates = result.Candidates[:limit]
	}
	return result, nil
}

// injectViewerProfile 造一份「房间里能说上话」的观众档案。
func injectViewerProfile(uid, name string, hits int, summary string) store.ViewerProfile {
	return store.ViewerProfile{
		UID: uid, Name: name, Hits: hits, Replies: hits / 3,
		LastSeen:       time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC),
		RecentTexts:    []string{"今天也在"},
		ProfileSummary: summary,
	}
}

// TestViewerMemoryVisibleInPromptWithoutTools 娇娇 7 案例验收：
// 「记得/喂饭/昨天/昨晚」与「甲问乙」都不依赖工具调用，
// 实际送进模型的 prompt 里能直接看到当前观众记忆与乙的档案。
func TestViewerMemoryVisibleInPromptWithoutTools(t *testing.T) {
	now := viewerMemoryNow()
	prov := &captureProvider{}
	prov.reply = func(provider.Request) string { return "记得" }
	engine, err := brain.New(prov, &fakeUsageRepo{}, func() brain.Options {
		o := brain.DefaultOptions()
		o.Model = "capture-model"
		o.Backoff = time.Millisecond
		return o
	}(), zap.NewNop())
	if err != nil {
		t.Fatalf("Brain 装配失败：%v", err)
	}

	profiles := map[string]store.ViewerProfile{
		"42":   viewerMemoryProfile(),
		"7001": injectViewerProfile("7001", "贞德", 7, "常在夜里出没"),
		"7002": injectViewerProfile("7002", "阿明", 4, "周末才来"),
		"7003": injectViewerProfile("7003", "今夜琴声温柔", 9, "开播就来"),
	}
	viewers := &fakeViewerRepo{
		profiles: profiles,
		top: []store.ViewerProfile{
			profiles["42"], profiles["7001"], profiles["7002"], profiles["7003"],
			{UID: "8008", Name: "路人甲", Hits: 3, LastSeen: now},
		},
	}
	finder := &fakeProfileFinder{results: map[string]viewer.Result{
		"阿明": {
			Status: viewer.StatusFound,
			Candidates: []viewer.Candidate{{
				ViewerProfileHit: store.ViewerProfileHit{Profile: profiles["7002"]},
			}},
		},
	}}
	focus := []memory.Message{
		{EventID: "live:focus:1", Channel: event.ChLive, Session: "30308220", Dir: event.DirIn, FromID: "7001", FromName: "贞德", Text: "晚上好", At: now.Add(-3 * time.Minute)},
		{EventID: "live:focus:2", Channel: event.ChLive, Session: "30308220", Dir: event.DirIn, FromID: "7003", FromName: "今夜琴声温柔", Text: "来了", At: now.Add(-2 * time.Minute)},
	}
	base, _ := snapshotTestMemory()
	mem := &fakeViewerRecordReader{fakeMemoryReader: base, records: viewerMemoryRecords(now), focus: focus}

	cases := []struct {
		text       string
		meta       []byte
		wantTarget string
		wantRoster bool
	}{
		{"记得我吗", nil, "", true},
		{"有没有喂过饭", nil, "", true},
		{"记得昨天聊天吗", nil, "", true},
		{"昨晚顶号的事还记得吗", nil, "", true},
		{"贞德是谁", nil, "【被问到的观众 · 贞德】", true},
		{"记得阿明吗", []byte(`{"reply_to_name":"阿明"}`), "【被问到的观众 · 阿明】", true},
		{"记得今夜琴声温柔吗", nil, "【被问到的观众 · 今夜琴声温柔】", true},
	}
	ids := make([]string, len(cases))
	for i := range cases {
		ids[i] = fmt.Sprintf("live:30308220:v%d", i+1)
	}
	workers, err := buildConsumeWorkers(
		scheduler.ChannelPolicy{}, snapshotTestPack(),
		map[event.Channel]brain.Brain{event.ChLive: engine},
		newSyncSender(), newFakeBus(ids...), time.Second,
		mem, nil, viewers, "", nil, nil, nil, nil, zap.NewNop(),
		withViewerFinder(finder),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	for i, tc := range cases {
		id := ids[i]
		claim := liveClaim(id)
		claim.Event.FromID = "42"
		claim.Event.FromName = "娇娇"
		claim.Event.Text = tc.text
		claim.Event.At = now
		claim.Event.Meta = tc.meta
		if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask(id)}); err != nil {
			t.Fatalf("Generate(%q) 失败：%v", tc.text, err)
		}
	}

	requests := prov.snapshot()
	if len(requests) != len(cases) {
		t.Fatalf("模型请求数 = %d，期望 %d（每个问题各一份输入）", len(requests), len(cases))
	}
	for i, req := range requests {
		user := req.Messages[1].Content
		for _, want := range []string{
			"<viewer>",
			"【对方档案 · 老熟人】",
			"昨晚顶号后跟鱼打了招呼",
			"昨天聊到夜宵想吃烤鱼",
			"9月3日给鱼喂过饭",
		} {
			if !strings.Contains(user, want) {
				t.Errorf("问题 %q 的 prompt 缺少 %q：\n%s", cases[i].text, want, user)
			}
		}
		if cases[i].wantTarget == "" {
			if strings.Contains(user, "【被问到的观众") {
				t.Errorf("问题 %q 不应注入乙档案：\n%s", cases[i].text, user)
			}
		} else if !strings.Contains(user, cases[i].wantTarget) {
			t.Errorf("问题 %q 的 prompt 缺少乙档案 %q：\n%s", cases[i].text, cases[i].wantTarget, user)
		}
		hasRoster := strings.Contains(user, "## 房间最近活跃")
		if hasRoster != cases[i].wantRoster {
			t.Errorf("问题 %q 名单注入 = %v，期望 %v：\n%s", cases[i].text, hasRoster, cases[i].wantRoster, user)
		}
	}
	if finder.calls != 1 || finder.gotQuery != "阿明" {
		t.Errorf("按昵称找乙的调用 = %d，query=%q", finder.calls, finder.gotQuery)
	}
}
