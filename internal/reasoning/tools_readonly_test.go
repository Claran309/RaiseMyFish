package reasoning

// 本文件测 7 个只读工具：注册、参数校验、截断、未配置降级。

import (
	"context"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

type fakeViewer struct {
	rows     []event.Event
	err      error
	gotID    string
	gotLimit int
}

func (f *fakeViewer) ViewerRecent(_ context.Context, fromID string, limit int) ([]event.Event, error) {
	f.gotID, f.gotLimit = fromID, limit
	return f.rows, f.err
}

type fakeReplies struct {
	rows     []event.Event
	err      error
	gotCh    event.Channel
	gotSess  string
	gotLimit int
}

func (f *fakeReplies) RecentReplies(_ context.Context, ch event.Channel, session string, limit int) ([]event.Event, error) {
	f.gotCh, f.gotSess, f.gotLimit = ch, session, limit
	return f.rows, f.err
}

type fakeLive struct {
	out  string
	err  error
	room string
}

func (f *fakeLive) LiveStatus(_ context.Context, room string) (string, error) {
	f.room = room
	return f.out, f.err
}

type fakeQuotaSrc struct {
	used int
	err  error
	day  string
}

func (f *fakeQuotaSrc) Used(day string) (int, error) {
	f.day = day
	return f.used, f.err
}

type fakeSearch struct {
	out   string
	err   error
	query string
}

func (f *fakeSearch) Search(_ context.Context, query string) (string, error) {
	f.query = query
	return f.out, f.err
}

type fakeFrame struct {
	path string
	err  error
}

func (f *fakeFrame) Snapshot(context.Context) (string, error) { return f.path, f.err }

type fakeFrameRead struct {
	out  string
	err  error
	path string
}

func (f *fakeFrameRead) Describe(_ context.Context, path string) (string, error) {
	f.path = path
	return f.out, f.err
}

type fakeViewerProfileSearch struct {
	hits    []store.ViewerProfileHit
	err     error
	gotQ    string
	gotLim  int
	gotCall int
}

func (f *fakeViewerProfileSearch) Search(_ context.Context, query string, limit int) ([]store.ViewerProfileHit, error) {
	f.gotCall++
	f.gotQ, f.gotLim = query, limit
	return f.hits, f.err
}

func readonlyDeps() ReadOnlyDeps {
	return ReadOnlyDeps{
		Viewer:     &fakeViewer{rows: []event.Event{{Text: "早"}, {Text: "在吃鱼"}}},
		Replies:    &fakeReplies{rows: []event.Event{{Text: "吃了"}}},
		Live:       &fakeLive{out: "在播：钓鱼 人气 1234"},
		Quota:      &fakeQuotaSrc{used: 1},
		QuotaLimit: 2,
		Frame:      &fakeFrame{path: "/tmp/f.jpg"},
		FrameRead:  &fakeFrameRead{out: "画面里有一条鱼。"},
		Search:     &fakeSearch{out: "搜索结果摘要"},
		Room:       "30308220",
		Now:        func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) },
	}
}

// TestReadOnlyToolsRegistered 7 个只读工具全部注册且描述完整。
func TestReadOnlyToolsRegistered(t *testing.T) {
	box, err := BuildReadOnlyTools(readonlyDeps())
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	want := []string{ToolViewerMemory, ToolReplyHistory, ToolLiveStatus, ToolQuota, ToolNow, ToolVision, ToolWebSearch}
	names := box.Names()
	if len(names) != len(want) {
		t.Fatalf("工具数 = %d，期望 %d：%v", len(names), len(want), names)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("第 %d 个 = %q，期望 %q", i, names[i], name)
		}
	}
	desc := box.Descriptions()
	for _, name := range want {
		if !strings.Contains(desc, name) {
			t.Errorf("描述缺少 %s", name)
		}
	}
}

