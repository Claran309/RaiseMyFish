package comment

// 本文件用 httptest 断言评论发送的 POST 参数、csrf、1条/分钟限频与错误分类。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

const testCookie = "SESSDATA=fixture; bili_jct=csrf-token"

type sendFixture struct {
	mu          sync.Mutex
	calls       int
	form        url.Values
	cookie      string
	contentType string
	code        int
	status      int
	raw         string
}

func newSendServer(t *testing.T) (*sendFixture, *httptest.Server) {
	t.Helper()
	fixture := &sendFixture{code: 0}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fixture.mu.Lock()
		fixture.calls++
		fixture.form = r.PostForm
		fixture.cookie = r.Header.Get("Cookie")
		fixture.contentType = r.Header.Get("Content-Type")
		status, raw := fixture.status, fixture.raw
		code := fixture.code
		fixture.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(raw))
			return
		}
		if raw != "" {
			_, _ = w.Write([]byte(raw))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": "0", "data": map[string]any{"rpid": 98765}})
	}))
	t.Cleanup(srv.Close)
	return fixture, srv
}

func newService(t *testing.T, root string) *Service {
	t.Helper()
	o := DefaultOptions()
	o.Cookie = testCookie
	o.APIRoot = root
	o.HTTPTimeout = time.Second
	s, err := New(o, zap.NewNop())
	if err != nil {
		t.Fatalf("构造发送器失败：%v", err)
	}
	return s
}

func testClaim() event.Claim {
	return event.Claim{
		Event: event.Event{
			ID:       "comment:123:456",
			Channel:  event.ChComment,
			Dir:      event.DirIn,
			Session:  "123",
			FromID:   "42",
			FromName: "路人",
			Text:     "视频里那条鱼真肥",
			Private:  false,
			ReplyTo:  "777",
			At:       time.Unix(1700000000, 0).UTC(),
			Status:   event.StatusQueued,
		},
		Stage:      event.StageGenerate,
		Lease:      event.Lease{Owner: "worker-1", Version: 1},
		LeaseUntil: time.Now().Add(time.Minute),
	}
}

func testReply(text string) sender.Reply {
	return sender.Reply{Text: text, Scope: memory.Scope{Channel: event.ChComment, Session: "123"}}
}

func kindOf(t *testing.T, err error) sender.Kind {
	t.Helper()
	var typed *sender.Error
	if !errors.As(err, &typed) {
		t.Fatalf("错误不是 *sender.Error：%v", err)
	}
	return typed.Kind
}

func TestSendPostsExpectedForm(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)

	res, err := s.Send(context.Background(), testClaim(), testReply("你好呀"))
	if err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if !res.OK || res.MsgID != "98765" || !res.Verified {
		t.Errorf("结果不符：%+v", res)
	}
	fixture.mu.Lock()
	calls, form, cookie, contentType := fixture.calls, fixture.form, fixture.cookie, fixture.contentType
	fixture.mu.Unlock()
	if calls != 1 {
		t.Fatalf("请求次数 = %d", calls)
	}
	if form.Get("oid") != "123" || form.Get("type") != "1" || form.Get("message") != "你好呀" || form.Get("root") != "777" || form.Get("parent") != "777" || form.Get("csrf") != "csrf-token" {
		t.Errorf("POST 参数不符：%v", form)
	}
	if cookie != testCookie {
		t.Errorf("Cookie = %q", cookie)
	}
	if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q", contentType)
	}
}

// TestSendUsesCollectedRoot 多级回复：root 用采集到的线程根，parent 用被回复评论。
func TestSendUsesCollectedRoot(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)
	claim := testClaim()
	claim.Event.Meta = json.RawMessage(`{"source":"bili-comment","comment_type":1,"root":"555","parent":"666"}`)
	if _, err := s.Send(context.Background(), claim, testReply("回你")); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	fixture.mu.Lock()
	form := fixture.form
	fixture.mu.Unlock()
	if form.Get("root") != "555" || form.Get("parent") != "777" {
		t.Errorf("root/parent 不符：%v", form)
	}
}

