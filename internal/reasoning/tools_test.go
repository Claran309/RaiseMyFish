package reasoning

// 本文件测四个工具：装配、格式化、范围、异常路径。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
)

type fakeMem struct {
	ctx memory.Context
	err error
	got memory.Scope
}

func (f *fakeMem) Recall(_ context.Context, s memory.Scope) (memory.Context, error) {
	f.got = s
	return f.ctx, f.err
}

// fakeQueryMem 额外实现带查询词的混合检索。
type fakeQueryMem struct {
	fakeMem
	queryCtx    memory.Context
	queryErr    error
	gotQuery    memory.SearchQuery
	queryCalls  int
	recallCalls int
}

func (f *fakeQueryMem) Recall(ctx context.Context, s memory.Scope) (memory.Context, error) {
	f.recallCalls++
	return f.fakeMem.Recall(ctx, s)
}

func (f *fakeQueryMem) Search(_ context.Context, q memory.SearchQuery) (memory.Context, error) {
	f.queryCalls++
	f.gotQuery = q
	return f.queryCtx, f.queryErr
}

type fakeKnow struct {
	ctx knowledge.Context
	err error
	got knowledge.Query
}

func (f *fakeKnow) Recall(_ context.Context, q knowledge.Query) (knowledge.Context, error) {
	f.got = q
	return f.ctx, f.err
}

type fakeHist struct {
	rows     []event.Event
	err      error
	gotLimit int
	gotScope memory.Scope
	calls    int
}

func (f *fakeHist) RecentEvents(_ context.Context, scope memory.Scope, limit int) ([]event.Event, error) {
	f.calls++
	f.gotScope, f.gotLimit = scope, limit
	return f.rows, f.err
}

func testScope() memory.Scope {
	return memory.Scope{Channel: event.ChLive, Session: "30308220"}
}

// TestBuildToolsAllRegistered 四个依赖齐全时注册四个工具。
func TestBuildToolsAllRegistered(t *testing.T) {
	box, err := BuildTools(ToolDeps{
		Memory:    &fakeMem{},
		Knowledge: &fakeKnow{},
		Presence:  func(time.Time) string { return "在看直播" },
		History:   &fakeHist{},
		Scope:     testScope(),
	})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	names := box.Names()
	want := []string{ToolMemorySearch, ToolKnowledgeSearch, ToolPresence, ToolHistory}
	if len(names) != len(want) {
		t.Fatalf("工具数 = %d，期望 %d：%v", len(names), len(want), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("第 %d 个工具 = %q，期望 %q", i, names[i], want[i])
		}
	}
	if !strings.Contains(box.Descriptions(), ToolMemorySearch) {
		t.Error("描述里应列出工具名")
	}
}

// TestBuildToolsSkipsNilDeps 依赖为 nil 的工具不注册；全 nil 直接报错。
func TestBuildToolsSkipsNilDeps(t *testing.T) {
	box, err := BuildTools(ToolDeps{Presence: func(time.Time) string { return "" }, Scope: testScope()})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if len(box.Names()) != 1 || box.Names()[0] != ToolPresence {
		t.Errorf("只应注册 presence：%v", box.Names())
	}

	if _, err := BuildTools(ToolDeps{Scope: testScope()}); err == nil {
		t.Error("一个工具都没有时应报错")
	}
}

// TestMemoryToolUsesEventScope 记忆工具必须用调用方给的 Scope 查询。
func TestMemoryToolUsesEventScope(t *testing.T) {
	mem := &fakeMem{ctx: memory.Context{
		Scope:   testScope(),
		Recent:  []memory.Message{{FromName: "路人", Text: "早"}},
		Records: []memory.Record{{Summary: "喜欢钓鱼"}},
	}}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope()})
	if err != nil {
		t.Fatal(err)
	}

	got, err := box.Run(context.Background(), ToolMemorySearch, "早")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if mem.got != testScope() {
		t.Errorf("Scope = %+v，期望 %+v", mem.got, testScope())
	}
	if !strings.Contains(got, "路人: 早") || !strings.Contains(got, "摘要: 喜欢钓鱼") {
		t.Errorf("格式化不符：%q", got)
	}
}