// TestViewerMemoryArgsAndTruncation 参数透传 + 结果截断 ≤500 字。
func TestViewerMemoryArgsAndTruncation(t *testing.T) {
	d := readonlyDeps()
	viewer := &fakeViewer{rows: []event.Event{{Text: strings.Repeat("鱼", 400)}, {Text: strings.Repeat("猫", 400)}}}
	d.Viewer = viewer
	box, _ := BuildReadOnlyTools(d)

	got, err := box.Run(context.Background(), ToolViewerMemory, `{"from_id":"12345","limit":5}`)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if viewer.gotID != "12345" || viewer.gotLimit != 5 {
		t.Errorf("参数透传不符：id=%q limit=%d", viewer.gotID, viewer.gotLimit)
	}
	if n := len([]rune(got)); n > maxReadOnlyRunes+1 {
		t.Errorf("结果未截断：%d rune", n)
	}
}

// TestViewerMemoryRejectsBadArgs 非数字 from_id、越界 limit 都要拒绝。
func TestViewerMemoryRejectsBadArgs(t *testing.T) {
	box, _ := BuildReadOnlyTools(readonlyDeps())
	for _, args := range []string{`{"from_id":"abc"}`, `{"from_id":"12","limit":99}`, `{}`} {
		if _, err := box.Run(context.Background(), ToolViewerMemory, args); err == nil {
			t.Errorf("非法参数未被拒绝：%s", args)
		}
	}
}

// TestViewerSearchOptionalTool 注入档案检索时才注册 viewer_search，查询参数透传。
func TestViewerSearchOptionalTool(t *testing.T) {
	d := readonlyDeps()
	search := &fakeViewerProfileSearch{hits: []store.ViewerProfileHit{{
		Profile: store.ViewerProfile{UID: "2002", Name: "白饭哥", RecentTexts: []string{"今天也要白饭"}},
	}}}
	d.ViewerSearch = search
	box, err := BuildReadOnlyTools(d)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	names := box.Names()
	if len(names) != 8 || names[1] != ToolViewerSearch {
		t.Fatalf("应额外注册 viewer_search：%v", names)
	}
	if !strings.Contains(box.Descriptions(), ToolViewerSearch) {
		t.Error("描述里应列出 viewer_search")
	}

	got, err := box.Run(context.Background(), ToolViewerSearch, "  那个爱要白饭的人 ")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if search.gotCall != 1 || search.gotQ != "那个爱要白饭的人" || search.gotLim != 3 {
		t.Errorf("查询参数不符：calls=%d q=%q limit=%d", search.gotCall, search.gotQ, search.gotLim)
	}
	if !strings.Contains(got, "2002") || !strings.Contains(got, "白饭哥") || !strings.Contains(got, "今天也要白饭") {
		t.Errorf("结果格式不符：%q", got)
	}

	if _, err := box.Run(context.Background(), ToolViewerSearch, "  "); err == nil {
		t.Error("空查询应报错")
	}
	if _, err := box.Run(context.Background(), ToolViewerSearch, strings.Repeat("鱼", 101)); err == nil {
		t.Error("超长查询应报错")
	}
}

// TestViewerSearchEmptyResult 没有命中时给明确文案。
func TestViewerSearchEmptyResult(t *testing.T) {
	d := readonlyDeps()
	d.ViewerSearch = &fakeViewerProfileSearch{}
	box, _ := BuildReadOnlyTools(d)
	got, err := box.Run(context.Background(), ToolViewerSearch, "不存在的人")
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if got != "没有找到匹配的观众" {
		t.Errorf("空结果文案 = %q", got)
	}
}

// TestReplyHistoryLimitAndScope 回复史按本房间查询，limit 透传。
func TestReplyHistoryLimitAndScope(t *testing.T) {
	d := readonlyDeps()
	replies := &fakeReplies{rows: []event.Event{{Text: "吃了"}}}
	d.Replies = replies
	box, _ := BuildReadOnlyTools(d)

	if _, err := box.Run(context.Background(), ToolReplyHistory, `{"limit":3}`); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if replies.gotLimit != 3 || replies.gotCh != event.ChLive || replies.gotSess != "30308220" {
		t.Errorf("查询范围不符：limit=%d ch=%s sess=%s", replies.gotLimit, replies.gotCh, replies.gotSess)
	}
	if _, err := box.Run(context.Background(), ToolReplyHistory, `{"limit":99}`); err == nil {
		t.Error("越界 limit 应被拒绝")
	}
}