// TestSendZeroRootFallsBack 顶层评论 Meta.root 为空/0 时回落到本条 rpid。
func TestSendZeroRootFallsBack(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)
	claim := testClaim()
	claim.Event.Meta = json.RawMessage(`{"source":"bili-comment","comment_type":1,"root":"0","parent":"0"}`)
	if _, err := s.Send(context.Background(), claim, testReply("回你")); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	fixture.mu.Lock()
	form := fixture.form
	fixture.mu.Unlock()
	if form.Get("root") != "777" || form.Get("parent") != "777" {
		t.Errorf("root/parent 不符：%v", form)
	}
}

func TestSendFallsBackToEventRpid(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)
	claim := testClaim()
	claim.Event.ReplyTo = ""
	if _, err := s.Send(context.Background(), claim, testReply("回你")); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	fixture.mu.Lock()
	form := fixture.form
	fixture.mu.Unlock()
	if form.Get("root") != "456" || form.Get("parent") != "456" {
		t.Errorf("未回落到事件 ID 里的 rpid：%v", form)
	}
}

func TestSendTruncatesTo200Runes(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)
	long := strings.Repeat("肥", 250)
	if _, err := s.Send(context.Background(), testClaim(), testReply(long)); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	fixture.mu.Lock()
	message := fixture.form.Get("message")
	fixture.mu.Unlock()
	if len([]rune(message)) != maxReplyRunes {
		t.Errorf("超长正文截断后 = %d rune", len([]rune(message)))
	}
}

func TestSendRateLimitedBacksOffExponentially(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	setCode := func(code int) {
		fixture.mu.Lock()
		fixture.code = code
		fixture.mu.Unlock()
	}
	calls := func() int {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		return fixture.calls
	}
	retryAt := func(err error) time.Time {
		t.Helper()
		var typed *sender.Error
		if !errors.As(err, &typed) || typed.Kind != sender.KindRetry {
			t.Fatalf("应为 retry：%v", err)
		}
		if typed.RetryAt.IsZero() {
			t.Fatal("retry 未提供 RetryAt")
		}
		return typed.RetryAt
	}

	// 平台第一次限流 → 30s 后重试
	setCode(-352)
	_, err := s.Send(context.Background(), testClaim(), testReply("第一条"))
	if got := retryAt(err); !got.Equal(now.Add(30 * time.Second)) {
		t.Errorf("首次退避 = %v，期望 %v", got, now.Add(30*time.Second))
	}
	if calls() != 1 {
		t.Fatalf("首次限流应已发出 1 次请求：%d", calls())
	}

	// 退避未到点的重投不产生请求
	if _, err := s.Send(context.Background(), testClaim(), testReply("第二条")); retryAt(err).Before(now.Add(30 * time.Second)) {
		t.Error("退避中的重投不应缩短等待")
	}
	if calls() != 1 {
		t.Fatalf("退避期间仍产生了请求：%d", calls())
	}

	// 退避到期仍被限流 → 翻倍到 60s
	now = now.Add(30 * time.Second)
	_, err = s.Send(context.Background(), testClaim(), testReply("第三条"))
	if got := retryAt(err); !got.Equal(now.Add(60 * time.Second)) {
		t.Errorf("第二次退避 = %v，期望 %v", got, now.Add(60*time.Second))
	}
	if calls() != 2 {
		t.Fatalf("退避到期后应重新 POST：%d", calls())
	}

	// 退避到期且平台放行 → 成功并清除退避
	now = now.Add(60 * time.Second)
	setCode(0)
	if _, err := s.Send(context.Background(), testClaim(), testReply("第四条")); err != nil {
		t.Fatalf("退避到期应可发送：%v", err)
	}
	if calls() != 3 {
		t.Fatalf("放行后请求次数 = %d", calls())
	}

	// 成功后重新计数：再次限流仍从 30s 起
	setCode(-352)
	_, err = s.Send(context.Background(), testClaim(), testReply("第五条"))
	if got := retryAt(err); !got.Equal(now.Add(30 * time.Second)) {
		t.Errorf("成功后重新退避 = %v，期望 %v", got, now.Add(30*time.Second))
	}
}

