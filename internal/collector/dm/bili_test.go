package dm

// 本文件用 httptest 断言私信适配器：变化会话拉详情、多条消息无遗漏、
// 自己发的过滤、未变化不重复拉详情、详情失败降级与鉴权分类。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
)

const selfUID = "3747"

type dmFixture struct {
	mu    sync.Mutex
	body  string
	code  int // 非 0 时 get_sessions 直接回平台错误码
	msgs  map[string]string
	bad   map[string]bool
	calls map[string]int
}

func (f *dmFixture) detailCalls(talker string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[talker]
}

func newFetcher(t *testing.T, fixture *dmFixture) *BiliFetcher {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			// WBI 签名密钥（fetch_session_msgs 需要 w_rid/wts）
			_, _ = w.Write([]byte(`{"code":0,"data":{"wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`))
		case "/session_svr/v1/session_svr/get_sessions":
			fixture.mu.Lock()
			body, code := fixture.body, fixture.code
			fixture.mu.Unlock()
			if code != 0 {
				_, _ = w.Write([]byte(`{"code":-101,"message":"账号未登录"}`))
				return
			}
			_, _ = w.Write([]byte(body))
		case "/svr_sync/v1/svr_sync/fetch_session_msgs":
			talker := r.URL.Query().Get("talker_id")
			if r.URL.Query().Get("session_type") != "1" || r.URL.Query().Get("size") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fixture.mu.Lock()
			fixture.calls[talker]++
			body, bad := fixture.msgs[talker], fixture.bad[talker]
			fixture.mu.Unlock()
			if bad {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if body == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := bili.New(bili.Options{
		APIRoot: srv.URL, // nav（WBI 密钥）也走 mock
		VCRoot:  srv.URL,
		Cookie:  "SESSDATA=fixture; bili_jct=csrf",
		SelfMID: selfUID,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造 bili 客户端失败：%v", err)
	}
	return NewBiliFetcher(client, selfUID, 20)
}

// sessionBody 构造单会话的 get_sessions 响应。
func sessionBody(talker, msgKey, sender, content string, ts int64) string {
	quoted, _ := json.Marshal(content)
	return `{"code":0,"data":{"session_list":[{"talker_id":"` + talker +
		`","last_msg":{"msg_key":"` + msgKey + `","sender_uid":"` + sender +
		`","content":` + string(quoted) + `,"timestamp":` + strconv.FormatInt(ts, 10) + `}}]}}`
}

// detailBody 构造 get_session_msgs 响应；content 是平台内层 JSON。
func detailBody(items ...[3]string) string {
	out := `{"code":0,"data":{"messages":[`
	for i, item := range items {
		if i > 0 {
			out += ","
		}
		out += `{"msg_key":"` + item[0] + `","sender_uid":"` + item[1] +
			`","content":"{\"content\":\"` + item[2] + `\"}","timestamp":1700000000}`
	}
	return out + `]}}`
}

// TestBiliFetcherPullsSessionDetails 变化会话拉详情：连发多条全部产出，自己的跳过。
func TestBiliFetcherPullsSessionDetails(t *testing.T) {
	fixture := &dmFixture{
		body: sessionBody("2002", "9003", "2002", "第三条", 1700000003),
		msgs: map[string]string{"2002": detailBody(
			[3]string{"9001", "2002", "第一条"},
			[3]string{"9002", "2002", "第二条"},
			[3]string{"9003", "2002", "第三条"},
			[3]string{"9004", selfUID, "我回的"},
		)},
		calls: map[string]int{},
	}
	f := newFetcher(t, fixture)

	batch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("拉取失败：%v", err)
	}
	got := batch.Messages
	if len(got) != 3 {
		t.Fatalf("应产出对方 3 条消息，实际 %d：%+v", len(got), got)
	}
	for i, id := range []string{"9001", "9002", "9003"} {
		if got[i].ID != id || got[i].PeerID != "2002" {
			t.Errorf("第 %d 条不符：%+v", i+1, got[i])
		}
	}
	if got[2].Text != "第三条" {
		t.Errorf("正文未解包：%q", got[2].Text)
	}
	if fixture.detailCalls("2002") != 1 {
		t.Errorf("详情请求次数 = %d", fixture.detailCalls("2002"))
	}
}

// TestBiliFetcherSkipsUnchangedDetail 会话 last_msg 没变时不再拉详情。
func TestBiliFetcherSkipsUnchangedDetail(t *testing.T) {
	fixture := &dmFixture{
		body:  sessionBody("2002", "9001", "2002", "在吗", 1700000001),
		msgs:  map[string]string{"2002": detailBody([3]string{"9001", "2002", "在吗"})},
		calls: map[string]int{},
	}
	f := newFetcher(t, fixture)
	for i := 0; i < 2; i++ {
		if _, err := f.Fetch(context.Background()); err != nil {
			t.Fatalf("第 %d 轮失败：%v", i+1, err)
		}
	}
	if fixture.detailCalls("2002") != 1 {
		t.Errorf("未变化会话不应重复拉详情，实际 %d 次", fixture.detailCalls("2002"))
	}
}

// TestBiliFetcherRetriesDetailAfterFailure 详情失败先降级 last_msg，下一轮重试。
func TestBiliFetcherRetriesDetailAfterFailure(t *testing.T) {
	fixture := &dmFixture{
		body:  sessionBody("2002", "9002", "2002", "第二条", 1700000002),
		msgs:  map[string]string{"2002": detailBody([3]string{"9001", "2002", "第一条"}, [3]string{"9002", "2002", "第二条"})},
		bad:   map[string]bool{"2002": true},
		calls: map[string]int{},
	}
	f := newFetcher(t, fixture)

	firstBatch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("详情失败不应让整轮失败：%v", err)
	}
	first := firstBatch.Messages
	if len(first) != 1 || first[0].ID != "9002" {
		t.Fatalf("应降级为 last_msg：%+v", first)
	}

	fixture.mu.Lock()
	fixture.bad["2002"] = false
	fixture.mu.Unlock()
	secondBatch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	second := secondBatch.Messages
	if len(second) != 2 || fixture.detailCalls("2002") != 2 {
		t.Fatalf("第二轮应重试详情并补全消息：calls=%d got=%+v", fixture.detailCalls("2002"), second)
	}
}

