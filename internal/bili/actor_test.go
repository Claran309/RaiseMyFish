package bili

// 本文件测账号快照：动态解析客户端在操作开始时冻结凭据，
// Bind 之后的请求不再跟随解析器切号。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type actorCookieLog struct {
	mu      sync.Mutex
	cookies []string
}

func (l *actorCookieLog) record(cookie string) {
	l.mu.Lock()
	l.cookies = append(l.cookies, cookie)
	l.mu.Unlock()
}

func (l *actorCookieLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.cookies...)
}

func TestBindFreezesResolvedAccount(t *testing.T) {
	log := &actorCookieLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.Header.Get("Cookie"))
		_, _ = w.Write([]byte(`{"code":0,"data":{"session_list":[]}}`))
	}))
	t.Cleanup(srv.Close)

	var mu sync.Mutex
	current := "SESSDATA=old; bili_jct=csrf-old"
	resolver := func(context.Context) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		if current == "SESSDATA=old; bili_jct=csrf-old" {
			return current, "111", nil
		}
		return current, "222", nil
	}
	client, err := New(Options{VCRoot: srv.URL, Resolver: resolver, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MID != "111" || snapshot.Cookie != "SESSDATA=old; bili_jct=csrf-old" || !snapshot.Identified() {
		t.Fatalf("快照不符：%+v", snapshot)
	}
	bound, err := client.Bind(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	current = "SESSDATA=new; bili_jct=csrf-new"
	mu.Unlock()

	if _, err := bound.DMSessionsPage(context.Background(), 20, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DMSessionsPage(context.Background(), 20, 0); err != nil {
		t.Fatal(err)
	}
	cookies := log.all()
	if len(cookies) != 2 || cookies[0] != "SESSDATA=old; bili_jct=csrf-old" || cookies[1] != "SESSDATA=new; bili_jct=csrf-new" {
		t.Fatalf("绑定客户端未冻结旧凭据：%v", cookies)
	}
}

func TestActorContextRoundTrip(t *testing.T) {
	want := Actor{AccountID: "account-1", MID: "111", Cookie: "SESSDATA=x; bili_jct=y", CSRF: "y"}
	ctx := WithActor(context.Background(), want)
	got, ok := ActorFrom(ctx)
	if !ok || got != want {
		t.Fatalf("上下文快照不符：%+v %v", got, ok)
	}
	if _, ok := ActorFrom(context.Background()); ok {
		t.Fatal("空上下文不应有快照")
	}
	if invalid := (Actor{MID: "111"}); invalid.Valid() || invalid.Identified() {
		t.Fatal("空 cookie 不应视为有效快照")
	}
	if bad := (Actor{MID: "abc", Cookie: "SESSDATA=x"}); bad.Identified() {
		t.Fatal("非数字 MID 不应带身份")
	}
}
