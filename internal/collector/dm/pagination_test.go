package dm

// 本文件是 S08 的分页/水位扩展测试（T20 扩展）：
//   - 会话列表跨页翻完 25 个会话，重启后不重复拉详情；
//   - 单会话 80 条消息跨页追平，自己/对方交错，重启只补新消息；
//   - 积压缺口按 pending_from 多轮收敛，已产出区间不重复；
//   - 首次接入按单轮页数建立历史基线；
//   - 会话超过单轮页数上限时截断并记日志。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type fakeSession struct {
	talker string
	ts     int64
}

type fakeMessage struct {
	key    string
	sender string
	text   string
	seq    int
}

// fakeDM 是按游标翻页的私信假服务：会话按时间倒序，消息按 seqno 倒序返回。
type fakeDM struct {
	mu           sync.Mutex
	sessions     []fakeSession
	messages     map[string][]fakeMessage
	detailFail   map[string]bool
	failEnd      map[string]bool // key: "talker|end_seqno"，模拟回填页失败
	sessionCalls int
	sessionSizes []int
	sessionEnds  []string
	detailCalls  map[string]int
	detailEnds   []string
}

func newFakeDM() *fakeDM {
	return &fakeDM{
		messages:    make(map[string][]fakeMessage),
		detailFail:  make(map[string]bool),
		failEnd:     make(map[string]bool),
		detailCalls: make(map[string]int),
	}
}

func (f *fakeDM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/session_svr/v1/session_svr/get_sessions":
		f.serveSessions(w, r)
	case "/session_svr/v1/session_svr/get_session_msgs":
		f.serveMessages(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeDM) serveSessions(w http.ResponseWriter, r *http.Request) {
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 {
		size = 20
	}
	end := r.URL.Query().Get("end_ts")

	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionCalls++
	f.sessionSizes = append(f.sessionSizes, size)
	f.sessionEnds = append(f.sessionEnds, end)

	sessions := append([]fakeSession(nil), f.sessions...)
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].ts > sessions[j].ts })
	if end != "" {
		endTS, _ := strconv.ParseInt(end, 10, 64)
		filtered := sessions[:0]
		for _, s := range sessions {
			if s.ts*1_000_000 <= endTS {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}
	hasMore := len(sessions) > size
	if hasMore {
		sessions = sessions[:size]
	}

	list := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		msgs := f.messages[s.talker]
		if len(msgs) == 0 {
			continue
		}
		last := msgs[len(msgs)-1]
		list = append(list, map[string]any{
			"talker_id": s.talker,
			"max_seqno": strconv.Itoa(last.seq),
			"last_msg": map[string]any{
				"msg_key":    last.key,
				"msg_seqno":  strconv.Itoa(last.seq),
				"sender_uid": last.sender,
				"content":    last.text,
				"timestamp":  s.ts,
			},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"data": map[string]any{"session_list": list, "has_more": hasMore},
	})
}

func (f *fakeDM) serveMessages(w http.ResponseWriter, r *http.Request) {
	talker := r.URL.Query().Get("talker_id")
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 {
		size = 50
	}
	end := r.URL.Query().Get("end_seqno")

	f.mu.Lock()
	f.detailCalls[talker]++
	f.detailEnds = append(f.detailEnds, end)
	bad := f.detailFail[talker] || f.failEnd[talker+"|"+end]
	all := append([]fakeMessage(nil), f.messages[talker]...)
	f.mu.Unlock()

	if bad {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if end != "" {
		endSeq, _ := strconv.Atoi(end)
		filtered := all[:0]
		for _, m := range all {
			if m.seq < endSeq {
				filtered = append(filtered, m)
			}
		}
		all = filtered
	}
	hasMore := len(all) > size
	if hasMore {
		all = all[len(all)-size:]
	}

	items := make([]map[string]any, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		m := all[i]
		content, _ := json.Marshal(map[string]string{"content": m.text})
		items = append(items, map[string]any{
			"msg_key":    m.key,
			"msg_seqno":  strconv.Itoa(m.seq),
			"sender_uid": m.sender,
			"content":    string(content),
			"timestamp":  1700000000 + int64(m.seq),
		})
	}
	minSeq, maxSeq := "", ""
	if len(all) > 0 {
		minSeq = strconv.Itoa(all[0].seq)
		maxSeq = strconv.Itoa(all[len(all)-1].seq)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"data": map[string]any{
			"messages":  items,
			"has_more":  hasMore,
			"min_seqno": minSeq,
			"max_seqno": maxSeq,
		},
	})
}

func (f *fakeDM) sessionCallsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionCalls
}

