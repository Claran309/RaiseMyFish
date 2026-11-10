package viewer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

type savedSummary struct {
	uid     string
	summary string
	at      time.Time
	record  store.MemoryRecord
}

type fakeSummaryStore struct {
	profiles []store.ViewerProfile
	listErr  error
	saveErr  error
	saved    []savedSummary
}

func (f *fakeSummaryStore) ActiveViewers(context.Context, time.Time, int, int) ([]store.ViewerProfile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]store.ViewerProfile(nil), f.profiles...), nil
}

func (f *fakeSummaryStore) SaveProfileSummary(_ context.Context, uid, summary string, at time.Time, record store.MemoryRecord) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, savedSummary{uid: uid, summary: summary, at: at, record: record})
	return nil
}

type fakeEventSource struct {
	byUID map[string][]event.Event
	err   error
}

func (f *fakeEventSource) ViewerRecent(_ context.Context, fromID string, limit int) ([]event.Event, error) {
	if f.err != nil {
		return nil, f.err
	}
	msgs := f.byUID[fromID]
	if len(msgs) > limit {
		msgs = msgs[:limit]
	}
	return append([]event.Event(nil), msgs...), nil
}

type scriptedLLM struct {
	reply string
	err   error
	// calls 记录输入，便于断言不泄露 UID/私密内容。
	systems []string
	users   []string
}

func (l *scriptedLLM) Complete(_ context.Context, system, user string) (string, error) {
	l.systems = append(l.systems, system)
	l.users = append(l.users, user)
	if l.err != nil {
		return "", l.err
	}
	return l.reply, nil
}

type fakeKnowledge struct {
	views []SummaryView
	err   error
}

func (f *fakeKnowledge) UpsertViewer(_ context.Context, view SummaryView) error {
	if f.err != nil {
		return f.err
	}
	f.views = append(f.views, view)
	return nil
}

func testNow() time.Time {
	return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
}

func testProfile(uid string) store.ViewerProfile {
	return store.ViewerProfile{
		UID: uid, Name: "侧写观众", Hits: 12, Replies: 3,
		LastSeen: testNow().Add(-time.Hour),
	}
}

// testMessages 返回新的在前，与 ViewerRecent 的排序一致。
func testMessages(uid string) []event.Event {
	return []event.Event{
		{ID: uid + "-3", Channel: event.ChLive, Dir: event.DirIn, FromID: uid, Text: "最新一句", At: testNow().Add(-time.Minute)},
		{ID: uid + "-2", Channel: event.ChLive, Dir: event.DirIn, FromID: uid, Text: "中间一句", At: testNow().Add(-2 * time.Minute)},
		{ID: uid + "-1", Channel: event.ChLive, Dir: event.DirIn, FromID: uid, Text: "最早一句", At: testNow().Add(-3 * time.Minute)},
	}
}