// TestBiliFetcherFallbackTextMatchesDetail 详情失败降级的正文与正常详情一致（都解包 JSON）。
func TestBiliFetcherFallbackTextMatchesDetail(t *testing.T) {
	fixture := &dmFixture{
		body:  sessionBody("2002", "9002", "2002", "{\"content\":\"第二条\"}", 1700000002),
		msgs:  map[string]string{"2002": detailBody([3]string{"9002", "2002", "第二条"})},
		bad:   map[string]bool{"2002": true},
		calls: map[string]int{},
	}
	f := newFetcher(t, fixture)

	fallbackBatch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("详情失败不应让整轮失败：%v", err)
	}
	fallback := fallbackBatch.Messages
	if len(fallback) != 1 || fallback[0].ID != "9002" {
		t.Fatalf("详情失败应降级为 last_msg：%+v", fallback)
	}
	if fallback[0].Text != "第二条" {
		t.Fatalf("降级正文未解包：%q", fallback[0].Text)
	}

	fixture.mu.Lock()
	fixture.bad["2002"] = false
	fixture.mu.Unlock()
	normalBatch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败：%v", err)
	}
	normal := normalBatch.Messages
	if len(normal) != 1 || normal[0].ID != "9002" {
		t.Fatalf("第二轮应产出详情消息：%+v", normal)
	}
	if normal[0].Text != fallback[0].Text {
		t.Fatalf("正常/降级正文不一致：normal=%q fallback=%q", normal[0].Text, fallback[0].Text)
	}
}

// TestBiliFetcherSelfLastSession 最后一条是自己时也要拉详情，捞回对方消息。
func TestBiliFetcherSelfLastSession(t *testing.T) {
	fixture := &dmFixture{
		body: sessionBody("2002", "9102", selfUID, "我回的", 1700000002),
		msgs: map[string]string{"2002": detailBody(
			[3]string{"9101", "2002", "在吗"},
			[3]string{"9102", selfUID, "我回的"},
		)},
		calls: map[string]int{},
	}
	f := newFetcher(t, fixture)

	batch, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := batch.Messages
	if len(got) != 1 || got[0].ID != "9101" || got[0].Text != "在吗" {
		t.Fatalf("应只产出对方消息：%+v", got)
	}
}

// TestBiliFetcherSessionsAuthError 会话列表鉴权错误 → KindAuth。
func TestBiliFetcherSessionsAuthError(t *testing.T) {
	fixture := &dmFixture{code: -101, calls: map[string]int{}}
	f := newFetcher(t, fixture)
	_, err := f.Fetch(context.Background())
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("应归为鉴权错误：%v", err)
	}
}

// TestBiliFetcherDetailAuthError 详情鉴权错误 → KindAuth（整体停采集）。
func TestBiliFetcherDetailAuthError(t *testing.T) {
	fixture := &dmFixture{
		body:  sessionBody("2002", "9001", "2002", "在吗", 1700000001),
		msgs:  map[string]string{"2002": `{"code":-101,"message":"账号未登录"}`},
		calls: map[string]int{},
	}
	f := newFetcher(t, fixture)
	_, err := f.Fetch(context.Background())
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("应归为鉴权错误：%v", err)
	}
}

// TestBiliFetcherNilClient 无凭据 → 鉴权错误，上层停采集。
func TestBiliFetcherNilClient(t *testing.T) {
	f := NewBiliFetcher(nil, selfUID, 20)
	_, err := f.Fetch(context.Background())
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("应归为鉴权错误：%v", err)
	}
}
