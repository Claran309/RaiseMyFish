package main

// 本文件是智能验收补齐（组3/6/7/12）的生产装配级验收：
// 用假 Provider 抓取实际送进模型的 prompt，覆盖并发身份隔离、问题主体分类、
// 纠正撤回与搜索状态文案。全程不开播、不连外部服务。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/brain/provider"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"RaiseMyFish/internal/webtools"
	"go.uber.org/zap"
)

// captureProvider 线程安全地保留每次实际请求，并按调用方给的规则生成回复。
type captureProvider struct {
	mu       sync.Mutex
	requests []provider.Request
	reply    func(provider.Request) string
	err      error
}

func (p *captureProvider) Name() string { return "capture" }

func (p *captureProvider) Chat(_ context.Context, r provider.Request) (provider.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, r)
	reply := p.reply
	p.mu.Unlock()
	if p.err != nil {
		return provider.Response{}, p.err
	}
	if reply == nil {
		return provider.Response{Text: `{"thought":"够了","action":"final","final":"好"}`}, nil
	}
	return provider.Response{Text: reply(r)}, nil
}

func (p *captureProvider) snapshot() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Request(nil), p.requests...)
}

func captureLLM(p *captureProvider) *providerLLM {
	return &providerLLM{
		blueprint: &reasoningModel{
			client: p, model: "capture-model", maxTokens: 128, temperature: 0,
			usage: &fakeUsageRepo{},
		},
		channel: event.ChLive,
		log:     zap.NewNop(),
	}
}

func captureReactRuntime(t *testing.T, p *captureProvider, box *reasoning.Toolbox) *reactRuntime {
	t.Helper()
	factory := func(memory.Scope, string, bool) (*reasoning.Agent, error) {
		return reasoning.New(captureLLM(p), box, reasoning.Options{MaxSteps: 3, Timeout: 2 * time.Second}, zap.NewNop())
	}
	return &reactRuntime{factory: factory, defaultReact: true}
}

// syncMemoryReader 是并发安全的记忆快照；组3会同时被两条事件读取。
type syncMemoryReader struct {
	mu     sync.Mutex
	ctx    memory.Context
	shared memory.SharedState
}

func (f *syncMemoryReader) Recall(context.Context, memory.Scope) (memory.Context, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctx, nil
}

func (f *syncMemoryReader) Shared(context.Context, memory.Scope) (memory.SharedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shared, nil
}

// mapViewerRepo 按 uid 返回不同档案，模拟「A/B 各有自己的资料」。
type mapViewerRepo struct {
	mu       sync.Mutex
	profiles map[string]store.ViewerProfile
	calls    []string
}

func (f *mapViewerRepo) UpsertProfile(context.Context, store.ViewerProfile) error { return nil }

func (f *mapViewerRepo) GetProfile(_ context.Context, uid string) (store.ViewerProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, uid)
	profile, ok := f.profiles[uid]
	if !ok {
		return store.ViewerProfile{}, store.ErrViewerNotFound
	}
	return profile, nil
}

func (f *mapViewerRepo) TopViewers(context.Context, int) ([]store.ViewerProfile, error) {
	return nil, nil
}

// syncSender 记录每条事件实际发出的正文（并发安全）。
type syncSender struct {
	mu     sync.Mutex
	texts  map[string]string
	claims map[string]event.Claim
}

func newSyncSender() *syncSender {
	return &syncSender{texts: map[string]string{}, claims: map[string]event.Claim{}}
}

func (s *syncSender) Name() string { return "sync" }

func (s *syncSender) Send(_ context.Context, c event.Claim, r sender.Reply) (sender.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts[c.Event.ID] = r.Text
	s.claims[c.Event.ID] = c
	return sender.Result{OK: true, Verified: true}, nil
}

func (s *syncSender) Resume(context.Context, event.Claim) (sender.Result, error) {
	return sender.Result{OK: true, Verified: true}, nil
}

func (s *syncSender) text(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.texts[id]
}