func TestSendRateLimitByMessage(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	fixture.mu.Lock()
	fixture.raw = `{"code":77001,"message":"操作太快，请稍后再试"}`
	fixture.mu.Unlock()

	_, err := s.Send(context.Background(), testClaim(), testReply("测试"))
	var typed *sender.Error
	if !errors.As(err, &typed) || typed.Kind != sender.KindRetry {
		t.Fatalf("「操作太快」应归为 retry：%v", err)
	}
	if !typed.RetryAt.Equal(now.Add(30 * time.Second)) {
		t.Errorf("RetryAt = %v，期望 %v", typed.RetryAt, now.Add(30*time.Second))
	}
}

func TestSendUsesEventCommentType(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)
	claim := testClaim()
	claim.Event.Meta = json.RawMessage(`{"source":"bili-comment","comment_type":17}`)
	if _, err := s.Send(context.Background(), claim, testReply("动态下面回你")); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	fixture.mu.Lock()
	form := fixture.form
	fixture.mu.Unlock()
	if form.Get("type") != "17" {
		t.Errorf("动态评论 type = %q，期望 17", form.Get("type"))
	}
}

func TestSendErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		code   int
		status int
		raw    string
		want   sender.Kind
	}{
		{"风控重试", -352, 0, "", sender.KindRetry},
		{"限流重试", -509, 0, "", sender.KindRetry},
		{"权限失败", -101, 0, "", sender.KindAuth},
		{"评论区关闭", commentClosed, 0, "", sender.KindDead},
		{"未知平台错误", -1, 0, "", sender.KindUncertain},
		{"文本限流", 0, 0, `{"code":77001,"message":"操作太快"}`, sender.KindRetry},
		{"HTTP限流", 0, http.StatusPreconditionFailed, "", sender.KindRetry},
		{"HTTP权限", 0, http.StatusForbidden, "", sender.KindAuth},
		{"HTTP服务端", 0, http.StatusInternalServerError, "", sender.KindUncertain},
		{"响应非JSON", 0, 0, "not-json", sender.KindUncertain},
		{"成功但无rpid", 0, 0, `{"code":0,"data":{}}`, sender.KindUncertain},
		{"缺data", 0, 0, `{"code":0}`, sender.KindUncertain},
		{"空白对象", 0, 0, `{}`, sender.KindUncertain},
		{"缺code", 0, 0, `{"data":{"rpid":98765}}`, sender.KindUncertain},
		{"code为null", 0, 0, `{"code":null,"data":{"rpid":98765}}`, sender.KindUncertain},
		{"code为字符串", 0, 0, `{"code":"0","data":{"rpid":98765}}`, sender.KindUncertain},
		{"code为浮点", 0, 0, `{"code":0.0,"data":{"rpid":98765}}`, sender.KindUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, srv := newSendServer(t)
			fixture.code, fixture.status, fixture.raw = tc.code, tc.status, tc.raw
			s := newService(t, srv.URL)
			_, err := s.Send(context.Background(), testClaim(), testReply("测试"))
			if err == nil {
				t.Fatalf("期望错误，实际成功：%v", err)
			}
			if kind := kindOf(t, err); kind != tc.want {
				t.Errorf("错误类别 = %s，期望 %s（%v）", kind, tc.want, err)
			}
		})
	}
}

