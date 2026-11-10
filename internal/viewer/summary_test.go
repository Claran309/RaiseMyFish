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

type fakeSummaryStore struct {
	profiles []store.ViewerProfile
	listErr  error
	saveErr  error
	saved    []store.ViewerSummaryWrite
	deferred []store.ViewerSummaryDefer
	revoked  []string
	jobs     []store.ViewerSummaryJob
	finished []string
	failed   []summaryJobFailure
}

type summaryJobFailure struct {
	uid      string
	action   string
	attempts int
	next     time.Time
}

func (f *fakeSummaryStore) ActiveViewers(context.Context, time.Time, time.Time, int, int) ([]store.ViewerProfile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]store.ViewerProfile(nil), f.profiles...), nil
}

func (f *fakeSummaryStore) GetProfile(_ context.Context, uid string) (store.ViewerProfile, error) {
	for _, profile := range f.profiles {
		if profile.UID == uid {
			return profile, nil
		}
	}
	return store.ViewerProfile{}, store.ErrViewerNotFound
}

func (f *fakeSummaryStore) SaveProfileSummary(_ context.Context, w store.ViewerSummaryWrite) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, w)
	for i := range f.profiles {
		if f.profiles[i].UID != w.UID {
			continue
		}
		f.profiles[i].ProfileSummary = w.Summary
		f.profiles[i].SummaryUpdatedAt = w.At
		f.profiles[i].SummarySourceHash = w.SourceHash
		f.profiles[i].SummaryGenerator = w.Generator
		f.profiles[i].SummaryFailures = w.Failures
		f.profiles[i].SummaryAttemptedAt = w.At
		f.profiles[i].SummaryNextRetryAt = w.NextRetryAt
		if !w.Fallback {
			f.profiles[i].SummaryLastSuccessAt = w.At
		}
	}
	f.enqueue(store.ViewerSummaryJob{UID: w.UID, Action: store.ViewerSummaryActionPublish})
	return nil
}

func (f *fakeSummaryStore) DeferProfileSummary(_ context.Context, d store.ViewerSummaryDefer) error {
	f.deferred = append(f.deferred, d)
	for i := range f.profiles {
		if f.profiles[i].UID != d.UID {
			continue
		}
		f.profiles[i].SummaryFailures = d.Failures
		f.profiles[i].SummaryAttemptedAt = d.At
		f.profiles[i].SummaryNextRetryAt = d.NextRetryAt
	}
	return nil
}

func (f *fakeSummaryStore) RevokeInvalidSummaries(context.Context) ([]string, error) {
	for _, uid := range f.revoked {
		for i := range f.profiles {
			if f.profiles[i].UID == uid {
				f.profiles[i].ProfileSummary = ""
				f.profiles[i].SummaryUpdatedAt = time.Time{}
			}
		}
		f.enqueue(store.ViewerSummaryJob{UID: uid, Action: store.ViewerSummaryActionPurge})
	}
	return append([]string(nil), f.revoked...), nil
}

func (f *fakeSummaryStore) PendingSummaryJobs(context.Context, time.Time, int) ([]store.ViewerSummaryJob, error) {
	return append([]store.ViewerSummaryJob(nil), f.jobs...), nil
}

func (f *fakeSummaryStore) FinishSummaryJob(_ context.Context, uid, action string) error {
	f.finished = append(f.finished, uid+":"+action)
	f.removeJob(uid, action)
	return nil
}

func (f *fakeSummaryStore) FailSummaryJob(_ context.Context, uid, action string, attempts int, nextRetry time.Time) error {
	f.failed = append(f.failed, summaryJobFailure{uid: uid, action: action, attempts: attempts, next: nextRetry})
	for i := range f.jobs {
		if f.jobs[i].UID == uid && f.jobs[i].Action == action {
			f.jobs[i].Attempts = attempts
		}
	}
	return nil
}

func (f *fakeSummaryStore) enqueue(job store.ViewerSummaryJob) {
	for i := range f.jobs {
		if f.jobs[i].UID == job.UID {
			f.jobs[i] = job
			return
		}
	}
	f.jobs = append(f.jobs, job)
}