// TestMemoryToolUsesQuerySearch 有查询词且实现混合检索时走 Search；空查询仍走 Recall。
func TestMemoryToolUsesQuerySearch(t *testing.T) {
	mem := &fakeQueryMem{
		queryCtx: memory.Context{
			Scope:   testScope(),
			Records: []memory.Record{{ID: "m1", Summary: "喜欢白饭", Importance: 0.6, Confidence: 0.8, ValidFrom: time.Now()}},
		},
	}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Run(context.Background(), ToolMemorySearch, " 爱要白饭 ")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if mem.queryCalls != 1 {
		t.Fatalf("混合检索调用次数 = %d，期望 1", mem.queryCalls)
	}
	if mem.gotQuery.Scope != testScope() || mem.gotQuery.Text != "爱要白饭" || mem.gotQuery.Limit != 10 {
		t.Errorf("查询参数不符：%+v", mem.gotQuery)
	}
	if !strings.Contains(got, "摘要: 喜欢白饭") {
		t.Errorf("格式化不符：%q", got)
	}

	// 空查询不消耗 embedding，仍走既有 Recall
	mem.fakeMem.ctx = memory.Context{Scope: testScope()}
	if _, err := box.Run(context.Background(), ToolMemorySearch, ""); err != nil {
		t.Fatal(err)
	}
	if mem.queryCalls != 1 || mem.recallCalls != 1 {
		t.Errorf("空查询应走 Recall：search=%d recall=%d", mem.queryCalls, mem.recallCalls)
	}
}

// TestMemoryToolReturnsRawMessages 摘要为空时也要把 events 原文返回给模型。
func TestMemoryToolReturnsRawMessages(t *testing.T) {
	mem := &fakeQueryMem{queryCtx: memory.Context{
		Scope: testScope(),
		Recent: []memory.Message{{
			EventID: "e1", Channel: event.ChLive, Session: "30308220", Dir: "in",
			FromName: "弓长三益", Text: "打错了很正常", At: time.Now(),
		}},
	}}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Run(context.Background(), ToolMemorySearch, "弓长三益")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !strings.Contains(got, "弓长三益: 打错了很正常") {
		t.Errorf("未返回原文弹幕：%q", got)
	}
}

// TestMemoryToolEmpty 没召回到内容时给明确文案，并标记状态 empty。
func TestMemoryToolEmpty(t *testing.T) {
	box, _ := BuildTools(ToolDeps{Memory: &fakeMem{}, Scope: testScope()})
	got, err := box.Run(context.Background(), ToolMemorySearch, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "状态=empty") || !strings.Contains(got, "没有相关记忆") {
		t.Errorf("空结果文案 = %q", got)
	}
}

// TestMemoryToolStructuredKeepsRoleTimeCrossChannel（C07）多源结果不丢角色/时间，
// 跨通道消息单独成组出现，不再只拼正文后被尾部统一裁掉。
func TestMemoryToolStructuredKeepsRoleTimeCrossChannel(t *testing.T) {
	mem := &fakeMem{ctx: memory.Context{
		Scope: testScope(),
		Recent: []memory.Message{{
			EventID: "e1", Channel: event.ChLive, Session: "30308220", Dir: event.DirIn,
			FromID: "10086", FromName: "路人", Text: "早", At: time.Date(2026, 11, 3, 9, 5, 0, 0, time.UTC),
		}, {
			EventID: "e2", Channel: event.ChLive, Session: "30308220", Dir: event.DirOut,
			FromName: "大肥鱼", Text: "早呀", At: time.Date(2026, 11, 3, 9, 6, 0, 0, time.UTC),
		}},
		CrossChannel: []memory.Message{{
			EventID: "c9", Channel: event.ChComment, Session: "post-1", Dir: event.DirIn,
			FromID: "2002", FromName: "白饭哥", Text: "今天也要白饭",
			At: time.Date(2026, 11, 2, 20, 30, 0, 0, time.UTC),
		}},
		Records: []memory.Record{{
			ID: "m1", Channel: event.ChLive, Session: "30308220", Summary: "喜欢钓鱼",
			Importance: 0.6, Confidence: 0.8, EventIDs: []string{"e1"},
			ValidFrom: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
		}},
	}}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope(), Now: func() time.Time {
		return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Run(context.Background(), ToolMemorySearch, "最近")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	for _, want := range []string{
		"状态=ok", "查询=memory_search:", "范围=live/30308220/公开", "as_of=2026-11-03 12:00:00",
		"[近期]", "观众 路人: 早", "11-03 09:05", "live#e1",
		"本鱼 大肥鱼: 早呀", "11-03 09:06",
		"[跨通道]", "观众 白饭哥: 今天也要白饭", "11-02 20:30", "comment#c9",
		"[摘要]", "摘要: 喜欢钓鱼", "11-01 08:00", "重要0.60 置信0.80",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("结构化结果缺少 %q：%q", want, got)
		}
	}
	if n := len([]rune(got)); n > maxObservationRunes {
		t.Errorf("结果超出 400 字预算：%d", n)
	}
}