func TestSendMissingCSRF(t *testing.T) {
	fixture, srv := newSendServer(t)
	o := DefaultOptions()
	o.Cookie = "SESSDATA=only"
	o.APIRoot = srv.URL
	o.HTTPTimeout = time.Second
	s, err := New(o, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(context.Background(), testClaim(), testReply("你好")); kindOf(t, err) != sender.KindAuth {
		t.Fatalf("缺 csrf 应为 auth，实际 %v", err)
	}
	fixture.mu.Lock()
	calls := fixture.calls
	fixture.mu.Unlock()
	if calls != 0 {
		t.Fatalf("缺 csrf 仍发出了请求：%d", calls)
	}
}

func TestSendRejectsInvalid(t *testing.T) {
	_, srv := newSendServer(t)
	s := newService(t, srv.URL)
	cases := []struct {
		name string
		edit func(*event.Claim, *sender.Reply)
	}{
		{"通道不符", func(c *event.Claim, r *sender.Reply) { c.Event.Channel = event.ChLive }},
		{"私密事件", func(c *event.Claim, r *sender.Reply) { c.Event.Private = true }},
		{"方向不符", func(c *event.Claim, r *sender.Reply) { c.Event.Dir = event.DirOut }},
		{"缺租约", func(c *event.Claim, r *sender.Reply) { c.Lease.Owner = "" }},
		{"会话非法", func(c *event.Claim, r *sender.Reply) { c.Event.Session = "abc" }},
		{"事件ID不符", func(c *event.Claim, r *sender.Reply) { c.Event.ID = "comment:999:456" }},
		{"rpid非法", func(c *event.Claim, r *sender.Reply) { c.Event.ReplyTo = "x" }},
		{"Scope不符", func(c *event.Claim, r *sender.Reply) { r.Scope.Session = "999" }},
		{"空正文", func(c *event.Claim, r *sender.Reply) { r.Text = "   " }},
		{"正文含NUL", func(c *event.Claim, r *sender.Reply) { r.Text = "a\x00b" }},
		{"正文非UTF8", func(c *event.Claim, r *sender.Reply) { r.Text = string([]byte{0xff, 0xfe}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claim := testClaim()
			reply := testReply("你好")
			tc.edit(&claim, &reply)
			if _, err := s.Send(context.Background(), claim, reply); kindOf(t, err) != sender.KindInput {
				t.Fatalf("非法输入应报 input，实际 %v", err)
			}
		})
	}
}

func TestResumeNeverReposts(t *testing.T) {
	fixture, srv := newSendServer(t)
	s := newService(t, srv.URL)
	if _, err := s.Resume(context.Background(), testClaim()); kindOf(t, err) != sender.KindUncertain {
		t.Fatalf("Resume 应返回 uncertain，实际 %v", err)
	}
	fixture.mu.Lock()
	calls := fixture.calls
	fixture.mu.Unlock()
	if calls != 0 {
		t.Fatalf("Resume 不应重新 POST：%d", calls)
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	valid := func() Options {
		o := DefaultOptions()
		o.Cookie = testCookie
		return o
	}
	cases := []struct {
		name string
		edit func(*Options)
	}{
		{"无凭据", func(o *Options) { o.Cookie = "" }},
		{"凭据换行", func(o *Options) { o.Cookie = "SESSDATA=x\nX: 1" }},
		{"零超时", func(o *Options) { o.HTTPTimeout = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := valid()
			tc.edit(&o)
			if _, err := New(o, zap.NewNop()); err == nil {
				t.Fatal("非法配置未被拒绝")
			}
		})
	}
	if _, err := New(valid(), nil); err == nil {
		t.Fatal("nil 日志器未被拒绝")
	}
}

func TestPlatformIDAcceptsNumberAndString(t *testing.T) {
	var got struct {
		A platformID `json:"a"`
		B platformID `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":123,"b":"456"}`), &got); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got.A != "123" || got.B != "456" {
		t.Errorf("platformID = %q/%q", got.A, got.B)
	}
}