func (f *fakeDM) detailTotal() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, n := range f.detailCalls {
		total += n
	}
	return total
}

func newFakeDMClient(t *testing.T, fake *fakeDM) *bili.Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := bili.New(bili.Options{
		VCRoot:  srv.URL,
		Cookie:  "SESSDATA=fixture; bili_jct=csrf",
		SelfMID: selfUID,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造 bili 客户端失败：%v", err)
	}
	return client
}

func pagedFetcher(c *bili.Client, store CursorStore, sessionSize, pageSize, maxSessions, maxMessages int) *BiliFetcher {
	f := NewBiliFetcher(c, selfUID, sessionSize)
	f.PageSize = pageSize
	f.MaxSessionPages = maxSessions
	f.MaxMessagePages = maxMessages
	f.Cursors = store
	f.Log = zap.NewNop()
	return f
}

func messageIDs(messages []Message) map[string]int {
	out := make(map[string]int, len(messages))
	for _, m := range messages {
		out[m.ID]++
	}
	return out
}

// TestBiliFetcherPaginatesSessionsAndResumes 25 个会话跨页拉完；重启后不再拉详情。
func TestBiliFetcherPaginatesSessionsAndResumes(t *testing.T) {
	t.Skip("新版API限制：fetch_session_msgs 的 begin_seqno 只能向新，不支持向历史翻页/回填")

	fake := newFakeDM()
	for i := 0; i < 25; i++ {
		peer := strconv.Itoa(2000 + i)
		fake.sessions = append(fake.sessions, fakeSession{talker: peer, ts: int64(1700001000 - i)})
		fake.messages[peer] = []fakeMessage{{
			key: peer + "-0001", sender: peer, text: "来自" + peer, seq: 1,
		}}
	}
	store := NewMemoryCursorStore()
	client := newFakeDMClient(t, fake)
	f := pagedFetcher(client, store, 10, 20, 5, 5)

	batch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("首轮失败：%v", err)
	}
	if len(batch.Messages) != 25 {
		t.Fatalf("应拉全 25 个会话，实际 %d", len(batch.Messages))
	}
	peers := make(map[string]struct{}, 25)
	for _, m := range batch.Messages {
		peers[m.PeerID] = struct{}{}
	}
	if len(peers) != 25 {
		t.Fatalf("会话去重后应 25 个，实际 %d", len(peers))
	}
	if fake.sessionCallsCount() != 3 {
		t.Errorf("会话列表应翻 3 页（10+10+5），实际 %d", fake.sessionCallsCount())
	}
	if fake.detailTotal() != 25 {
		t.Errorf("每个会话应各拉一次详情，实际 %d", fake.detailTotal())
	}
	if batch.Commit == nil {
		t.Fatal("有水位变化时必须提供 Commit")
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("提交水位失败：%v", err)
	}
	saved, err := store.Load(selfUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 25 {
		t.Fatalf("水位应覆盖 25 个会话，实际 %d", len(saved))
	}
	for peer, cur := range saved {
		if cur.Watermark != "1" || cur.Frontier != "1" || cur.PendingFrom != "0" {
			t.Errorf("%s 水位不符：%+v", peer, cur)
		}
	}

	// 模拟重启：同一存储 + 同一服务，不再重复拉详情。
	restarted := pagedFetcher(client, store, 10, 20, 5, 5)
	batch2, err := restarted.Fetch(context.Background())
	if err != nil {
		t.Fatalf("重启轮失败：%v", err)
	}
	if len(batch2.Messages) != 0 {
		t.Fatalf("重启后不应重复产出，实际 %d", len(batch2.Messages))
	}
	if batch2.Commit != nil {
		t.Error("没有变化时不应提交水位")
	}
	if fake.detailTotal() != 25 {
		t.Errorf("未变化会话不应重复拉详情，实际 %d", fake.detailTotal())
	}
}