func (f *fakeSummaryStore) removeJob(uid, action string) {
	out := f.jobs[:0]
	for _, job := range f.jobs {
		if job.UID == uid && job.Action == action {
			continue
		}
		out = append(out, job)
	}
	f.jobs = out
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
	views   []SummaryView
	removed []string
	err     error
}

func (f *fakeKnowledge) UpsertViewer(_ context.Context, view SummaryView) error {
	if f.err != nil {
		return f.err
	}
	f.views = append(f.views, view)
	return nil
}

func (f *fakeKnowledge) RemoveViewer(_ context.Context, uid string) error {
	if f.err != nil {
		return f.err
	}
	f.removed = append(f.removed, uid)
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
	if stats.ProjectionFailed != 0 || stats.Retained != 0 || stats.SourceChanged != 0 {
		t.Fatalf("补偿统计不符：%+v", stats)
	}
	if len(st.saved) != 1 {
		t.Fatalf("应写一条侧写：%+v", st.saved)
	}
	saved := st.saved[0]
	if saved.UID != profile.UID || !saved.At.Equal(testNow().UTC()) {
		t.Fatalf("侧写时间或 UID 不符：%+v", saved)
	}
	for _, want := range []string{"性格：话痨", "偏好：猫猫", "口头禅：绷不住了", "关系：常被鱼回", "最近话题：白饭"} {
		if !strings.Contains(saved.Summary, want) {
			t.Fatalf("侧写缺少 %q：%q", want, saved.Summary)
		}
	}
	record := saved.Record
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
	// 来源版本必须与输入快照一致，生成器版本固定。
	if len(saved.Sources) != 3 || saved.Generator != store.ViewerSummaryGenerator {
		t.Fatalf("来源快照不符：%+v", saved)
	}
	wantHash := store.ViewerSummarySourceHash(profile.UID, store.ViewerSummaryGenerator, summarySources(testMessages(profile.UID)))
	if saved.SourceHash != wantHash || saved.Failures != 0 || !saved.NextRetryAt.IsZero() || saved.Fallback {
		t.Fatalf("来源指纹或补偿状态不符：%+v", saved)
	}
	if len(kb.views) != 1 {
		t.Fatalf("应写一条知识：%+v", kb.views)
	}
	view := kb.views[0]
	if view.UID != profile.UID || view.Summary != saved.Summary || !strings.Contains(view.Stats, "说过 12 次") {
		t.Fatalf("知识条目不符：%+v", view)
	}
	if view.SourceHash != wantHash || view.Generator != store.ViewerSummaryGenerator {
		t.Fatalf("知识条目缺少来源版本：%+v", view)
	}
	if len(view.Keywords) == 0 || view.Keywords[0] != profile.Name || !strings.Contains(strings.Join(view.Keywords, ","), "绷不住了") {
		t.Fatalf("知识关键词不符：%+v", view.Keywords)
	}
	// 保存时登记投影任务，知识一次成功后就地完成。
	if len(st.jobs) != 0 || len(st.finished) != 1 || st.finished[0] != profile.UID+":publish" {
		t.Fatalf("投影任务未完成：jobs=%+v finished=%+v", st.jobs, st.finished)
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
		name      string
		llm       SummaryLLM
		retryable bool
	}{
		{name: "模型报错", llm: &scriptedLLM{err: errors.New("模拟模型失败")}, retryable: true},
		{name: "JSON损坏", llm: &scriptedLLM{reply: "这不是JSON"}, retryable: true},
		{name: "字段全空", llm: &scriptedLLM{reply: `{"personality":"","preferences":"","catchphrases":"","relationship":"","recent_topics":""}`}, retryable: true},
		{name: "未配置模型", llm: nil, retryable: false},
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
			if stats.Fallback != 1 || stats.Summarized != 0 || stats.Failed != 0 || stats.Retained != 0 {
				t.Fatalf("降级统计不符：%+v", stats)
			}
			if len(st.saved) != 1 {
				t.Fatalf("降级仍应写档案：%+v", st.saved)
			}
			saved := st.saved[0]
			for _, want := range []string{"（模型未生成侧写，仅统计）", "说过 12 次", "最近说过：", "最新一句"} {
				if !strings.Contains(saved.Summary, want) {
					t.Fatalf("降级摘要缺少 %q：%q", want, saved.Summary)
				}
			}
			if saved.Record.Confidence != summaryFallbackConfidence || !saved.Fallback || saved.Failures != 1 {
				t.Fatalf("降级置信度或状态不符：%+v", saved)
			}
			if tt.retryable {
				want := testNow().Add(summaryRetryDelay(1))
				if !saved.NextRetryAt.Equal(want) {
					t.Fatalf("可恢复失败应安排补偿：got=%v want=%v", saved.NextRetryAt, want)
				}
			} else if !saved.NextRetryAt.IsZero() {
				t.Fatalf("未配置模型不应无限重试：%v", saved.NextRetryAt)
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

func TestSummarizerRetainsGoodVersionOnModelFailure(t *testing.T) {
	good := testProfile("good")
	good.ProfileSummary = "性格：安静；偏好：钓鱼"
	good.SummaryUpdatedAt = testNow().Add(-2 * time.Hour)
	good.SummaryLastSuccessAt = good.SummaryUpdatedAt
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{good}}
	es := &fakeEventSource{byUID: map[string][]event.Event{good.UID: testMessages(good.UID)}}
	llm := &scriptedLLM{err: errors.New("模型暂时不可用")}

	stats, err := newTestSummarizer(t, st, es, llm, &fakeKnowledge{}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.saved) != 0 || len(st.deferred) != 1 {
		t.Fatalf("旧版本不应被降级覆盖：saved=%+v deferred=%+v", st.saved, st.deferred)
	}
	deferred := st.deferred[0]
	if deferred.UID != good.UID || deferred.Failures != 1 || !deferred.NextRetryAt.Equal(testNow().Add(summaryRetryDelay(1))) {
		t.Fatalf("补偿状态不符：%+v", deferred)
	}
	profile, _ := st.GetProfile(context.Background(), good.UID)
	if profile.ProfileSummary != good.ProfileSummary || !profile.SummaryUpdatedAt.Equal(good.SummaryUpdatedAt) {
		t.Fatalf("旧好版本被改写：%+v", profile)
	}
	if stats.Retained != 1 || stats.Fallback != 0 || stats.Failed != 0 || stats.Knowledge != 0 {
		t.Fatalf("保留旧版本统计不符：%+v", stats)
	}
}

// TestSummarizerModelRecoveryReplacesFallback 首次降级写入后，重启一轮模型恢复：
// 好版本覆盖降级内容，失败计数与补偿时间清零。
func TestSummarizerModelRecoveryReplacesFallback(t *testing.T) {
	profile := testProfile("recover")
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{profile}}
	es := &fakeEventSource{byUID: map[string][]event.Event{profile.UID: testMessages(profile.UID)}}
	broken := &scriptedLLM{err: errors.New("模型暂时不可用")}
	if _, err := newTestSummarizer(t, st, es, broken, nil).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.saved) != 1 || !st.saved[0].Fallback || !st.saved[0].NextRetryAt.Equal(testNow().Add(summaryRetryDelay(1))) {
		t.Fatalf("首次失败应写降级并安排补偿：%+v", st.saved)
	}

	// 模拟进程重启：同一份库状态、新的侧写器与恢复的模型。
	recovered := &scriptedLLM{reply: `{"personality":"话痨","preferences":"猫猫"}`}
	if _, err := newTestSummarizer(t, st, es, recovered, nil).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.saved) != 2 {
		t.Fatalf("恢复后应再写一版：%+v", st.saved)
	}
	latest := st.saved[1]
	if latest.Fallback || latest.Failures != 0 || !latest.NextRetryAt.IsZero() {
		t.Fatalf("恢复后仍标记失败：%+v", latest)
	}
	if !strings.Contains(latest.Summary, "性格：话痨") {
		t.Fatalf("恢复后未写入模型侧写：%q", latest.Summary)
	}
	got, _ := st.GetProfile(context.Background(), profile.UID)
	if got.SummaryLastSuccessAt.IsZero() || got.SummaryFailures != 0 || !got.SummaryNextRetryAt.IsZero() {
		t.Fatalf("恢复后状态未清零：%+v", got)
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
	if stats.Candidates != 2 || stats.Summarized+stats.Fallback != 1 || stats.Failed != 1 || len(st.saved) != 1 || st.saved[0].UID != ok.UID {
		t.Fatalf("失败后未继续：stats=%+v saved=%+v", stats, st.saved)
	}
}

