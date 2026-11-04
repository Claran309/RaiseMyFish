package dm

// 本文件测私信发送护栏：白名单、当日收信人锁定、限频、互关检查、错误分类。

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
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
		AllowedMIDs: []string{"313591854", "777"},
		RateLimit:   10 * time.Minute,
		Now:         func() time.Time { return *clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, tr, rel
}

func claimFor(peer string) event.Claim {
	return event.Claim{Event: event.Event{ID: "dm:" + peer + ":1", Channel: event.ChDM, Dir: event.DirIn, Session: peer, FromID: peer, Text: "在吗"}}
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

// TestNewValidatesWhitelist 白名单必须非空且为数字。
func TestNewValidatesWhitelist(t *testing.T) {
	tr, rel := &fakeTransport{}, &fakeRelation{}
	if _, err := New(tr, rel, Options{}); err == nil {
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
}

// TestResumeNotRetried 私信不做重投。
func TestResumeNotRetried(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, _, _ := newSender(t, &now)
	if _, err := s.Resume(context.Background(), claimFor("313591854")); err == nil {
		t.Error("Resume 应返回错误（不重投）")
	}
}