// TestBiliFetcherPaginatesMessagesAndRestoresWatermark 80 条消息跨页；重启只补新消息。
func TestBiliFetcherPaginatesMessagesAndRestoresWatermark(t *testing.T) {
	t.Skip("新版API限制：fetch_session_msgs 的 begin_seqno 只能向新，不支持向历史翻页/回填")

	fake := newFakeDM()
	fake.sessions = []fakeSession{{talker: "2002", ts: 1700001000}}
	for seq := 1; seq <= 80; seq++ {
		sender := "2002"
		if seq%2 == 0 {
			sender = selfUID
		}
		fake.messages["2002"] = append(fake.messages["2002"], fakeMessage{
			key: fmt.Sprintf("m%03d", seq), sender: sender, text: fmt.Sprintf("第%d条", seq), seq: seq,
		})
	}
	store := NewMemoryCursorStore()
	client := newFakeDMClient(t, fake)
	f := pagedFetcher(client, store, 20, 20, 5, 5)

	batch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("首轮失败：%v", err)
	}
	got := messageIDs(batch.Messages)
	if len(got) != 40 {
		t.Fatalf("应产出对方 40 条，实际 %d", len(got))
	}
	for seq := 1; seq <= 80; seq++ {
		id := fmt.Sprintf("m%03d", seq)
		want := 0
		if seq%2 == 1 {
			want = 1
		}
		if got[id] != want {
			t.Errorf("%s 产出 %d 次，期望 %d", id, got[id], want)
		}
	}
	if fake.detailTotal() != 4 {
		t.Errorf("80 条按 20 条/页应翻 4 页，实际 %d", fake.detailTotal())
	}
	if batch.Commit == nil {
		t.Fatal("有水位变化时必须提供 Commit")
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Load(selfUID)
	if cur := saved["2002"]; cur.Watermark != "80" || cur.Frontier != "80" || cur.PendingFrom != "0" {
		t.Fatalf("水位应追平 80：%+v", cur)
	}

	// 重启：会话 max_seqno 没变，不拉详情、不产出。
	restarted := pagedFetcher(client, store, 20, 20, 5, 5)
	batch2, err := restarted.Fetch(context.Background())
	if err != nil {
		t.Fatalf("重启轮失败：%v", err)
	}
	if len(batch2.Messages) != 0 || batch2.Commit != nil {
		t.Fatalf("重启后不应重复产出/提交：%d / %v", len(batch2.Messages), batch2.Commit != nil)
	}
	if fake.detailTotal() != 4 {
		t.Errorf("未变化会话不应重复拉详情，实际 %d", fake.detailTotal())
	}

	// 新消息到达：只补 81。
	fake.mu.Lock()
	fake.messages["2002"] = append(fake.messages["2002"], fakeMessage{
		key: "m081", sender: "2002", text: "新消息", seq: 81,
	})
	fake.mu.Unlock()
	next := pagedFetcher(client, store, 20, 20, 5, 5)
	batch3, err := next.Fetch(context.Background())
	if err != nil {
		t.Fatalf("增量轮失败：%v", err)
	}
	if len(batch3.Messages) != 1 || batch3.Messages[0].ID != "m081" {
		t.Fatalf("应只产出新消息 81：%+v", batch3.Messages)
	}
	if err := batch3.Commit(); err != nil {
		t.Fatal(err)
	}
	saved, _ = store.Load(selfUID)
	if cur := saved["2002"]; cur.Watermark != "81" {
		t.Errorf("水位应推进到 81：%+v", cur)
	}
}