// TestQuotaAndNow 额度按当天计算；时间按本地格式。
func TestQuotaAndNow(t *testing.T) {
	d := readonlyDeps()
	quota := &fakeQuotaSrc{used: 1}
	d.Quota = quota
	box, _ := BuildReadOnlyTools(d)

	got, err := box.Run(context.Background(), ToolQuota, "")
	if err != nil {
		t.Fatalf("quota 失败：%v", err)
	}
	if !strings.Contains(got, "已送 1") || !strings.Contains(got, "还剩 1") {
		t.Errorf("额度文案不符：%q", got)
	}
	if quota.day != "2026-11-03" {
		t.Errorf("查询日期 = %q", quota.day)
	}

	now, err := box.Run(context.Background(), ToolNow, "")
	if err != nil {
		t.Fatalf("now 失败：%v", err)
	}
	if !strings.HasPrefix(now, "2026-11-03 12:00:00") {
		t.Errorf("时间格式不符：%q", now)
	}
}

// TestVisionUsesFrameAndReader 画面工具串起截帧 + 识图。
func TestVisionUsesFrameAndReader(t *testing.T) {
	d := readonlyDeps()
	read := &fakeFrameRead{out: "画面里有一条鱼在啃面包。"}
	d.FrameRead = read
	box, _ := BuildReadOnlyTools(d)

	got, err := box.Run(context.Background(), ToolVision, "")
	if err != nil {
		t.Fatalf("vision 失败：%v", err)
	}
	if read.path != "/tmp/f.jpg" {
		t.Errorf("识图路径 = %q", read.path)
	}
	if !strings.Contains(got, "啃面包") {
		t.Errorf("描述未返回：%q", got)
	}
}

// TestWebSearchValidationAndFallback 搜索参数校验 + 未配置时明确说明。
func TestWebSearchValidationAndFallback(t *testing.T) {
	d := readonlyDeps()
	box, _ := BuildReadOnlyTools(d)
	if _, err := box.Run(context.Background(), ToolWebSearch, "  "); err == nil {
		t.Error("空查询应报错")
	}
	if _, err := box.Run(context.Background(), ToolWebSearch, strings.Repeat("查", 101)); err == nil {
		t.Error("超长查询应报错")
	}

	// 未配置搜索源：返回说明而不是编造
	d2 := readonlyDeps()
	d2.Search = nil
	box2, _ := BuildReadOnlyTools(d2)
	got, err := box2.Run(context.Background(), ToolWebSearch, "今天几号")
	if err != nil {
		t.Fatalf("未配置不应报错：%v", err)
	}
	if !strings.Contains(got, "未配置") {
		t.Errorf("应说明未配置：%q", got)
	}
}

// TestReadOnlyNilSourcesDegrade 数据源缺失时给「未配置」，不 panic。
func TestReadOnlyNilSourcesDegrade(t *testing.T) {
	box, err := BuildReadOnlyTools(ReadOnlyDeps{Room: "1"})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	cases := map[string]string{
		ToolViewerMemory: `{"from_id":"1"}`,
		ToolReplyHistory: "",
		ToolLiveStatus:   "",
		ToolQuota:        "",
		ToolVision:       "",
		ToolWebSearch:    "天气",
	}
	for name, args := range cases {
		got, err := box.Run(context.Background(), name, args)
		if err != nil {
			t.Errorf("%s 未配置时不应报错：%v", name, err)
			continue
		}
		if !strings.Contains(got, "未配置") {
			t.Errorf("%s 应说明未配置，实际 %q", name, got)
		}
	}
}
