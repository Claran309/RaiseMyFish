package dm

// 本文件测私信发送护栏：白名单、当日收信人锁定、限频、互关检查、错误分类。

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/sender"
)

type fakeTransport struct {
	sent    int
	lastID  string
	lastTxt string
	err     error
}

func (f *fakeTransport) SendDM(_ context.Context, mid, text string) (string, error) {
	f.sent++
	f.lastID, f.lastTxt = mid, text
	if f.err != nil {
		return "", f.err
	}
	return "msg-1", nil
}

type fakeRelation struct {
	attribute int
	err       error
}

func (f *fakeRelation) Attribute(context.Context, string) (int, error) {
	return f.attribute, f.err
}

func newSender(t *testing.T, clock *time.Time) (*Sender, *fakeTransport, *fakeRelation) {
	t.Helper()
	tr := &fakeTransport{}
	rel := &fakeRelation{attribute: mutualAttribute}
	s, err := New(tr, rel, Options{
		AllFollowers:  false,
		AllowedMIDs:   []string{"313591854", "777"},
		RateLimit:     10 * time.Minute,
		Now:           func() time.Time { return *clock },
		RecipientPath: filepath.Join(t.TempDir(), "dm-recipient.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, tr, rel
}

func newAllFollowersSender(t *testing.T, clock *time.Time) (*Sender, *fakeTransport) {
	t.Helper()
	tr := &fakeTransport{}
	s, err := New(tr, &fakeRelation{attribute: mutualAttribute}, Options{
		AllFollowers: true,
		Now:          func() time.Time { return *clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, tr
}

func claimFor(peer string) event.Claim {
	return event.Claim{Event: event.Event{ID: "dm:" + peer + ":1", Channel: event.ChDM, Dir: event.DirIn, Session: peer, FromID: peer, Text: "在吗"}}
}

// compositeClaimFor 是新格式私信事件：Session 带接收账号，对方 UID 仍是收信人。
func compositeClaimFor(recv, peer string) event.Claim {
	return event.Claim{
		Event: event.Event{
			ID: "dm:" + recv + ":" + peer + ":1", Channel: event.ChDM, Dir: event.DirIn,
			Session: event.DMSession(recv, peer), FromID: peer, Text: "在吗", Private: true,
		},
		Lease: event.Lease{Owner: "worker-1", Version: 1},
	}
}

// TestSendCompositeSessionUsesPeerAsRecipient 复合 Session 不影响收信人解析。
func TestSendCompositeSessionUsesPeerAsRecipient(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, tr := newAllFollowersSender(t, &now)
	c := compositeClaimFor("1001", "2002")
	if _, err := s.Send(context.Background(), c, sender.Reply{Text: "你好"}); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if tr.lastID != "2002" || tr.lastTxt != "你好" {
		t.Fatalf("收信人解析错误：mid=%q text=%q", tr.lastID, tr.lastTxt)
	}
	normalized, err := Normalize(c, sender.Reply{
		Text:  " 你好 ",
		Scope: memory.Scope{Channel: event.ChDM, Session: c.Event.Session, Private: true},
	})
	if err != nil || normalized.Text != "你好" {
		t.Fatalf("Normalize=%+v %v", normalized, err)
	}
}

// TestSendWhitelistBlocked 非白名单直接拒绝，不查关系不发送。
func TestSendWhitelistBlocked(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, tr, _ := newSender(t, &now)

	_, err := s.Send(context.Background(), claimFor("999"), sender.Reply{Text: "你好"})
	if err == nil {
		t.Fatal("非白名单应拒绝")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindInput {
		t.Errorf("应归为 input：%v", err)
	}
	if tr.sent != 0 {
		t.Error("拒绝时不应调用传输层")
	}
}

// TestSendRecipientLockPerDay 一天只允许一个收信人。
func TestSendRecipientLockPerDay(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, tr, _ := newSender(t, &now)

	if _, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "主人好"}); err != nil {
		t.Fatalf("首次发送失败：%v", err)
	}
	// 换人 → 拒绝
	now = now.Add(time.Hour)
	if _, err := s.Send(context.Background(), claimFor("777"), sender.Reply{Text: "换个收信人"}); err == nil {
		t.Fatal("当天换收信人应拒绝")
	}
	if tr.sent != 1 {
		t.Errorf("只应发出去 1 条，实际 %d", tr.sent)
	}
	// 隔天恢复
	now = now.Add(24 * time.Hour)
	if _, err := s.Send(context.Background(), claimFor("777"), sender.Reply{Text: "第二天"}); err != nil {
		t.Fatalf("隔天应可发送：%v", err)
	}
	if tr.sent != 2 {
		t.Errorf("应发出 2 条，实际 %d", tr.sent)
	}
}

// TestSendRateLimit 同一收信人 10 分钟限频。
func TestSendRateLimit(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, tr, _ := newSender(t, &now)

	if _, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "一"}); err != nil {
		t.Fatalf("首条失败：%v", err)
	}
	now = now.Add(9 * time.Minute)
	if _, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "二"}); err == nil {
		t.Fatal("10 分钟内第二条应被限频")
	}
	if tr.sent != 1 {
		t.Errorf("限频期间不应发送：%d", tr.sent)
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "三"}); err != nil {
		t.Fatalf("超过 10 分钟应放行：%v", err)
	}
}