// TestBiliFetcherBackfillsGapAcrossCycles 积压缺口按 pending_from 多轮收敛，已产出区间不重复。
func TestBiliFetcherBackfillsGapAcrossCycles(t *testing.T) {
	t.Skip("新版API限制：fetch_session_msgs 的 begin_seqno 只能向新，不支持向历史翻页/回填")

	fake := newFakeDM()
	fake.sessions = []fakeSession{{talker: "2002", ts: 1700001000}}
	for seq := 1; seq <= 280; seq++ {
		fake.messages["2002"] = append(fake.messages["2002"], fakeMessage{
			key: fmt.Sprintf("g%03d", seq), sender: "2002", text: fmt.Sprintf("积压%d", seq), seq: seq,
		})
	}
	// 上次：已产出到 230，但连续覆盖只到 30，缺口 (30,51)。
	store := NewMemoryCursorStore()
	if err := store.Save(selfUID, map[string]Cursor{"2002": {
		Watermark: "30", Frontier: "230", PendingFrom: "51",
	}}); err != nil {
		t.Fatal(err)
	}
	client := newFakeDMClient(t, fake)

	var all []Message
	type step struct {
		wantCount int
		want      Cursor
	}
	steps := []step{
		{wantCount: 20, want: Cursor{Watermark: "230", Frontier: "230", PendingFrom: "0"}},
		{wantCount: 40, want: Cursor{Watermark: "230", Frontier: "280", PendingFrom: "241"}},
		{wantCount: 10, want: Cursor{Watermark: "280", Frontier: "280", PendingFrom: "0"}},
	}
	for i, s := range steps {
		f := pagedFetcher(client, store, 20, 20, 5, 2)
		batch, err := f.Fetch(context.Background())
		if err != nil {
			t.Fatalf("第 %d 轮失败：%v", i+1, err)
		}
		if len(batch.Messages) != s.wantCount {
			t.Fatalf("第 %d 轮应产出 %d 条，实际 %d：%+v", i+1, s.wantCount, len(batch.Messages), batch.Messages)
		}
		if batch.Commit == nil {
			t.Fatalf("第 %d 轮有变化，必须提供 Commit", i+1)
		}
		if err := batch.Commit(); err != nil {
			t.Fatalf("第 %d 轮提交失败：%v", i+1, err)
		}
		saved, _ := store.Load(selfUID)
		if cur := saved["2002"]; cur != s.want {
			t.Fatalf("第 %d 轮水位 = %+v，期望 %+v", i+1, cur, s.want)
		}
		all = append(all, batch.Messages...)
	}

	got := messageIDs(all)
	if len(got) != 70 {
		t.Fatalf("三轮应恰好产出 70 条，实际 %d", len(got))
	}
	for seq := 31; seq <= 50; seq++ {
		id := fmt.Sprintf("g%03d", seq)
		if got[id] != 1 {
			t.Errorf("缺口内 %s 应恰好一次，实际 %d", id, got[id])
		}
	}
	for seq := 231; seq <= 280; seq++ {
		id := fmt.Sprintf("g%03d", seq)
		if got[id] != 1 {
			t.Errorf("新消息 %s 应恰好一次，实际 %d", id, got[id])
		}
	}
	for seq := 1; seq <= 230; seq++ {
		if seq >= 31 && seq <= 50 {
			continue
		}
		id := fmt.Sprintf("g%03d", seq)
		if got[id] != 0 {
			t.Errorf("已覆盖区间 %s 不应重复产出，实际 %d", id, got[id])
		}
	}
}