// TestMemoryToolBudgetKeepsCrossChannel（C07）长列表下按组轮转，
// 跨通道结果不能被近期消息挤掉，超出预算要注明剩余条数。
func TestMemoryToolBudgetKeepsCrossChannel(t *testing.T) {
	var recent []memory.Message
	for i := 0; i < 8; i++ {
		recent = append(recent, memory.Message{
			EventID: fmt.Sprintf("e%d", i), Channel: event.ChLive, Session: "30308220", Dir: event.DirIn,
			FromName: "话痨", Text: strings.Repeat("鱼", 80),
			At: time.Date(2026, 11, 3, 10, i, 0, 0, time.UTC),
		})
	}
	mem := &fakeMem{ctx: memory.Context{
		Scope:  testScope(),
		Recent: recent,
		CrossChannel: []memory.Message{{
			EventID: "c9", Channel: event.ChComment, Session: "post-1", Dir: event.DirIn,
			FromName: "关键人物", Text: "我在评论区说过关键线索",
			At: time.Date(2026, 11, 2, 20, 30, 0, 0, time.UTC),
		}},
		Records: []memory.Record{{
			ID: "m1", Summary: "重要摘要", Importance: 0.9, Confidence: 0.9,
			ValidFrom: time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC),
		}},
	}}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope(), Now: func() time.Time {
		return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Run(context.Background(), ToolMemorySearch, "鱼")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if !strings.Contains(got, "[跨通道]") || !strings.Contains(got, "关键人物") {
		t.Errorf("跨通道结果被挤出预算：%q", got)
	}
	if !strings.Contains(got, "另有") || !strings.Contains(got, "未展示") {
		t.Errorf("超预算应注明剩余条数：%q", got)
	}
	if n := len([]rune(got)); n > maxObservationRunes {
		t.Errorf("结果超出 400 字预算：%d", n)
	}
}