type failingStore struct {
	*fakeSummaryStore
	failUID string
}

func (f *failingStore) SaveProfileSummary(ctx context.Context, w store.ViewerSummaryWrite) error {
	if w.UID == f.failUID {
		return errors.New("模拟写库失败")
	}
	return f.fakeSummaryStore.SaveProfileSummary(ctx, w)
}

func TestSummarizerKnowledgeFailureKeepsProfileAndJob(t *testing.T) {
	profile := testProfile("9")
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{profile}}
	es := &fakeEventSource{byUID: map[string][]event.Event{profile.UID: testMessages(profile.UID)}}
	kb := &fakeKnowledge{err: errors.New("模拟知识写入失败")}
	stats, err := newTestSummarizer(t, st, es, nil, kb).RunOnce(context.Background())
	// 知识失败不算侧写失败：不重复计 Failed，也不回滚档案。
	if err != nil {
		t.Fatalf("知识失败不应让整轮失败：%v", err)
	}
	if len(st.saved) != 1 {
		t.Fatalf("知识失败不应回滚档案侧写：%+v", st.saved)
	}
	if stats.Fallback != 1 || stats.Failed != 0 || stats.Knowledge != 0 || stats.ProjectionFailed != 1 {
		t.Fatalf("知识失败统计不符：%+v", stats)
	}
	if len(st.jobs) != 1 || st.jobs[0].Action != store.ViewerSummaryActionPublish {
		t.Fatalf("知识失败应保留投影任务：%+v", st.jobs)
	}
	if len(st.finished) != 0 {
		t.Fatalf("投影失败不应完成任务：%+v", st.finished)
	}
}

