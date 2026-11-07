package dm

// S09/T18：私信发送/关系查询优先使用上下文里的账号快照，
// 切号后旧尝试仍以旧号 sender_uid 与旧凭据发出。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/bili"
)

type dmHTTPFixture struct {
	mu     sync.Mutex
	calls  int
	cookie string
	form   url.Values
}

func newDMBiliClient(t *testing.T, fixture *dmHTTPFixture) *bili.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fixture.mu.Lock()
		fixture.calls++
		fixture.cookie = r.Header.Get("Cookie")
		fixture.form = r.PostForm
		fixture.mu.Unlock()
		switch r.URL.Path {
		case "/web_im/v1/web_im/send_msg":
			_, _ = w.Write([]byte(`{"code":0,"data":{"msg_key":"mk-1"}}`))
		case "/x/relation":
			_, _ = w.Write([]byte(`{"code":0,"data":{"attribute":6}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	// 底层客户端静态凭据是新号；旧尝试必须靠上下文快照覆盖。
	client, err := bili.New(bili.Options{
		APIRoot: srv.URL, LiveRoot: srv.URL, VCRoot: srv.URL,
		Cookie:  "SESSDATA=new-secret; bili_jct=csrf-new",
		SelfMID: "222",
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("构造 bili 客户端失败：%v", err)
	}
	return client
}

func oldActor() bili.Actor {
	return bili.Actor{
		AccountID: "account-old", MID: "111",
		Cookie: "SESSDATA=old-secret; bili_jct=csrf-old", CSRF: "csrf-old", Source: "db",
	}
}

func TestSendDMUsesActorSnapshot(t *testing.T) {
	fixture := &dmHTTPFixture{}
	transporter := NewBiliTransporter(newDMBiliClient(t, fixture))
	ctx := bili.WithActor(context.Background(), oldActor())

	msgID, err := transporter.SendDM(ctx, "2002", "旧号回复")
	if err != nil || msgID != "mk-1" {
		t.Fatalf("发送失败：%q %v", msgID, err)
	}
	fixture.mu.Lock()
	cookie, form, calls := fixture.cookie, fixture.form, fixture.calls
	fixture.mu.Unlock()
	if calls != 1 {
		t.Fatalf("应只发一次请求：%d", calls)
	}
	if cookie != oldActor().Cookie {
		t.Fatalf("POST 未使用旧账号凭据：%q", cookie)
	}
	if form.Get("msg[sender_uid]") != "111" {
		t.Fatalf("sender_uid 未绑定旧号：%q", form.Get("msg[sender_uid]"))
	}
	if form.Get("csrf") != "csrf-old" {
		t.Fatalf("csrf 未使用旧账号快照：%q", form.Get("csrf"))
	}
}

func TestSendDMWithoutActorKeepsActiveAccount(t *testing.T) {
	fixture := &dmHTTPFixture{}
	transporter := NewBiliTransporter(newDMBiliClient(t, fixture))

	if _, err := transporter.SendDM(context.Background(), "2002", "当前账号回复"); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	form := fixture.form
	fixture.mu.Unlock()
	if form.Get("msg[sender_uid]") != "222" {
		t.Fatalf("无快照时应使用当前账号：%q", form.Get("msg[sender_uid]"))
	}
}

func TestSendDMInvalidActorNeverPosts(t *testing.T) {
	fixture := &dmHTTPFixture{}
	transporter := NewBiliTransporter(newDMBiliClient(t, fixture))
	ctx := bili.WithActor(context.Background(), bili.Actor{AccountID: "account-old", MID: "111"})

	_, err := transporter.SendDM(ctx, "2002", "正文")
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindAuth {
		t.Fatalf("空凭据快照应归为鉴权错误：%v", err)
	}
	fixture.mu.Lock()
	calls := fixture.calls
	fixture.mu.Unlock()
	if calls != 0 {
		t.Fatalf("快照无效仍然发起了 %d 次请求", calls)
	}
}

func TestRelationUsesActorSnapshot(t *testing.T) {
	fixture := &dmHTTPFixture{}
	relation := NewBiliRelation(newDMBiliClient(t, fixture))
	ctx := bili.WithActor(context.Background(), oldActor())

	attribute, err := relation.Attribute(ctx, "2002")
	if err != nil || attribute != 6 {
		t.Fatalf("关系查询失败：%d %v", attribute, err)
	}
	fixture.mu.Lock()
	cookie := fixture.cookie
	fixture.mu.Unlock()
	if cookie != oldActor().Cookie {
		t.Fatalf("关系查询未使用旧账号凭据：%q", cookie)
	}
}