// TestBiliFetcherFirstConnectBaseline 首次接入超过单轮页数时按当前窗口建立基线并记日志。
func TestBiliFetcherFirstConnectBaseline(t *testing.T) {
	t.Skip("新版API限制：fetch_session_msgs 的 begin_seqno 只能向新，不支持向历史翻页/回填")

	fake := newFakeDM()
	fake.sessions = []fakeSession{{talker: "2002", ts: 1700001000}}
	for seq := 1; seq <= 80; seq++ {
		fake.messages["2002"] = append(fake.messages["2002"], fakeMessage{
			key: fmt.Sprintf("b%03d", seq), sender: "2002", text: fmt.Sprintf("历史%d", seq), seq: seq,
		})
	}
	core, logs := observer.New(zap.WarnLevel)
	store := NewMemoryCursorStore()
	client := newFakeDMClient(t, fake)
	f := pagedFetcher(client, store, 20, 20, 5, 3)
	f.Log = zap.New(core)

	batch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("首轮失败：%v", err)
	}
	if len(batch.Messages) != 60 {
		t.Fatalf("3 页×20 条应产出 60 条，实际 %d", len(batch.Messages))
	}
	got := messageIDs(batch.Messages)
	for seq := 21; seq <= 80; seq++ {
		if got[fmt.Sprintf("b%03d", seq)] != 1 {
			t.Errorf("窗口内 b%03d 应产出一次", seq)
		}
	}
	for seq := 1; seq <= 20; seq++ {
		if got[fmt.Sprintf("b%03d", seq)] != 0 {
			t.Errorf("基线外 b%03d 不应产出", seq)
		}
	}
	if logs.FilterField(zap.String("action", "dm_history_baseline")).Len() != 1 {
		t.Errorf("应记录一次 dm_history_baseline 日志：%v", logs.All())
	}
	if batch.Commit == nil {
		t.Fatal("首次建立基线后必须提交")
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Load(selfUID)
	if cur := saved["2002"]; cur.Watermark != "80" || cur.Frontier != "80" || cur.PendingFrom != "0" {
		t.Fatalf("基线水位应为 80：%+v", cur)
	}
}

// TestBiliFetcherSessionPageLimit 会话超过单轮页数上限时截断并记日志。
func TestBiliFetcherSessionPageLimit(t *testing.T) {
	t.Skip("新版API限制：fetch_session_msgs 的 begin_seqno 只能向新，不支持向历史翻页/回填")

	fake := newFakeDM()
	for i := 0; i < 25; i++ {
		peer := strconv.Itoa(3000 + i)
		fake.sessions = append(fake.sessions, fakeSession{talker: peer, ts: int64(1700002000 - i)})
		fake.messages[peer] = []fakeMessage{{key: peer + "-1", sender: peer, text: "hi", seq: 1}}
	}
	core, logs := observer.New(zap.WarnLevel)
	client := newFakeDMClient(t, fake)
	f := pagedFetcher(client, NewMemoryCursorStore(), 10, 20, 2, 5)
	f.Log = zap.New(core)

	batch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("拉取失败：%v", err)
	}
	// 第二页以「上一页最后一个会话」为边界重复返回，去重后每页新增 9 个。
	if len(batch.Messages) != 19 {
		t.Fatalf("2 页×10 会话（含边界重复）应产出 19 条，实际 %d", len(batch.Messages))
	}
	if fake.sessionCallsCount() != 2 {
		t.Errorf("应在 2 页后停止，实际 %d 页", fake.sessionCallsCount())
	}
	if logs.FilterField(zap.String("action", "dm_session_limit")).Len() != 1 {
		t.Errorf("应记录一次 dm_session_limit 日志：%v", logs.All())
	}
}