// fakeProfileSearcher 是 viewer_search 的假档案检索，记录被查的每个主体。
type fakeProfileSearcher struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeProfileSearcher) Find(_ context.Context, query string, _ int) (viewer.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, query)
	f.mu.Unlock()
	if strings.Contains(query, "贞德") {
		return viewer.Result{
			Status: viewer.StatusFound,
			Candidates: []viewer.Candidate{{
				ViewerProfileHit: store.ViewerProfileHit{Profile: store.ViewerProfile{
					UID: "7001", Name: "贞德", Hits: 12, Replies: 3,
					LastSeen:    time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC),
					RecentTexts: []string{"今天也要白饭"},
				}},
				Tier: viewer.TierName,
			}},
		}, nil
	}
	return viewer.Result{Status: viewer.StatusNotFound}, nil
}

func (f *fakeProfileSearcher) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// TestSmartConcurrentSameQuestionKeepsIdentities 组3：
// A/B 同时问「记得我吗」，两份 prompt 的当前身份与证据必须分别正确，答案不共享。
func TestSmartConcurrentSameQuestionKeepsIdentities(t *testing.T) {
	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	base := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	prov := &captureProvider{}
	prov.reply = func(r provider.Request) string {
		if len(r.Messages) < 2 {
			return "看不到输入"
		}
		user := r.Messages[1].Content
		switch {
		case strings.Contains(user, "阿A的私密档案"):
			return "记得，观众A，你爱吃鱼"
		case strings.Contains(user, "阿B的私密档案"):
			return "记得，观众B，你爱看球"
		default:
			return "不记得"
		}
	}
	engine, err := brain.New(prov, &fakeUsageRepo{}, func() brain.Options {
		o := brain.DefaultOptions()
		o.Model = "capture-model"
		o.Backoff = time.Millisecond
		return o
	}(), zap.NewNop())
	if err != nil {
		t.Fatalf("Brain 装配失败：%v", err)
	}

	mem := &syncMemoryReader{
		ctx: memory.Context{Scope: scope, Focus: []memory.Message{
			{EventID: "live:30308220:a1", Channel: event.ChLive, Session: scope.Session, Dir: event.DirIn, FromID: "1001", FromName: "观众A", Text: "我是观众A，我爱吃鱼", At: base},
			{EventID: "live:30308220:b1", Channel: event.ChLive, Session: scope.Session, Dir: event.DirIn, FromID: "2002", FromName: "观众B", Text: "我是观众B，我爱看球", At: base.Add(time.Minute)},
		}},
		shared: memory.SharedState{Scope: scope},
	}
	viewers := &mapViewerRepo{profiles: map[string]store.ViewerProfile{
		"1001": {UID: "1001", Name: "观众A", Hits: 5, Replies: 2, LastSeen: base, RecentTexts: []string{"阿A的私密档案：爱吃鱼"}},
		"2002": {UID: "2002", Name: "观众B", Hits: 8, Replies: 1, LastSeen: base, RecentTexts: []string{"阿B的私密档案：爱看球"}},
	}}
	snd := newSyncSender()
	workers, err := buildConsumeWorkers(
		scheduler.ChannelPolicy{}, snapshotTestPack(),
		map[event.Channel]brain.Brain{event.ChLive: engine},
		snd, newFakeBus("live:30308220:a", "live:30308220:b"), time.Second,
		mem, nil, viewers, "", nil, nil, nil, nil, zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}

	claims := map[string]event.Claim{}
	for _, spec := range []struct{ id, uid, name string }{
		{"live:30308220:a", "1001", "观众A"},
		{"live:30308220:b", "2002", "观众B"},
	} {
		claim := liveClaim(spec.id)
		claim.Event.FromID = spec.uid
		claim.Event.FromName = spec.name
		claim.Event.Text = "记得我吗"
		claim.Event.At = base.Add(2 * time.Minute)
		claims[spec.id] = claim
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for id, claim := range claims {
		wg.Add(1)
		go func(id string, claim event.Claim) {
			defer wg.Done()
			if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask(id)}); err != nil {
				errs <- err
			}
		}(id, claim)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Generate 失败：%v", err)
	}

	requests := prov.snapshot()
	if len(requests) != 2 {
		t.Fatalf("模型请求数 = %d，期望 2（每条事件各一份输入）", len(requests))
	}
	byEvent := map[string]string{}
	for _, r := range requests {
		user := r.Messages[1].Content
		switch {
		case strings.Contains(user, `"current_event":{"id":"live:30308220:a"`):
			byEvent["a"] = user
		case strings.Contains(user, `"current_event":{"id":"live:30308220:b"`):
			byEvent["b"] = user
		default:
			t.Fatalf("请求里没有可识别的当前事件：%s", user)
		}
	}
	if len(byEvent) != 2 {
		t.Fatalf("两条事件未各自生成输入：%d", len(byEvent))
	}
	for _, want := range []struct {
		key, speaker, ownMark, otherMark string
	}{
		{"a", `"speaker":{"id":"1001","name":"观众A"}`, "阿A的私密档案", "阿B的私密档案"},
		{"b", `"speaker":{"id":"2002","name":"观众B"}`, "阿B的私密档案", "阿A的私密档案"},
	} {
		user := byEvent[want.key]
		if !strings.Contains(user, want.speaker) {
			t.Errorf("事件 %s 的当前身份不对：%s", want.key, user)
		}
		if !strings.Contains(user, want.ownMark) || strings.Contains(user, want.otherMark) {
			t.Errorf("事件 %s 的档案证据串了：%s", want.key, user)
		}
	}
	if got := snd.text("live:30308220:a"); !strings.Contains(got, "观众A") || strings.Contains(got, "观众B") {
		t.Errorf("A 的回复串了：%q", got)
	}
	if got := snd.text("live:30308220:b"); !strings.Contains(got, "观众B") || strings.Contains(got, "观众A") {
		t.Errorf("B 的回复串了：%q", got)
	}
}

