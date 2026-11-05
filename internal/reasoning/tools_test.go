package reasoning

// 本文件测四个工具：装配、格式化、范围、异常路径。

import (
	"context"
	"errors"
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
	gotCh    event.Channel
	gotSess  string
	calls    int
}

func (f *fakeHist) RecentEvents(_ context.Context, ch event.Channel, session string, limit int) ([]event.Event, error) {
	f.calls++
	f.gotCh, f.gotSess, f.gotLimit = ch, session, limit
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

// TestMemoryToolEmpty 没召回到内容时给明确文案，不是空串。
func TestMemoryToolEmpty(t *testing.T) {
	box, _ := BuildTools(ToolDeps{Memory: &fakeMem{}, Scope: testScope()})
	got, err := box.Run(context.Background(), ToolMemorySearch, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "没有相关记忆" {
		t.Errorf("空结果文案 = %q", got)
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
	if hist.gotCh != event.ChLive || hist.gotSess != "30308220" {
		t.Errorf("查询范围 = %s/%s", hist.gotCh, hist.gotSess)
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
