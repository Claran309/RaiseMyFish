package msgfeed

// 本文件用 httptest 模拟 msgfeed：测首次基线、增量水位、翻页与账号隔离。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type feedPage struct {
	items      string
	cursorID   string
	cursorTime int64
	isEnd      bool
	lastViewAt int64
}

func writeFeedPage(w http.ResponseWriter, page feedPage) {
	cursorID := page.cursorID
	if cursorID == "" {
		cursorID = "0"
	}
	payload := map[string]any{
		"code": 0,
		"data": map[string]any{
			"items":        json.RawMessage(page.items),
			"cursor":       map[string]any{"is_end": page.isEnd, "id": cursorID, "time": page.cursorTime},
			"last_view_at": page.lastViewAt,
		},
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func itemJSON(id string, time int64, business int, oid, rpid, root, user, text string) string {
	body := map[string]any{
		"id": id,
		"user": map[string]any{
			"mid": user, "nickname": "观众" + user,
		},
		"item": map[string]any{
			"type": "reply", "business_id": business, "subject_id": oid,
			"source_id": rpid, "root_id": root, "source_content": text,
		},
		"reply_time": time,
		"at_time":    time,
	}
	data, _ := json.Marshal(body)
	return string(data)
}

func newSource(t *testing.T, server *httptest.Server, store CursorStore) *BiliSource {
	t.Helper()
	client, err := bili.New(bili.Options{APIRoot: server.URL, Cookie: "SESSDATA=test", SelfMID: "8"})
	if err != nil {
		t.Fatal(err)
	}
	return &BiliSource{
		Client:   client,
		Cursors:  store,
		MaxPages: 5,
		Now:      func() time.Time { return time.Unix(300, 0) },
		Log:      zap.NewNop(),
	}
}

func TestBiliSourceFirstRunUsesLastViewAt(t *testing.T) {
	replyItems := "[" + itemJSON("11", 101, 1, "123", "777", "0", "42", "新回复") + "," +
		itemJSON("10", 99, 1, "123", "776", "0", "43", "旧回复") + "]"
	atItems := "[" + itemJSON("12", 102, 1, "123", "778", "0", "44", "@你") + "]"
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Path == msgfeedAtPathForTest {
			writeFeedPage(w, feedPage{items: atItems, cursorID: "12", cursorTime: 102, isEnd: true, lastViewAt: 100})
			return
		}
		writeFeedPage(w, feedPage{items: replyItems, cursorID: "10", cursorTime: 99, isEnd: true, lastViewAt: 100})
	}))
	defer server.Close()

	store := NewMemoryCursorStore()
	source := newSource(t, server, store)
	batch, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 2 {
		t.Fatalf("应只产出 last_view_at 之后的消息，实际 %d", len(batch.Messages))
	}
	if batch.Messages[0].RPID != "777" || batch.Messages[0].Feed != "reply" || batch.Messages[1].RPID != "778" || batch.Messages[1].Feed != "at" {
		t.Fatalf("消息不符：%+v", batch.Messages)
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	cursor, _ := store.Load("8")
	if !cursor.Initialized || cursor.ReplyTime != 101 || cursor.ReplyID != "11" || cursor.AtTime != 102 || cursor.AtID != "12" {
		t.Fatalf("水位不符：%+v", cursor)
	}
	if cursor.LastViewAt != 100 {
		t.Fatalf("查看水位应落盘：%+v", cursor)
	}

	// 同一批数据再拉一次：水位已覆盖，不应重复产出。
	again, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Messages) != 0 {
		t.Fatalf("第二轮不应重复产出：%+v", again.Messages)
	}
	if err := again.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestBiliSourceFirstRunBaselinesPerFeed(t *testing.T) {
	replyItems := "[" + itemJSON("11", 110, 1, "123", "777", "0", "42", "较新回复") + "," +
		itemJSON("10", 90, 1, "123", "776", "0", "43", "旧回复") + "]"
	atItems := "[" + itemJSON("13", 120, 1, "124", "778", "0", "44", "较新@") + "," +
		itemJSON("12", 60, 1, "124", "779", "0", "45", "查看水位之后的@") + "]"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == msgfeedAtPathForTest {
			writeFeedPage(w, feedPage{items: atItems, cursorID: "12", cursorTime: 60, isEnd: true, lastViewAt: 50})
			return
		}
		writeFeedPage(w, feedPage{items: replyItems, cursorID: "10", cursorTime: 90, isEnd: true, lastViewAt: 100})
	}))
	defer server.Close()

	store := NewMemoryCursorStore()
	source := newSource(t, server, store)
	batch, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// at 的基线是 50：60 的 @ 虽早于 reply 的查看水位 100，也必须采到。
	if len(batch.Messages) != 3 {
		t.Fatalf("应按各自基线产出消息，实际 %d：%+v", len(batch.Messages), batch.Messages)
	}
	feeds := map[string]string{}
	for _, m := range batch.Messages {
		feeds[m.Feed] += " " + m.RPID
	}
	if feeds["reply"] != " 777" || feeds["at"] != " 778 779" {
		t.Fatalf("消息来源不符：%+v", feeds)
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	cursor, _ := store.Load("8")
	if cursor.ReplyTime != 110 || cursor.AtTime != 120 || cursor.LastViewAt != 100 {
		t.Fatalf("水位不符：%+v", cursor)
	}
}