func smartWorkers(t *testing.T, prov *captureProvider, mem memory.Reader, viewers store.ViewerRepo, box *reasoning.Toolbox) (scheduler.Workers, *syncSender) {
	t.Helper()
	snd := newSyncSender()
	workers, err := buildConsumeWorkers(
		scheduler.ChannelPolicy{}, snapshotTestPack(),
		map[event.Channel]brain.Brain{event.ChLive: &fakeBrain{reply: brain.Reply{Text: "不该走单步"}}},
		snd, newFakeBus("live:30308220:smart"), time.Second,
		mem, nil, viewers, "", nil, nil, captureReactRuntime(t, prov, box), nil, zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	return workers, snd
}

func runSmartEvent(t *testing.T, workers scheduler.Workers, id, uid, name, text string, at time.Time) {
	t.Helper()
	claim := liveClaim(id)
	claim.Event.FromID = uid
	claim.Event.FromName = name
	claim.Event.Text = text
	claim.Event.At = at
	if _, err := workers.Generate(context.Background(), scheduler.Work{Claim: claim, Task: liveTask(id)}); err != nil {
		t.Fatalf("Generate 失败：%v", err)
	}
}

// TestSmartQuestionSubjectRoutesToDifferentEvidence 组6：
// 「贞德是谁」按房间候选查档案，「鲁迅是谁」不许一律查观众，改走记忆证据。
func TestSmartQuestionSubjectRoutesToDifferentEvidence(t *testing.T) {
	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	base := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	mem := &syncMemoryReader{
		ctx: memory.Context{Scope: scope, Focus: []memory.Message{
			{EventID: "live:30308220:j1", Channel: event.ChLive, Session: scope.Session, Dir: event.DirIn, FromID: "7001", FromName: "贞德", Text: "今天也要白饭", At: base},
		}},
		shared: memory.SharedState{Scope: scope},
	}
	cases := []struct {
		name       string
		id         string
		text       string
		wantKind   string
		wantAction string
		wantNoCall bool
	}{
		{"房间候选", "live:30308220:smart", "贞德是谁", `"kind":"room_candidate"`, "viewer_search", false},
		{"窗口外主体", "live:30308220:smart", "鲁迅是谁", `"kind":"unresolved"`, "memory_search", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			searcher := &fakeProfileSearcher{}
			prov := &captureProvider{}
			prov.reply = func(r provider.Request) string {
				user := r.Messages[1].Content
				if !strings.Contains(user, "已经查到") {
					if strings.Contains(user, `"kind":"room_candidate"`) {
						return `{"thought":"房间里有这个人，查档案","action":"viewer_search","args":"贞德"}`
					}
					return `{"thought":"不是房间观众，先查记忆","action":"memory_search","args":"鲁迅"}`
				}
				if strings.Contains(user, "贞德") && strings.Contains(user, "viewer_search(贞德)") {
					return `{"thought":"够了","action":"final","final":"贞德是直播间观众"}`
				}
				if strings.Contains(user, "memory_search(鲁迅)") {
					return `{"thought":"够了","action":"final","final":"记忆里没查到鲁迅"}`
				}
				return `{"thought":"没查到","action":"final","final":"不知道"}`
			}
			recallBox, err := reasoning.BuildTools(reasoning.ToolDeps{Memory: mem, Scope: scope, Now: func() time.Time { return base }})
			if err != nil {
				t.Fatal(err)
			}
			readonly, err := reasoning.BuildReadOnlyTools(reasoning.ReadOnlyDeps{ViewerSearch: searcher, Room: scope.Session, Now: func() time.Time { return base }})
			if err != nil {
				t.Fatal(err)
			}
			box, err := reasoning.MergeToolboxes(recallBox, readonly)
			if err != nil {
				t.Fatal(err)
			}
			workers, _ := smartWorkers(t, prov, mem, nil, box)
			runSmartEvent(t, workers, tc.id, "1001", "观众A", tc.text, base.Add(2*time.Minute))

			requests := prov.snapshot()
			if len(requests) != 2 {
				t.Fatalf("模型请求数 = %d，期望 2（首步选工具 + 拿到观察后回答）", len(requests))
			}
			first := requests[0].Messages[1].Content
			if !strings.Contains(first, tc.wantKind) {
				t.Fatalf("首步输入缺少主体分类 %s：%s", tc.wantKind, first)
			}
			system := requests[0].Messages[0].Content
			for _, want := range []string{"kind=room_candidate 才优先用 viewer_search", "kind=unresolved 不要假定是观众", "viewer_search 没查到只表示档案里没有"} {
				if !strings.Contains(system, want) {
					t.Errorf("system 缺少主体规则 %q", want)
				}
			}
			second := requests[1].Messages[1].Content
			if !strings.Contains(second, tc.wantAction) {
				t.Fatalf("观察里没有走预期工具 %s：%s", tc.wantAction, second)
			}
			if tc.wantNoCall {
				if strings.Contains(second, "viewer_search(鲁迅)") {
					t.Fatalf("窗口外主体不应强行查观众档案：%s", second)
				}
				if calls := searcher.called(); len(calls) != 0 {
					t.Fatalf("窗口外主体不应调用 viewer_search，实际 %v", calls)
				}
			} else if calls := searcher.called(); len(calls) != 1 || calls[0] != "贞德" {
				t.Fatalf("房间候选应查一次贞德，实际 %v", calls)
			}
		})
	}
}