func TestSummarizerProjectionRecoveryWithoutLLMCall(t *testing.T) {
	profile := testProfile("10")
	profile.ProfileSummary = "性格：话痨；偏好：白饭"
	profile.SummaryUpdatedAt = testNow().Add(-time.Hour)
	profile.SummarySourceHash = "abcdef"
	profile.SummaryGenerator = store.ViewerSummaryGenerator
	profile.SummaryKeywords = []string{profile.Name, "绷不住了"}
	st := &fakeSummaryStore{
		profiles: []store.ViewerProfile{profile},
		jobs:     []store.ViewerSummaryJob{{UID: profile.UID, Action: store.ViewerSummaryActionPublish}},
	}
	es := &fakeEventSource{byUID: map[string][]event.Event{}}
	llm := &scriptedLLM{reply: "不应被调用"}
	kb := &fakeKnowledge{}

	stats, err := newTestSummarizer(t, st, es, llm, kb).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.users) != 0 {
		t.Fatalf("补投影不应调用模型：%+v", llm.users)
	}
	if len(kb.views) != 1 || kb.views[0].Summary != profile.ProfileSummary ||
		kb.views[0].SourceHash != profile.SummarySourceHash || kb.views[0].Generator != profile.SummaryGenerator {
		t.Fatalf("补投影内容不符：%+v", kb.views)
	}
	if stats.Projections != 1 || stats.Knowledge != 1 || stats.ProjectionFailed != 0 || stats.PendingJobs != 1 {
		t.Fatalf("补投影统计不符：%+v", stats)
	}
	if len(st.jobs) != 0 || len(st.finished) != 1 {
		t.Fatalf("补投影后任务未完成：jobs=%+v finished=%+v", st.jobs, st.finished)
	}
}

func TestSummarizerProjectionFailureBacksOff(t *testing.T) {
	profile := testProfile("11")
	profile.ProfileSummary = "性格：话痨"
	profile.SummaryUpdatedAt = testNow().Add(-time.Hour)
	st := &fakeSummaryStore{
		profiles: []store.ViewerProfile{profile},
		jobs:     []store.ViewerSummaryJob{{UID: profile.UID, Action: store.ViewerSummaryActionPublish, Attempts: 2}},
	}
	kb := &fakeKnowledge{err: errors.New("文件只读")}
	stats, err := newTestSummarizer(t, &fakeSummaryStoreView{fakeSummaryStore: st, view: profile}, &fakeEventSource{}, nil, kb).RunOnce(context.Background())
	if err == nil {
		t.Fatal("投影失败应上报以便日志可见")
	}
	if stats.ProjectionFailed != 1 || stats.Failed != 0 || len(st.failed) != 1 {
		t.Fatalf("投影失败统计不符：stats=%+v failed=%+v", stats, st.failed)
	}
	if st.failed[0].attempts != 3 || st.failed[0].next.Before(testNow()) {
		t.Fatalf("投影补偿退避不符：%+v", st.failed[0])
	}
	if len(st.jobs) != 1 || st.jobs[0].Attempts != 3 {
		t.Fatalf("失败任务应保留：%+v", st.jobs)
	}
}