func TestBiliSourcePullsReadMessagesAfterOwnerView(t *testing.T) {
	replyItems := "[" + itemJSON("20", 150, 1, "123", "780", "0", "42", "已被主人读过的回复") + "]"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == msgfeedAtPathForTest {
			writeFeedPage(w, feedPage{items: "[]", isEnd: true, lastViewAt: 200})
			return
		}
		writeFeedPage(w, feedPage{items: replyItems, cursorID: "20", cursorTime: 150, isEnd: true, lastViewAt: 200})
	}))
	defer server.Close()

	store := NewMemoryCursorStore()
	if err := store.Save("8", Cursor{Initialized: true, ReplyTime: 100, ReplyID: "10", AtTime: 100, AtID: "10", LastViewAt: 100}); err != nil {
		t.Fatal(err)
	}
	core, logs := observer.New(zap.WarnLevel)
	source := newSource(t, server, store)
	source.Log = zap.New(core)
	batch, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 主人顶号查看后 last_view_at 跳到 200，150 的已读消息仍要采到。
	if len(batch.Messages) != 1 || batch.Messages[0].RPID != "780" {
		t.Fatalf("已读消息未被采到：%+v", batch.Messages)
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	cursor, _ := store.Load("8")
	if cursor.ReplyTime != 150 || cursor.LastViewAt != 200 {
		t.Fatalf("游标应只按已处理消息推进，查看水位仅记录：%+v", cursor)
	}
	warned := false
	for _, entry := range logs.All() {
		if entry.Message == "消息查看水位超前，仍按已处理游标拉取" && entry.ContextMap()["action"] == "msgfeed_watermark_ahead" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("顶号查看水位超前应留下告警")
	}
}

func TestBiliSourceNoLastViewBaselinesWithoutReplayingHistory(t *testing.T) {
	var mu sync.Mutex
	replyItems := "[" + itemJSON("20", 200, 1, "123", "777", "0", "42", "历史") + "]"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		items := replyItems
		mu.Unlock()
		if r.URL.Path == msgfeedAtPathForTest {
			writeFeedPage(w, feedPage{items: "[]", isEnd: true})
			return
		}
		writeFeedPage(w, feedPage{items: items, cursorID: "20", cursorTime: 200, isEnd: true})
	}))
	defer server.Close()

	store := NewMemoryCursorStore()
	source := newSource(t, server, store)
	first, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 0 {
		t.Fatal("没有 last_view_at 时首次接入不应补答历史")
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	cursor, _ := store.Load("8")
	// 没有 last_view_at 时按类别各自建基线：reply 用自身最新一条，at 为空用当前时刻。
	if !cursor.Initialized || cursor.ReplyTime != 200 || cursor.ReplyID != "20" || cursor.AtTime != 300 || cursor.AtID != "" {
		t.Fatalf("基线不符：%+v", cursor)
	}

	mu.Lock()
	replyItems = "[" + itemJSON("21", 201, 1, "123", "778", "0", "42", "新消息") + "]"
	mu.Unlock()
	second, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 1 || second.Messages[0].RPID != "778" {
		t.Fatalf("新消息未产出：%+v", second.Messages)
	}
}

func TestBiliSourcePagesUntilWatermark(t *testing.T) {
	replyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == msgfeedAtPathForTest {
			writeFeedPage(w, feedPage{items: "[]", isEnd: true, lastViewAt: 100})
			return
		}
		replyCalls++
		if r.URL.Query().Get("id") == "11" {
			writeFeedPage(w, feedPage{items: "[" + itemJSON("10", 90, 1, "123", "700", "0", "42", "旧消息") + "]", cursorID: "10", cursorTime: 90, isEnd: true, lastViewAt: 100})
			return
		}
		writeFeedPage(w, feedPage{items: "[" + itemJSON("11", 105, 1, "123", "777", "0", "42", "新消息") + "]", cursorID: "11", cursorTime: 105, isEnd: false, lastViewAt: 100})
	}))
	defer server.Close()

	store := NewMemoryCursorStore()
	source := newSource(t, server, store)
	batch, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].RPID != "777" {
		t.Fatalf("翻页消息不符：%+v", batch.Messages)
	}
	if replyCalls != 2 {
		t.Fatalf("翻页次数 = %d", replyCalls)
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	cursor, _ := store.Load("8")
	if cursor.ReplyTime != 105 || cursor.ReplyID != "11" {
		t.Fatalf("水位应推进到新消息而非旧消息：%+v", cursor)
	}
}