// TestSmartCorrectionRetractsAndReanswers 组7：
// 纠正后重问，实际输入必须带纠正块与原始证据，并撤掉旧回复派生的摘要/事实。
func TestSmartCorrectionRetractsAndReanswers(t *testing.T) {
	scope := memory.Scope{Channel: event.ChLive, Session: "30308220"}
	base := time.Date(2026, 11, 10, 21, 0, 0, 0, time.UTC)
	mem := &syncMemoryReader{
		ctx: memory.Context{Scope: scope, Focus: []memory.Message{
			{EventID: "live:30308220:a1", Channel: event.ChLive, Session: scope.Session, Dir: event.DirIn, FromID: "1001", FromName: "A", Text: "刚才谁说要吃鱼", At: base},
			{EventID: "live:30308220:b1", Channel: event.ChLive, Session: scope.Session, Dir: event.DirIn, FromID: "2002", FromName: "B", Text: "我要吃鱼", At: base.Add(time.Minute)},
			{EventID: "live:30308220:r1", Channel: event.ChLive, Session: scope.Session, Dir: event.DirOut, Text: "你说要吃鱼", ReplyTo: "live:30308220:a1", At: base.Add(90 * time.Second)},
			{EventID: "live:30308220:a2", Channel: event.ChLive, Session: scope.Session, Dir: event.DirIn, FromID: "1001", FromName: "A", Text: "那句不是我说的，是B说的", At: base.Add(2 * time.Minute)},
		}, Records: []memory.Record{
			{ID: "rec-wrong", Channel: event.ChLive, Session: scope.Session, Summary: "A说想吃鱼", Importance: .9, Confidence: .8, EventIDs: []string{"live:30308220:r1"}},
			{ID: "rec-keep", Channel: event.ChLive, Session: scope.Session, Summary: "B在直播间聊过吃的", Importance: .5, Confidence: .8, EventIDs: []string{"live:30308220:b1"}},
		}},
		shared: memory.SharedState{Scope: scope, Facts: []memory.Fact{
			{Key: "wrong_owner", Value: "A想吃鱼", Channel: event.ChLive, Session: scope.Session, SourceID: "live:30308220:r1", At: base.Add(90 * time.Second)},
			{Key: "b_likes_fish", Value: "B想吃鱼", Channel: event.ChLive, Session: scope.Session, SourceID: "live:30308220:b1", At: base.Add(time.Minute)},
		}},
	}
	prov := &captureProvider{}
	prov.reply = func(r provider.Request) string {
		user := r.Messages[1].Content
		if strings.Contains(user, `"claimed":{"id":"2002","name":"B"}`) {
			return `{"thought":"先承认纠正","action":"final","final":"对，我记混了，那句是B说的"}`
		}
		return `{"thought":"没看到纠正","action":"final","final":"不知道"}`
	}
	box, err := reasoning.NewToolbox(reasoning.Tool{
		Name: reasoning.ToolHistory, Desc: "测试工具",
		Run: func(context.Context, string) (string, error) { return "弹幕一", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	workers, snd := smartWorkers(t, prov, mem, nil, box)
	runSmartEvent(t, workers, "live:30308220:smart", "1001", "A", "那到底谁说的", base.Add(3*time.Minute))

	requests := prov.snapshot()
	if len(requests) != 1 {
		t.Fatalf("模型请求数 = %d，期望 1", len(requests))
	}
	user := requests[0].Messages[1].Content
	for _, want := range []string{
		`"corrections":[{"event_id":"live:30308220:a2"`,
		`"denied":{"id":"1001","name":"A"}`,
		`"claimed":{"id":"2002","name":"B"}`,
		`"superseded_event_ids":["live:30308220:r1"]`,
		`"retracted_record_ids":["rec-wrong"]`,
		`"retracted_fact_keys":["wrong_owner"]`,
		`"original_evidence":[{"event_id":"live:30308220:b1"`,
		"我要吃鱼",
		"先承认纠正",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("实际输入缺少 %q：\n%s", want, user)
		}
	}
	for _, banned := range []string{`"Summary":"A说想吃鱼"`, `"Value":"A想吃鱼"`} {
		if strings.Contains(user, banned) {
			t.Errorf("被撤回内容仍出现在实际输入：%q", banned)
		}
	}
	for _, want := range []string{`"Summary":"B在直播间聊过吃的"`, `"Value":"B想吃鱼"`} {
		if !strings.Contains(user, want) {
			t.Errorf("未撤回内容丢失：%q", want)
		}
	}
	if system := requests[0].Messages[0].Content; !strings.Contains(system, "context 里有 corrections 时，先承认纠正") {
		t.Error("system 缺少纠正规则")
	}
	if got := snd.text("live:30308220:smart"); !strings.Contains(got, "对，我记混了") || !strings.Contains(got, "B") {
		t.Errorf("回复没有承认纠正并按正确归属：%q", got)
	}
}

// TestSmartSearchStatusReachesModelInput 组12：
// 未配置/失败/确无结果/成功四种状态必须原样进入模型输入，且失败不能伪装成查无事实。
func TestSmartSearchStatusReachesModelInput(t *testing.T) {
	base := time.Date(2026, 11, 10, 22, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		search func(t *testing.T) reasoning.WebSearcher
		want   []string
		banned []string
	}{
		{
			name: "未配置",
			search: func(t *testing.T) reasoning.WebSearcher {
				s, err := webtools.NewHTTP(webtools.Options{})
				if err != nil {
					t.Fatal(err)
				}
				return s
			},
			want:   []string{"搜索能力：未配置搜索源", "不要声称已联网"},
			banned: []string{"搜索状态：已完成（已联网）"},
		},
		{
			name: "确无结果",
			search: func(t *testing.T) reasoning.WebSearcher {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, `{"results":[]}`)
				}))
				t.Cleanup(srv.Close)
				s, err := webtools.NewHTTP(webtools.Options{URL: srv.URL})
				if err != nil {
					t.Fatal(err)
				}
				return s
			},
			want:   []string{"搜索状态：已完成（已联网），但没有搜到相关结果", "不代表事实不存在"},
			banned: []string{"搜索状态：失败"},
		},
		{
			name: "调用失败",
			search: func(t *testing.T) reasoning.WebSearcher {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				t.Cleanup(srv.Close)
				s, err := webtools.NewHTTP(webtools.Options{URL: srv.URL})
				if err != nil {
					t.Fatal(err)
				}
				return s
			},
			want:   []string{"工具失败", "搜索状态：失败", "不代表没有相关事实"},
			banned: []string{"没有搜到相关结果", "已完成（已联网）"},
		},
		{
			name: "成功",
			search: func(t *testing.T) reasoning.WebSearcher {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, `{"results":[{"title":"测试标题","snippet":"测试摘要","url":"https://example.com"}]}`)
				}))
				t.Cleanup(srv.Close)
				s, err := webtools.NewHTTP(webtools.Options{URL: srv.URL})
				if err != nil {
					t.Fatal(err)
				}
				return s
			},
			want:   []string{"搜索状态：已完成（已联网）", "测试标题"},
			banned: []string{"搜索状态：失败", "没有搜到相关结果"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prov := &captureProvider{}
			prov.reply = func(r provider.Request) string {
				user := r.Messages[1].Content
				if !strings.Contains(user, "已经查到") {
					return `{"thought":"需要联网查证","action":"web_search","args":"贞德是谁"}`
				}
				return `{"thought":"按工具状态回答","action":"final","final":"知道了"}`
			}
			box, err := reasoning.BuildReadOnlyTools(reasoning.ReadOnlyDeps{Search: tc.search(t), Room: "30308220", Now: func() time.Time { return base }})
			if err != nil {
				t.Fatal(err)
			}
			workers, _ := smartWorkers(t, prov, nil, nil, box)
			runSmartEvent(t, workers, "live:30308220:smart", "1001", "观众A", "贞德是谁", base)

			requests := prov.snapshot()
			if len(requests) != 2 {
				t.Fatalf("模型请求数 = %d，期望 2", len(requests))
			}
			system := requests[0].Messages[0].Content
			if !strings.Contains(system, "只有工具结果以「搜索状态：已完成（已联网）」开头时才能说联网查到了") {
				t.Error("system 缺少「未确认完成不得声称联网」规则")
			}
			second := requests[1].Messages[1].Content
			for _, want := range tc.want {
				if !strings.Contains(second, want) {
					t.Errorf("模型输入缺少 %q：\n%s", want, second)
				}
			}
			for _, banned := range tc.banned {
				if strings.Contains(second, banned) {
					t.Errorf("模型输入含不该出现的状态 %q：\n%s", banned, second)
				}
			}
		})
	}
}

