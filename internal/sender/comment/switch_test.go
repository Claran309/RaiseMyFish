package comment

// S09/T18：评论发送优先使用上下文里的账号快照，切号后旧尝试不得改用新号。

import (
	"context"
	"testing"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/sender"
)

func TestSendUsesActorSnapshot(t *testing.T) {
	fixture, srv := newSendServer(t)
	// 静态装配已是新号；旧尝试通过上下文携带旧账号快照。
	s := newService(t, srv.URL)
	old := bili.Actor{
		AccountID: "account-old", MID: "111",
		Cookie: "SESSDATA=old-secret; bili_jct=old-csrf", CSRF: "old-csrf", Source: "db",
	}
	ctx := bili.WithActor(context.Background(), old)

	res, err := s.Send(ctx, testClaim(), testReply("旧号回复"))
	if err != nil || !res.OK {
		t.Fatalf("发送失败：%+v %v", res, err)
	}
	fixture.mu.Lock()
	cookie, csrf := fixture.cookie, fixture.form.Get("csrf")
	fixture.mu.Unlock()
	if cookie != old.Cookie {
		t.Fatalf("POST 未使用旧账号凭据：%q", cookie)
	}
	if csrf != "old-csrf" {
		t.Fatalf("csrf 未使用旧账号快照：%q", csrf)
	}
}

func TestSendActorMissingCredentialNeverPosts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		actor bili.Actor
	}{
		{"empty_cookie", bili.Actor{AccountID: "account-old", MID: "111"}},
		{"no_csrf", bili.Actor{AccountID: "account-old", MID: "111", Cookie: "SESSDATA=old-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, srv := newSendServer(t)
			s := newService(t, srv.URL)
			_, err := s.Send(bili.WithActor(context.Background(), tc.actor), testClaim(), testReply("你好"))
			if kindOf(t, err) != sender.KindAuth {
				t.Fatalf("kind=%s want auth", kindOf(t, err))
			}
			fixture.mu.Lock()
			calls := fixture.calls
			fixture.mu.Unlock()
			if calls != 0 {
				t.Fatalf("快照缺凭据仍然发起了 %d 次请求", calls)
			}
		})
	}
}