// fakeSummaryStoreView 让候选列表为空，只验证投影路径。
type fakeSummaryStoreView struct {
	*fakeSummaryStore
	view store.ViewerProfile
}

func (f *fakeSummaryStoreView) ActiveViewers(context.Context, time.Time, time.Time, int, int) ([]store.ViewerProfile, error) {
	return nil, nil
}

func TestSummarizerPurgeJob(t *testing.T) {
	st := &fakeSummaryStore{
		jobs: []store.ViewerSummaryJob{{UID: "12", Action: store.ViewerSummaryActionPurge}},
	}
	kb := &fakeKnowledge{}
	stats, err := newTestSummarizer(t, &fakeSummaryStoreView{fakeSummaryStore: st}, &fakeEventSource{}, nil, kb).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(kb.removed) != 1 || kb.removed[0] != "12" || stats.Purged != 1 {
		t.Fatalf("purge 任务未执行：removed=%+v stats=%+v", kb.removed, stats)
	}
	if len(st.jobs) != 0 || len(st.finished) != 1 || st.finished[0] != "12:purge" {
		t.Fatalf("purge 任务未完成：jobs=%+v finished=%+v", st.jobs, st.finished)
	}
}

func TestSummarizerSweepRevokedThenPurge(t *testing.T) {
	profile := testProfile("13")
	profile.ProfileSummary = "旧侧写"
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{profile}, revoked: []string{profile.UID}}
	kb := &fakeKnowledge{}
	stats, err := newTestSummarizer(t, &fakeSummaryStoreView{fakeSummaryStore: st}, &fakeEventSource{}, nil, kb).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Revoked != 1 || stats.Purged != 1 || len(kb.removed) != 1 {
		t.Fatalf("撤回后未清理知识：stats=%+v removed=%+v", stats, kb.removed)
	}
	got, _ := st.GetProfile(context.Background(), profile.UID)
	if got.ProfileSummary != "" {
		t.Fatalf("撤回后档案侧写仍可见：%+v", got)
	}
}

func TestSummarizerSourceChangedSkipsWithoutRetry(t *testing.T) {
	profile := testProfile("14")
	st := &fakeSummaryStore{profiles: []store.ViewerProfile{profile}, saveErr: store.ErrViewerSourceChanged}
	es := &fakeEventSource{byUID: map[string][]event.Event{profile.UID: testMessages(profile.UID)}}
	kb := &fakeKnowledge{}
	stats, err := newTestSummarizer(t, st, es, nil, kb).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("来源变化应作为已处理跳过：%v", err)
	}
	if stats.SourceChanged != 1 || stats.Failed != 0 || stats.Fallback != 0 || len(st.saved) != 0 || len(kb.views) != 0 {
		t.Fatalf("来源变化统计不符：stats=%+v saved=%+v views=%+v", stats, st.saved, kb.views)
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

func TestSummaryRetryDelay(t *testing.T) {
	if got := summaryRetryDelay(1); got != 10*time.Minute {
		t.Fatalf("首次退避 = %v", got)
	}
	if got := summaryRetryDelay(4); got != 80*time.Minute {
		t.Fatalf("第四次退避 = %v", got)
	}
	if got := summaryRetryDelay(7); got != summaryRetryMax {
		t.Fatalf("退避应有上限：%v", got)
	}
	if got := summaryRetryDelay(100); got != summaryRetryMax {
		t.Fatalf("超大失败数应封顶：%v", got)
	}
	if next := summaryNextRetry(testNow(), summaryMaxFailures); !next.IsZero() {
		t.Fatalf("达到尝试上限应停止自动重试：%v", next)
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