// TestHistoryKeepsReplyChainAndTime（C07）history 保留角色、时间、事件 ID 与回复链。
func TestHistoryKeepsReplyChainAndTime(t *testing.T) {
	hist := &fakeHist{rows: []event.Event{{
		ID: "e5", Channel: event.ChLive, Session: "30308220", Dir: event.DirIn,
		FromID: "10086", FromName: "甲", Text: "刚才那条呢", ReplyTo: "e4",
		At: time.Date(2026, 11, 3, 11, 20, 0, 0, time.UTC),
	}}}
	box, err := BuildTools(ToolDeps{History: hist, Scope: testScope(), Now: func() time.Time {
		return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Run(context.Background(), ToolHistory, "5")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	for _, want := range []string{
		"状态=ok", "查询=history:", "范围=live/30308220/公开",
		"[弹幕]", "观众 甲: 刚才那条呢", "11-03 11:20", "live#e5", "↩e4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("history 缺少 %q：%q", want, got)
		}
	}
}

// TestToolQueryIDStableAndDistinct 查询标识确定且随参数变化，便于模型引用同一次查询。
func TestToolQueryIDStableAndDistinct(t *testing.T) {
	hist := &fakeHist{rows: []event.Event{{FromName: "甲", Text: "一"}}}
	box, err := BuildTools(ToolDeps{History: hist, Scope: testScope(), Now: func() time.Time {
		return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := box.Run(ctx, ToolHistory, "5")
	if err != nil {
		t.Fatal(err)
	}
	second, err := box.Run(ctx, ToolHistory, "5")
	if err != nil {
		t.Fatal(err)
	}
	other, err := box.Run(ctx, ToolHistory, "6")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("同参数查询标识不稳定：%q vs %q", first, second)
	}
	if first == other {
		t.Errorf("不同参数查询标识相同：%q", first)
	}
}

// TestKnowledgeToolPassesQuery 知识工具把 args 当查询词。
func TestKnowledgeToolPassesQuery(t *testing.T) {
	know := &fakeKnow{ctx: knowledge.Context{Items: []knowledge.Item{{ID: "k1", Text: "拉拉=百合"}}}}
	box, _ := BuildTools(ToolDeps{Knowledge: know, Scope: testScope()})

	got, err := box.Run(context.Background(), ToolKnowledgeSearch, "何意味")
	if err != nil {
		t.Fatal(err)
	}
	if know.got.Text != "何意味" {
		t.Errorf("查询词 = %q", know.got.Text)
	}
	if know.got.Scope.Channel != event.ChLive || know.got.Scope.Session != "30308220" {
		t.Errorf("查询范围 = %+v", know.got.Scope)
	}
	if !strings.Contains(got, "k1: 拉拉=百合") {
		t.Errorf("格式化不符：%q", got)
	}
}

// TestHistoryToolLimit 条数参数：默认 10、显式值生效、越界夹到 20。
func TestHistoryToolLimit(t *testing.T) {
	hist := &fakeHist{rows: []event.Event{{FromName: "甲", Text: "一"}}}
	box, _ := BuildTools(ToolDeps{History: hist, Scope: testScope()})

	cases := []struct {
		args string
		want int
	}{
		{"", 10},
		{"5", 5},
		{"999", 20},
		{"abc", 10},
	}
	for _, tc := range cases {
		if _, err := box.Run(context.Background(), ToolHistory, tc.args); err != nil {
			t.Fatalf("执行失败 %q：%v", tc.args, err)
		}
		if hist.gotLimit != tc.want {
			t.Errorf("args=%q → limit=%d，期望 %d", tc.args, hist.gotLimit, tc.want)
		}
	}
	if hist.gotScope.Channel != event.ChLive || hist.gotScope.Session != "30308220" || hist.gotScope.Private {
		t.Errorf("查询范围 = %+v", hist.gotScope)
	}
}

// TestPresenceToolFallback 状态为空时给固定文案。
func TestPresenceToolFallback(t *testing.T) {
	box, _ := BuildTools(ToolDeps{Presence: func(time.Time) string { return "" }, Scope: testScope()})
	got, err := box.Run(context.Background(), ToolPresence, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "没有进行中的状态" {
		t.Errorf("空状态文案 = %q", got)
	}
}

// TestToolOutputClipped 超长输出会被裁剪，避免撑爆下一轮 prompt。
func TestToolOutputClipped(t *testing.T) {
	long := strings.Repeat("鱼", 1000)
	box, _ := BuildTools(ToolDeps{Memory: &fakeMem{ctx: memory.Context{
		Recent: []memory.Message{{FromName: "a", Text: long}},
	}}, Scope: testScope()})

	got, err := box.Run(context.Background(), ToolMemorySearch, "")
	if err != nil {
		t.Fatal(err)
	}
	if runes := len([]rune(got)); runes > maxObservationRunes+8 {
		t.Errorf("输出未裁剪：%d runes", runes)
	}
}

// TestUnknownToolErrors 未知工具返回错误（不静默成功）。
func TestUnknownToolErrors(t *testing.T) {
	box, _ := BuildTools(ToolDeps{Presence: func(time.Time) string { return "x" }, Scope: testScope()})
	if _, err := box.Run(context.Background(), "nope", ""); err == nil {
		t.Error("未知工具应报错")
	}
	var nilBox *Toolbox
	if _, err := nilBox.Run(context.Background(), "x", ""); err == nil {
		t.Error("nil toolbox 应报错")
	}
}

// TestToolErrorPropagates 工具内部错误原样上抛，由 Agent 决定是否转成观察。
func TestToolErrorPropagates(t *testing.T) {
	boom := errors.New("查库炸了")
	box, _ := BuildTools(ToolDeps{Memory: &fakeMem{err: boom}, Scope: testScope()})
	if _, err := box.Run(context.Background(), ToolMemorySearch, ""); !errors.Is(err, boom) {
		t.Errorf("错误未透传：%v", err)
	}
}

// memoryTimeTestNow 是时间窗测试的固定时钟（本地时区，避免 UTC 跨日）。
func memoryTimeTestNow() time.Time {
	return time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
}

// TestMemoryToolParsesYesterdayWindow 「记得昨天聊天」要剥掉时间词与问句外壳。
func TestMemoryToolParsesYesterdayWindow(t *testing.T) {
	mem := &fakeQueryMem{queryCtx: memory.Context{
		Scope: testScope(),
		Records: []memory.Record{{
			ID: "m1", Summary: "昨天聊到白饭", Importance: 0.6, Confidence: 0.8,
			ValidFrom: memoryTimeTestNow().Add(-24 * time.Hour),
		}},
	}}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope(), Now: memoryTimeTestNow})
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Run(context.Background(), ToolMemorySearch, "记得昨天聊天吗")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if mem.queryCalls != 1 {
		t.Fatalf("检索次数 = %d，期望 1", mem.queryCalls)
	}
	wantAfter := time.Date(2026, 11, 9, 0, 0, 0, 0, time.Local)
	if mem.gotQuery.Text != "聊天" {
		t.Errorf("关键词 = %q，期望「聊天」", mem.gotQuery.Text)
	}
	if !mem.gotQuery.After.Equal(wantAfter) || !mem.gotQuery.Before.Equal(wantAfter.Add(24*time.Hour)) {
		t.Errorf("时间窗 = %v ~ %v", mem.gotQuery.After, mem.gotQuery.Before)
	}
	if !strings.Contains(got, "时间窗：昨天") {
		t.Errorf("结果缺少时间窗说明：%q", got)
	}
}

// TestMemoryToolLastNightWindowAndBareFallback 「昨晚顶号的事」关键词无结果时
// 退回只看时间窗，并保留时间窗说明。
func TestMemoryToolLastNightWindowAndBareFallback(t *testing.T) {
	mem := &fakeQueryMem{queryCtx: memory.Context{Scope: testScope()}}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope(), Now: memoryTimeTestNow})
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Run(context.Background(), ToolMemorySearch, "昨晚顶号的事")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if mem.queryCalls != 2 {
		t.Fatalf("应先关键词再回退时间窗，实际 %d 次", mem.queryCalls)
	}
	if mem.gotQuery.Text != "" {
		t.Errorf("回退查询应清空关键词，实际 %q", mem.gotQuery.Text)
	}
	wantAfter := time.Date(2026, 11, 9, 18, 0, 0, 0, time.Local)
	wantBefore := time.Date(2026, 11, 10, 6, 0, 0, 0, time.Local)
	if !mem.gotQuery.After.Equal(wantAfter) || !mem.gotQuery.Before.Equal(wantBefore) {
		t.Errorf("昨晚窗口 = %v ~ %v", mem.gotQuery.After, mem.gotQuery.Before)
	}
	if !strings.Contains(got, "时间窗：昨晚") || !strings.Contains(got, "没有相关记忆") {
		t.Errorf("结果说明不符：%q", got)
	}
}

// TestMemoryToolPureTimeQueryRecallsWindow 只问「昨天」时也要按时间窗召回。
func TestMemoryToolPureTimeQueryRecallsWindow(t *testing.T) {
	mem := &fakeQueryMem{queryCtx: memory.Context{Scope: testScope()}}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope(), Now: memoryTimeTestNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Run(context.Background(), ToolMemorySearch, "昨天"); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if mem.queryCalls != 1 || mem.gotQuery.Text != "" {
		t.Errorf("纯时间查询应只按窗口召回：calls=%d text=%q", mem.queryCalls, mem.gotQuery.Text)
	}
	if mem.gotQuery.After.IsZero() || mem.gotQuery.Before.IsZero() {
		t.Errorf("缺少时间窗：%+v", mem.gotQuery)
	}
}

// TestMemoryToolRefinesFeedingVariant 「有没有喂过饭」去「过」后应能命中「喂饭」。
func TestMemoryToolRefinesFeedingVariant(t *testing.T) {
	mem := &fakeQueryMem{queryCtx: memory.Context{Scope: testScope()}}
	box, err := BuildTools(ToolDeps{Memory: mem, Scope: testScope(), Now: memoryTimeTestNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Run(context.Background(), ToolMemorySearch, "有没有喂过饭"); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if mem.queryCalls != 2 || mem.gotQuery.Text != "喂饭" {
		t.Errorf("变体回退不符：calls=%d text=%q", mem.queryCalls, mem.gotQuery.Text)
	}
	if !mem.gotQuery.After.IsZero() || !mem.gotQuery.Before.IsZero() {
		t.Errorf("无时间词不应带时间窗：%+v", mem.gotQuery)
	}
}

// TestParseMemoryTimeWindow 只认明确说法，半开区间与本地时区口径固定。
func TestParseMemoryTimeWindow(t *testing.T) {
	now := memoryTimeTestNow()
	yesterday := time.Date(2026, 11, 9, 0, 0, 0, 0, time.Local)
	cases := []struct {
		query      string
		wantText   string
		wantName   string
		wantAfter  time.Time
		wantBefore time.Time
	}{
		{"昨天聊天", "聊天", "昨天", yesterday, yesterday.Add(24 * time.Hour)},
		{"昨日说过", "说过", "昨天", yesterday, yesterday.Add(24 * time.Hour)},
		{"前天的事", "的事", "前天", yesterday.AddDate(0, 0, -1), yesterday},
		{"昨晚", "", "昨晚", yesterday.Add(18 * time.Hour), time.Date(2026, 11, 10, 6, 0, 0, 0, time.Local)},
		{"上次说过", "说过", "上次（最近7天）", now.Add(-7 * 24 * time.Hour), now},
		{"最近", "最近", "", time.Time{}, time.Time{}},
	}
	for _, tc := range cases {
		text, window := parseMemoryTimeWindow(tc.query, now)
		if text != tc.wantText || window.Name != tc.wantName {
			t.Errorf("%q → text=%q name=%q，期望 %q/%q", tc.query, text, window.Name, tc.wantText, tc.wantName)
			continue
		}
		if !window.After.Equal(tc.wantAfter) || !window.Before.Equal(tc.wantBefore) {
			t.Errorf("%q 时间窗 = %v ~ %v，期望 %v ~ %v", tc.query, window.After, window.Before, tc.wantAfter, tc.wantBefore)
		}
	}
}

// TestCleanMemoryKeywordAndVariants 问句外壳剥离与口语变体。
func TestCleanMemoryKeywordAndVariants(t *testing.T) {
	if got := cleanMemoryKeyword("还记得今夜琴声温柔吗"); got != "今夜琴声温柔" {
		t.Errorf("清洗结果 = %q", got)
	}
	if got := cleanMemoryKeyword("记得 聊天吗"); got != "聊天" {
		t.Errorf("清洗结果 = %q", got)
	}
	if got := cleanMemoryKeyword("有没有喂过饭"); got != "喂过饭" {
		t.Errorf("清洗结果 = %q", got)
	}
	got := memoryKeywordVariants("喂过饭")
	if len(got) != 2 || got[0] != "喂过饭" || got[1] != "喂饭" {
		t.Errorf("变体 = %v", got)
	}
	if got := memoryKeywordVariants("  "); got != nil {
		t.Errorf("空关键词不应有变体：%v", got)
	}
}

// TestNewToolboxRejectsBad 空名/重名/nil Run 都算配置错误。
func TestNewToolboxRejectsBad(t *testing.T) {
	noop := func(context.Context, string) (string, error) { return "", nil }
	if _, err := NewToolbox(Tool{Name: " ", Run: noop}); err == nil {
		t.Error("空名应报错")
	}
	if _, err := NewToolbox(Tool{Name: "a"}); err == nil {
		t.Error("Run 为空应报错")
	}
	if _, err := NewToolbox(Tool{Name: "a", Run: noop}, Tool{Name: "a", Run: noop}); err == nil {
		t.Error("重名应报错")
	}
}