// TestSendRequiresMutualFollow 非互关（attribute≠6）拒发。
func TestSendRequiresMutualFollow(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, tr, rel := newSender(t, &now)
	rel.attribute = 2

	_, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "你好"})
	if err == nil {
		t.Fatal("非互关应拒绝")
	}
	if tr.sent != 0 {
		t.Error("非互关不应发送")
	}
}

// TestSendValidatesText 正文长度/空正文。
func TestSendValidatesText(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, _, _ := newSender(t, &now)

	if _, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "  "}); err == nil {
		t.Error("空正文应拒绝")
	}
	long := make([]rune, 201)
	for i := range long {
		long[i] = '鱼'
	}
	if _, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: string(long)}); err == nil {
		t.Error("超 200 字应拒绝")
	}
}

// TestSendClassifiesErrors 传输层错误分类。
func TestSendClassifiesErrors(t *testing.T) {
	cases := []struct {
		err  error
		want Kind
	}{
		{errors.New("403 forbidden"), KindAuth},
		{errors.New("network timeout"), KindRetry},
		{errors.New("unknown boom"), KindUncertain},
	}
	for _, tc := range cases {
		now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
		s, tr, _ := newSender(t, &now)
		tr.err = tc.err

		_, err := s.Send(context.Background(), claimFor("313591854"), sender.Reply{Text: "你好"})
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != tc.want {
			t.Errorf("%v 应归为 %s，实际 %v", tc.err, tc.want, err)
		}
	}
}

// TestSendRateLimitBackoff 平台限流 → 30s 起指数退避，未到点不调用传输层。
func TestSendRateLimitBackoff(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, tr := newAllFollowersSender(t, &now)
	tr.err = errors.New("操作太快，请稍后再试")
	retryAt := func(err error) time.Time {
		t.Helper()
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != KindRetry {
			t.Fatalf("应为 retry：%v", err)
		}
		if typed.RetryAt.IsZero() {
			t.Fatal("retry 未提供 RetryAt")
		}
		return typed.RetryAt
	}

	_, err := s.Send(context.Background(), claimFor("999"), sender.Reply{Text: "一"})
	if got := retryAt(err); !got.Equal(now.Add(30 * time.Second)) {
		t.Errorf("首次退避 = %v，期望 %v", got, now.Add(30*time.Second))
	}
	if tr.sent != 1 {
		t.Fatalf("首次限流应已调用传输层：%d", tr.sent)
	}

	// 退避未到点：不调用传输层
	if _, err := s.Send(context.Background(), claimFor("999"), sender.Reply{Text: "二"}); retryAt(err).Before(now.Add(30 * time.Second)) {
		t.Error("退避中的重投不应缩短等待")
	}
	if tr.sent != 1 {
		t.Fatalf("退避期间仍调用了传输层：%d", tr.sent)
	}

	// 退避到期仍限流 → 翻倍
	now = now.Add(30 * time.Second)
	_, err = s.Send(context.Background(), claimFor("999"), sender.Reply{Text: "三"})
	if got := retryAt(err); !got.Equal(now.Add(60 * time.Second)) {
		t.Errorf("第二次退避 = %v，期望 %v", got, now.Add(60*time.Second))
	}
	if tr.sent != 2 {
		t.Fatalf("退避到期后应重试：%d", tr.sent)
	}

	// 放行后清空退避
	now = now.Add(60 * time.Second)
	tr.err = nil
	if _, err := s.Send(context.Background(), claimFor("999"), sender.Reply{Text: "四"}); err != nil {
		t.Fatalf("退避到期应可发送：%v", err)
	}
	if tr.sent != 3 {
		t.Fatalf("放行后调用次数 = %d", tr.sent)
	}
	tr.err = errors.New("rate limit")
	_, err = s.Send(context.Background(), claimFor("999"), sender.Reply{Text: "五"})
	if got := retryAt(err); !got.Equal(now.Add(30 * time.Second)) {
		t.Errorf("成功后重新退避 = %v，期望 %v", got, now.Add(30*time.Second))
	}
}

// TestClassifyBiliError B站平台错误按状态码/业务码分类，未知保持 uncertain。
func TestClassifyBiliError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Kind
	}{
		{"业务限流", &bili.Error{Op: "dm_send", Code: -352}, KindRetry},
		{"HTTP限流", &bili.Error{Op: "dm_send", Status: http.StatusTooManyRequests}, KindRetry},
		{"业务鉴权", &bili.Error{Op: "dm_send", Code: -101}, KindAuth},
		{"HTTP鉴权", &bili.Error{Op: "dm_send", Status: http.StatusForbidden}, KindAuth},
		{"未知业务码", &bili.Error{Op: "dm_send", Code: 21019}, KindUncertain},
		{"HTTP服务端", &bili.Error{Op: "dm_send", Status: http.StatusInternalServerError}, KindUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyBiliError(tc.err)
			var typed *Error
			if !errors.As(got, &typed) || typed.Kind != tc.want {
				t.Fatalf("分类 = %v，期望 %s", got, tc.want)
			}
		})
	}
	if classifyBiliError(errors.New("普通错误")) != nil {
		t.Error("非B站错误应返回 nil 交给通用分类")
	}
}