// TestSmartSearchFailureIsNotEvidence 组12：搜索失败只进观察，不能当已验证证据回落。
func TestSmartSearchFailureIsNotEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	search, err := webtools.NewHTTP(webtools.Options{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	box, err := reasoning.BuildReadOnlyTools(reasoning.ReadOnlyDeps{Search: search, Room: "30308220"})
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedStepLLM{replies: []string{
		`{"thought":"查","action":"web_search","args":"贞德是谁"}`,
		`{"thought":"没查成","action":"final","final":"没查到"}`,
	}}
	agent, err := reasoning.New(llm, box, reasoning.Options{MaxSteps: 3, Timeout: 2 * time.Second}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), reasoning.Input{System: "人设", Question: "贞德是谁"})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if len(res.Evidence) != 0 {
		t.Fatalf("搜索失败不能作为证据：%+v", res.Evidence)
	}
	if len(llm.users) != 2 || !strings.Contains(llm.users[1], "搜索状态：失败") || !strings.Contains(llm.users[1], "不代表没有相关事实") {
		t.Fatalf("失败状态未进入观察：%+v", llm.users)
	}
}

// scriptedStepLLM 按顺序回放 JSON 步骤，并保留实际 user 输入。
type scriptedStepLLM struct {
	replies []string
	users   []string
}

func (s *scriptedStepLLM) Complete(_ context.Context, _, user string) (string, error) {
	s.users = append(s.users, user)
	if len(s.replies) == 0 {
		return "", fmt.Errorf("没有更多脚本回复")
	}
	next := s.replies[0]
	s.replies = s.replies[1:]
	return next, nil
}