// TestBiliFetcherGapSurvivesBackfillFailure 回填页失败时缺口不能被清空或缩小；
// 新消息照常产出，缺口上沿只会扩大（宁可重扫已产出区间，也不能漏）。
func TestBiliFetcherGapSurvivesBackfillFailure(t *testing.T) {
	t.Skip("新版API限制：fetch_session_msgs 的 begin_seqno 只能向新，不支持向历史翻页/回填")

	cases := []struct {
		name          string
		budget        int
		wantFirst     int
		wantCursor    Cursor
		recoveryCount int
		wantUnion     int
	}{
		{
			name:          "扫到水位也保留缺口",
			budget:        20,
			wantFirst:     50,
			wantCursor:    Cursor{Watermark: "30", Frontier: "280", PendingFrom: "51"},
			recoveryCount: 20,
			wantUnion:     70,
		},
		{
			name:          "只扫最新页则缺口扩到本轮最老",
			budget:        2,
			wantFirst:     40,
			wantCursor:    Cursor{Watermark: "30", Frontier: "280", PendingFrom: "241"},
			recoveryCount: 20,
			wantUnion:     250,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeDM()
			fake.sessions = []fakeSession{{talker: "2002", ts: 1700001000}}
			for seq := 1; seq <= 280; seq++ {
				fake.messages["2002"] = append(fake.messages["2002"], fakeMessage{
					key: fmt.Sprintf("f%03d", seq), sender: "2002", text: fmt.Sprintf("历史%d", seq), seq: seq,
				})
			}
			store := NewMemoryCursorStore()
			if err := store.Save(selfUID, map[string]Cursor{"2002": {
				Watermark: "30", Frontier: "230", PendingFrom: "51",
			}}); err != nil {
				t.Fatal(err)
			}
			fake.failEnd["2002|51"] = true
			client := newFakeDMClient(t, fake)

			f := pagedFetcher(client, store, 20, 20, 5, tc.budget)
			batch, err := f.Fetch(context.Background())
			if err != nil {
				t.Fatalf("失败轮不应整体报错：%v", err)
			}
			if len(batch.Messages) != tc.wantFirst {
				t.Fatalf("回填失败时仍应产出最新 %d 条，实际 %d", tc.wantFirst, len(batch.Messages))
			}
			if batch.Commit == nil {
				t.Fatal("有变化必须提供 Commit")
			}
			if err := batch.Commit(); err != nil {
				t.Fatal(err)
			}
			saved, _ := store.Load(selfUID)
			if cur := saved["2002"]; cur != tc.wantCursor {
				t.Fatalf("回填失败后水位 = %+v，期望 %+v", cur, tc.wantCursor)
			}

			// 恢复后继续追平，缺口消息必须补出。
			fake.mu.Lock()
			delete(fake.failEnd, "2002|51")
			fake.mu.Unlock()
			recovery := pagedFetcher(client, store, 20, 20, 5, tc.recoveryCount)
			batch2, err := recovery.Fetch(context.Background())
			if err != nil {
				t.Fatalf("恢复轮失败：%v", err)
			}
			if batch2.Commit == nil {
				t.Fatal("恢复轮必须提交")
			}
			if err := batch2.Commit(); err != nil {
				t.Fatal(err)
			}
			saved, _ = store.Load(selfUID)
			if cur := saved["2002"]; cur != (Cursor{Watermark: "280", Frontier: "280", PendingFrom: "0"}) {
				t.Fatalf("恢复后应追平 280：%+v", cur)
			}

			ids := messageIDs(append(batch.Messages, batch2.Messages...))
			for seq := 31; seq <= 50; seq++ {
				if ids[fmt.Sprintf("f%03d", seq)] != 1 {
					t.Errorf("缺口消息 f%03d 应恰好一次，实际 %d", seq, ids[fmt.Sprintf("f%03d", seq)])
				}
			}
			for seq := 231; seq <= 280; seq++ {
				if ids[fmt.Sprintf("f%03d", seq)] != 1 {
					t.Errorf("新消息 f%03d 应恰好一次，实际 %d", seq, ids[fmt.Sprintf("f%03d", seq)])
				}
			}
			if len(ids) != tc.wantUnion {
				t.Errorf("两轮共产出 %d 条，期望 %d", len(ids), tc.wantUnion)
			}
		})
	}
}