func TestBiliSourceSkipsAtNonComment(t *testing.T) {
	nonComment := `[{"id":30,"user":{"mid":42,"nickname":"观众"},"item":{"type":"dynamic","business_id":17,"subject_id":123,"source_id":0,"root_id":0,"source_content":"动态正文@你"},"at_time":150}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == msgfeedAtPathForTest {
			writeFeedPage(w, feedPage{items: nonComment, cursorID: "30", cursorTime: 150, isEnd: true, lastViewAt: 100})
			return
		}
		writeFeedPage(w, feedPage{items: "[]", isEnd: true, lastViewAt: 100})
	}))
	defer server.Close()

	source := newSource(t, server, NewMemoryCursorStore())
	batch, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 0 {
		t.Fatalf("非评论 @ 不应产出评论事件：%+v", batch.Messages)
	}
}

func TestBiliSourceCursorIsPerAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFeedPage(w, feedPage{items: "[]", isEnd: true})
	}))
	defer server.Close()

	actors := &fakeActors{actor: bili.Actor{MID: "8", Cookie: "SESSDATA=a"}}
	source := newSource(t, server, NewMemoryCursorStore())
	source.Actors = actors
	first, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	actors.actor = bili.Actor{MID: "9", Cookie: "SESSDATA=b"}
	second, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.SelfID != "9" {
		t.Fatalf("账号快照未生效：%q", second.SelfID)
	}
	if err := second.Commit(); err != nil {
		t.Fatal(err)
	}
	store := source.Cursors.(*MemoryCursorStore)
	if c8, _ := store.Load("8"); !c8.Initialized {
		t.Fatal("账号 8 的水位未保存")
	}
	if c9, _ := store.Load("9"); !c9.Initialized {
		t.Fatal("账号 9 的水位未保存")
	}
}

func TestBiliSourceClassifiesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusPreconditionFailed)
	}))
	defer server.Close()
	source := newSource(t, server, NewMemoryCursorStore())
	_, err := source.Fetch(context.Background())
	var kinded *Error
	if !asError(err, &kinded) || kinded.Kind != KindRate {
		t.Fatalf("412 应分类为限流：%v", err)
	}
}

type fakeActors struct{ actor bili.Actor }

func (f *fakeActors) CurrentActor(context.Context) (bili.Actor, error) { return f.actor, nil }

func asError(err error, target **Error) bool {
	for err != nil {
		if typed, ok := err.(*Error); ok {
			*target = typed
			return true
		}
		type unwrapper interface{ Unwrap() error }
		next, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = next.Unwrap()
	}
	return false
}

const msgfeedAtPathForTest = "/x/msgfeed/at"

type countingAdmission struct {
	began    int
	released int
}

func (a *countingAdmission) BeginOperation(context.Context) (func(), error) {
	a.began++
	var once sync.Once
	return func() { once.Do(func() { a.released++ }) }, nil
}

// TestBiliSourceHoldsAdmissionUntilBatchClose 账号准入门槛覆盖整轮拉取，批次关闭才释放。
func TestBiliSourceHoldsAdmissionUntilBatchClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFeedPage(w, feedPage{items: "[]", isEnd: true, lastViewAt: 100})
	}))
	defer server.Close()
	source := newSource(t, server, NewMemoryCursorStore())
	admission := &countingAdmission{}
	source.Admission = admission
	batch, err := source.Fetch(context.Background())
	if err != nil {
		t.Fatalf("拉取失败：%v", err)
	}
	if admission.began != 1 || admission.released != 0 {
		t.Fatalf("拉取期间应持有准入：%+v", admission)
	}
	if batch.Close == nil {
		t.Fatal("批次应带释放回调")
	}
	batch.Close()
	if admission.released != 1 {
		t.Fatalf("批次关闭后应释放准入：%+v", admission)
	}
}

// TestBiliSourceReleasesAdmissionOnError 拉取失败必须立即归还准入，不能拖到下一轮。
func TestBiliSourceReleasesAdmissionOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	source := newSource(t, server, NewMemoryCursorStore())
	admission := &countingAdmission{}
	source.Admission = admission
	if _, err := source.Fetch(context.Background()); err == nil {
		t.Fatal("应返回拉取错误")
	}
	if admission.began != 1 || admission.released != 1 {
		t.Fatalf("失败后应释放准入：%+v", admission)
	}
}