func newTestSummarizer(t *testing.T, store SummaryStore, events SummaryEventSource, llm SummaryLLM, knowledge SummaryKnowledge) *Summarizer {
	t.Helper()
	s, err := NewSummarizer(store, events, llm, knowledge, SummaryOptions{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSummarizerLLMSuccess(t *testing.T) {
	profile := testProfile("42")
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{profile}}
	es := &fakeEventSource{byUID: map[string][]event.Event{profile.UID: testMessages(profile.UID)}}
	llm := &scriptedLLM{reply: "```json\n" + `{"personality":"话痨","preferences":"猫猫","catchphrases":"绷不住了、绷不住","relationship":"常被鱼回","recent_topics":"白饭"}` + "\n```"}
	kb := &fakeKnowledge{}

	stats, err := newTestSummarizer(t, st, es, llm, kb).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Candidates != 1 || stats.Summarized != 1 || stats.Fallback != 0 || stats.Knowledge != 1 || stats.Failed != 0 {
		t.Fatalf("统计不符：%+v", stats)
	}
	if len(st.saved) != 1 {
		t.Fatalf("应写一条侧写：%+v", st.saved)
	}
	saved := st.saved[0]
	if saved.uid != profile.UID || !saved.at.Equal(testNow().UTC()) {
		t.Fatalf("侧写时间或 UID 不符：%+v", saved)
	}
	for _, want := range []string{"性格：话痨", "偏好：猫猫", "口头禅：绷不住了", "关系：常被鱼回", "最近话题：白饭"} {
		if !strings.Contains(saved.summary, want) {
			t.Fatalf("侧写缺少 %q：%q", want, saved.summary)
		}
	}
	record := saved.record
	if record.Kind != "viewer_profile" || record.Source != "content" || record.Private ||
		record.Subject != memory.SoulSubjectViewer || record.FromID != profile.UID ||
		record.AssertedByID != profile.UID || record.FromName != profile.Name {
		t.Fatalf("记忆记录身份不符：%+v", record)
	}
	if record.Confidence != summaryConfidence || record.ID != "viewer-profile:"+profile.UID {
		t.Fatalf("记忆记录置信度或标识不符：%+v", record)
	}
	if len(record.EventIDs) != 3 || record.EventIDs[0] != profile.UID+"-3" {
		t.Fatalf("记忆来源事件不符：%+v", record.EventIDs)
	}
	if len(kb.views) != 1 {
		t.Fatalf("应写一条知识：%+v", kb.views)
	}
	view := kb.views[0]
	if view.UID != profile.UID || view.Summary != saved.summary || !strings.Contains(view.Stats, "说过 12 次") {
		t.Fatalf("知识条目不符：%+v", view)
	}
	if len(view.Keywords) == 0 || view.Keywords[0] != profile.Name || !strings.Contains(strings.Join(view.Keywords, ","), "绷不住了") {
		t.Fatalf("知识关键词不符：%+v", view.Keywords)
	}
	if len(llm.users) != 1 || strings.Contains(llm.users[0], profile.UID) {
		t.Fatalf("模型输入泄露 UID 或未调用：%q", llm.users)
	}
	if !strings.Contains(llm.users[0], "最早一句") {
		t.Fatalf("模型输入内容不符：%q", llm.users[0])
	}
}

func TestSummarizerFallbacks(t *testing.T) {
	tests := []struct {
		name string
		llm  SummaryLLM
	}{
		{name: "模型报错", llm: &scriptedLLM{err: errors.New("模拟模型失败")}},
		{name: "JSON损坏", llm: &scriptedLLM{reply: "这不是JSON"}},
		{name: "字段全空", llm: &scriptedLLM{reply: `{"personality":"","preferences":"","catchphrases":"","relationship":"","recent_topics":""}`}},
		{name: "未配置模型", llm: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := testProfile("7")
			st := &fakeSummaryStore{profiles: []store.ViewerProfile{profile}}
			es := &fakeEventSource{byUID: map[string][]event.Event{profile.UID: testMessages(profile.UID)}}
			stats, err := newTestSummarizer(t, st, es, tt.llm, nil).RunOnce(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if stats.Fallback != 1 || stats.Summarized != 0 || stats.Failed != 0 {
				t.Fatalf("降级统计不符：%+v", stats)
			}
			if len(st.saved) != 1 {
				t.Fatalf("降级仍应写档案：%+v", st.saved)
			}
			summary := st.saved[0].summary
			for _, want := range []string{"（模型未生成侧写，仅统计）", "说过 12 次", "最近说过：", "最新一句"} {
				if !strings.Contains(summary, want) {
					t.Fatalf("降级摘要缺少 %q：%q", want, summary)
				}
			}
			if st.saved[0].record.Confidence != summaryFallbackConfidence {
				t.Fatalf("降级置信度不符：%v", st.saved[0].record.Confidence)
			}
		})
	}
}

func TestSummarizerSkipsWithoutMessages(t *testing.T) {
	profile := testProfile("8")
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{profile}}
	es := &fakeEventSource{byUID: map[string][]event.Event{}}
	stats, err := newTestSummarizer(t, st, es, nil, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 1 || len(st.saved) != 0 {
		t.Fatalf("无发言应跳过：stats=%+v saved=%+v", stats, st.saved)
	}
}

func TestSummarizerContinuesAfterSingleFailure(t *testing.T) {
	ok := testProfile("ok")
	bad := testProfile("bad")
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{ok, bad}}
	es := &fakeEventSource{byUID: map[string][]event.Event{ok.UID: testMessages(ok.UID), bad.UID: testMessages(bad.UID)}}
	// 第二位写库失败：第一位必须成功，整轮返回安全错误且统计可见。
	wrapped := &failingStore{fakeSummaryStore: st, failUID: bad.UID}
	summarizer, err := NewSummarizer(wrapped, es, nil, nil, SummaryOptions{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	stats, runErr := summarizer.RunOnce(context.Background())
	if runErr == nil {
		t.Fatal("单观众失败应返回聚合错误")
	}
	if stats.Candidates != 2 || stats.Summarized+stats.Fallback != 1 || stats.Failed != 1 || len(st.saved) != 1 || st.saved[0].uid != ok.UID {
		t.Fatalf("失败后未继续：stats=%+v saved=%+v", stats, st.saved)
	}
}

type failingStore struct {
	*fakeSummaryStore
	failUID string
}

func (f *failingStore) SaveProfileSummary(ctx context.Context, uid, summary string, at time.Time, record store.MemoryRecord) error {
	if uid == f.failUID {
		return errors.New("模拟写库失败")
	}
	return f.fakeSummaryStore.SaveProfileSummary(ctx, uid, summary, at, record)
}

func TestSummarizerKnowledgeFailureKeepsProfile(t *testing.T) {
	profile := testProfile("9")
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{profile}}
	es := &fakeEventSource{byUID: map[string][]event.Event{profile.UID: testMessages(profile.UID)}}
	kb := &fakeKnowledge{err: errors.New("模拟知识写入失败")}
	stats, err := newTestSummarizer(t, st, es, nil, kb).RunOnce(context.Background())
	if err == nil {
		t.Fatal("知识写入失败应上报")
	}
	if len(st.saved) != 1 {
		t.Fatalf("知识失败不应回滚档案侧写：%+v", st.saved)
	}
	if stats.Fallback != 1 || stats.Failed != 1 || stats.Knowledge != 0 {
		t.Fatalf("知识失败统计不符：%+v", stats)
	}
}

func TestSummarizerListFailure(t *testing.T) {
	st := &fakeSummaryStore{listErr: errors.New("模拟读库失败")}
	stats, err := newTestSummarizer(t, st, &fakeEventSource{}, nil, nil).RunOnce(context.Background())
	if err == nil {
		t.Fatal("读取候选失败应上报")
	}
	if stats.Candidates != 0 {
		t.Fatalf("读取失败不应有候选：%+v", stats)
	}
}

func TestParseSummaryReply(t *testing.T) {
	reply, err := parseSummaryReply("  ```json\n{\"personality\":\"话痨\",\"catchphrases\":\"绷\\n不住\"}\n```  ")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Personality != "话痨" {
		t.Fatalf("personality 解析不符：%+v", reply)
	}
	if reply.Catchphrases != "绷 不住" {
		t.Fatalf("换行应折叠为空格：%q", reply.Catchphrases)
	}
	if _, err := parseSummaryReply(""); err == nil {
		t.Fatal("空输出应拒绝")
	}
	clipped, err := parseSummaryReply(`{"personality":"` + strings.Repeat("长", 100) + `"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(clipped.Personality)) != 40 {
		t.Fatalf("超长字段未截断：%d", len([]rune(clipped.Personality)))
	}
}

func TestNewSummarizerOptions(t *testing.T) {
	st := &fakeSummaryStore{}
	events := &fakeEventSource{}
	summarizer, err := NewSummarizer(st, events, nil, nil, SummaryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summarizer.opts.MinHits != 2 || summarizer.opts.Window != 24*time.Hour ||
		summarizer.opts.RecentLimit != 20 || summarizer.opts.MaxPerRun != 100 {
		t.Fatalf("默认参数不符：%+v", summarizer.opts)
	}
	bad := []SummaryOptions{
		{MinHits: -1},
		{Window: 30 * time.Minute},
		{RecentLimit: 51},
		{MaxPerRun: 501},
	}
	for _, opts := range bad {
		if _, err := NewSummarizer(st, events, nil, nil, opts); err == nil {
			t.Fatalf("非法参数未拒绝：%+v", opts)
		}
	}
	if _, err := NewSummarizer(nil, events, nil, nil, SummaryOptions{}); err == nil {
		t.Fatal("缺少存储应拒绝")
	}
	if _, err := NewSummarizer(st, nil, nil, nil, SummaryOptions{}); err == nil {
		t.Fatal("缺少事件来源应拒绝")
	}
}

func TestFallbackSummaryCapsSamples(t *testing.T) {
	msgs := []event.Event{
		{ID: "a", Text: "一"}, {ID: "b", Text: "二"}, {ID: "c", Text: "三"}, {ID: "d", Text: "四"},
	}
	got := fallbackSummary(msgs, "说过 4 次")
	if !strings.Contains(got, "一 / 二 / 三") || strings.Contains(got, "四") {
		t.Fatalf("降级样本应只取最近三条：%q", got)
	}
	if got := fallbackSummary(nil, "统计"); !strings.Contains(got, "仅统计") || strings.Contains(got, "最近说过") {
		t.Fatalf("无发言不应拼最近说过：%q", got)
	}
}