// TestClassifyPreservesCause 分类后的错误仍能展开到原平台错误/本地前置原因，
// 让 dead/unknown 日志能看到平台 code 或「缺 bili_jct」这类根因。
func TestClassifyPreservesCause(t *testing.T) {
	original := &bili.Error{Op: "dm_send", Code: -400}
	got := classifyBiliError(original)
	var apiErr *bili.Error
	if !errors.As(got, &apiErr) || apiErr.Code != -400 {
		t.Fatalf("分类丢失平台错误：%v", got)
	}
	var classified *Error
	if !errors.As(got, &classified) || classified.Cause == nil ||
		!strings.Contains(classified.Cause.Error(), "私信平台错误") ||
		!strings.Contains(classified.Cause.Error(), "code -400") {
		t.Fatalf("分类文案未保留平台 code：%v", got)
	}

	local := classify(errors.New("凭据缺少bili_jct，无法执行写操作"))
	var typed *Error
	if !errors.As(local, &typed) || typed.Kind != KindAuth {
		t.Fatalf("本地缺凭据应归 auth：%v", local)
	}
	if typed.Cause == nil || !strings.Contains(typed.Cause.Error(), "bili_jct") {
		t.Fatalf("鉴权分类未保留根因：%v", local)
	}

	badInput := classify(errors.New("mid 必须是纯数字"))
	if !errors.As(badInput, &typed) || typed.Kind != KindInput {
		t.Fatalf("非法参数应归 input：%v", badInput)
	}
}

// TestNewValidatesWhitelist 护栏模式下白名单必须非空且为数字。
func TestNewValidatesWhitelist(t *testing.T) {
	tr, rel := &fakeTransport{}, &fakeRelation{}
	if _, err := New(tr, rel, Options{AllFollowers: false}); err == nil {
		t.Error("空白名单应报错")
	}
	if _, err := New(tr, rel, Options{AllowedMIDs: []string{"abc"}}); err == nil {
		t.Error("非数字 mid 应报错")
	}
	if _, err := New(nil, rel, Options{AllowedMIDs: []string{"1"}}); err == nil {
		t.Error("nil 传输层应报错")
	}
	if _, err := New(tr, nil, Options{AllowedMIDs: []string{"1"}}); err == nil {
		t.Error("nil 关系检查应报错")
	}
	if _, err := New(tr, rel, Options{AllFollowers: true}); err != nil {
		t.Errorf("all_followers 模式应允许空白名单：%v", err)
	}
}

// TestSendAllFollowersAllowsAnyone 所有发信人可回：跳过白名单/互关/日收信人/限频。
func TestSendAllFollowersAllowsAnyone(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	tr := &fakeTransport{}
	rel := &fakeRelation{attribute: 2}
	s, err := New(tr, rel, Options{AllFollowers: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for i, peer := range []string{"9001", "9002", "9003"} {
		res, err := s.Send(context.Background(), claimFor(peer), sender.Reply{Text: "你好"})
		if err != nil {
			t.Fatalf("第 %d 个发信人应放行：%v", i+1, err)
		}
		if !res.OK {
			t.Errorf("第 %d 个发信人结果未标记成功：%+v", i+1, res)
		}
	}
	if tr.sent != 3 {
		t.Fatalf("应连续发出 3 条，实际 %d", tr.sent)
	}
	if tr.lastID != "9003" {
		t.Errorf("最后收信人 = %q，期望 9003", tr.lastID)
	}
}

// TestResumeNotRetried 私信不做重投。
func TestResumeNotRetried(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, _, _ := newSender(t, &now)
	if _, err := s.Resume(context.Background(), claimFor("313591854")); err == nil {
		t.Error("Resume 应返回错误（不重投）")
	}
}

// TestSetAllowedMIDsHotUpdate 面板热更新：新名单立即生效，旧名单成员被拒。
func TestSetAllowedMIDsHotUpdate(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, _, _ := newSender(t, &now)

	if err := s.SetAllowedMIDs([]string{"888", "999"}); err != nil {
		t.Fatalf("更新白名单失败：%v", err)
	}
	if _, err := s.Send(context.Background(), claimFor("777"), sender.Reply{Text: "你好"}); err == nil {
		t.Error("旧白名单成员应被拒绝")
	}
	if _, err := s.Send(context.Background(), claimFor("888"), sender.Reply{Text: "你好"}); err != nil {
		t.Errorf("新白名单成员应放行：%v", err)
	}

	if err := s.SetAllowedMIDs(nil); err == nil {
		t.Error("空名单应拒绝")
	}
	if err := s.SetAllowedMIDs([]string{"abc"}); err == nil {
		t.Error("非数字 mid 应拒绝")
	}
	if !s.allowedMID("888") {
		t.Error("非法更新不应破坏原名单")
	}
}
