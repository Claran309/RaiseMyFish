package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const testDBCookie = "SESSDATA=db-secret; bili_jct=db-csrf"

// fakeCredentialResolver 返回固定凭据，用于验证装配层行为。
type fakeCredentialResolver struct {
	cred  account.Credential
	err   error
	calls int
}

func (f *fakeCredentialResolver) Resolve(context.Context) (account.Credential, error) {
	f.calls++
	return f.cred, f.err
}

func accountTestConfig() config.Config {
	return config.Config{
		Collector: config.Collectors{Live: config.LiveCollector{
			Cookie: "SESSDATA=env-secret", CookieFile: "env-cookie.txt", SelfID: "100",
		}},
		Watch:  config.Watch{Cookie: "env-watch-cookie"},
		Gift:   config.Gift{Cookie: "env-gift-cookie", CookieFile: "gift.txt"},
		Sender: &config.Sender{SelfID: "100"},
	}
}

// TestApplyActiveAccountBackfillsDBAccount DB 活跃账号覆盖静态配置并注入 Resolver。
func TestApplyActiveAccountBackfillsDBAccount(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	resolver := &fakeCredentialResolver{cred: account.Credential{
		Cookie: testDBCookie, CSRF: "db-csrf", MID: "42", AccountID: "account-x", Source: account.SourceDB,
	}}
	cfg := accountTestConfig()
	applyActiveAccount(context.Background(), &cfg, resolver, zap.New(core))

	if cfg.Collector.Live.Cookie != testDBCookie || cfg.Collector.Live.CookieFile != "" {
		t.Errorf("直播凭据未回填：cookie=%q file=%q", cfg.Collector.Live.Cookie, cfg.Collector.Live.CookieFile)
	}
	if cfg.Collector.Live.SelfID != "42" {
		t.Errorf("采集 self_id 未回填：%q", cfg.Collector.Live.SelfID)
	}
	if cfg.Watch.Cookie != testDBCookie {
		t.Errorf("看直播凭据未回填：%q", cfg.Watch.Cookie)
	}
	if cfg.Gift.Cookie != testDBCookie || cfg.Gift.CookieFile != "" {
		t.Errorf("送礼凭据未回填：cookie=%q file=%q", cfg.Gift.Cookie, cfg.Gift.CookieFile)
	}
	if cfg.Sender == nil || cfg.Sender.SelfID != "42" {
		t.Errorf("发送 self_id 未回填：%+v", cfg.Sender)
	}
	if cfg.BiliResolver == nil {
		t.Fatal("DB 活跃账号必须注入动态 Resolver")
	}
	cookie, mid, err := cfg.BiliResolver(context.Background())
	if err != nil || cookie != testDBCookie || mid != "42" {
		t.Fatalf("Resolver 输出不符：cookie=%q mid=%q err=%v", cookie, mid, err)
	}
	for _, entry := range logs.All() {
		if strings.Contains(fmt.Sprint(entry.ContextMap()), "secret") || strings.Contains(entry.Message, "secret") {
			t.Errorf("日志泄露凭据：%s %v", entry.Message, entry.ContextMap())
		}
	}
}

// TestApplyActiveAccountEnvKeepsStatic 环境兜底时不改写静态配置，但仍注入 Resolver。
func TestApplyActiveAccountEnvKeepsStatic(t *testing.T) {
	resolver := &fakeCredentialResolver{cred: account.Credential{
		Cookie: "SESSDATA=env-only", MID: "100", Source: account.SourceEnv,
	}}
	cfg := accountTestConfig()
	applyActiveAccount(context.Background(), &cfg, resolver, zap.NewNop())

	if cfg.Collector.Live.Cookie != "SESSDATA=env-secret" || cfg.Collector.Live.CookieFile != "env-cookie.txt" {
		t.Errorf("环境兜底不应改写静态凭据：%+v", cfg.Collector.Live)
	}
	if cfg.Watch.Cookie != "env-watch-cookie" || cfg.Gift.Cookie != "env-gift-cookie" {
		t.Error("环境兜底不应改写旁路凭据")
	}
	if cfg.BiliResolver == nil {
		t.Fatal("环境兜底也必须注入 Resolver")
	}
	cookie, mid, err := cfg.BiliResolver(context.Background())
	if err != nil || cookie != "SESSDATA=env-only" || mid != "100" {
		t.Fatalf("Resolver 输出不符：cookie=%q mid=%q err=%v", cookie, mid, err)
	}
}

// TestApplyActiveAccountNoCredential 无凭据时保留启动，Resolver 在请求期报错。
func TestApplyActiveAccountNoCredential(t *testing.T) {
	resolver := &fakeCredentialResolver{err: account.ErrNoCredential}
	cfg := accountTestConfig()
	applyActiveAccount(context.Background(), &cfg, resolver, zap.NewNop())

	if cfg.BiliResolver == nil {
		t.Fatal("无凭据时也应注入 Resolver")
	}
	if _, _, err := cfg.BiliResolver(context.Background()); !errors.Is(err, bili.ErrNoCredential) {
		t.Fatalf("应映射为 bili.ErrNoCredential，实际 %v", err)
	}
}

// TestApplyActiveAccountStoreErrorKeepsEnv 数据库读失败时保持环境静态凭据，不注入 Resolver。
func TestApplyActiveAccountStoreErrorKeepsEnv(t *testing.T) {
	resolver := &fakeCredentialResolver{err: errors.New("db 挂了")}
	cfg := accountTestConfig()
	applyActiveAccount(context.Background(), &cfg, resolver, zap.NewNop())

	if cfg.BiliResolver != nil {
		t.Error("数据库读失败时不应注入 Resolver")
	}
	if cfg.Collector.Live.Cookie != "SESSDATA=env-secret" {
		t.Errorf("静态凭据被意外改写：%q", cfg.Collector.Live.Cookie)
	}
}

// TestApplyActiveAccountMakesCredentialsValid 启动顺序：回填前凭据校验失败，回填后通过。
func TestApplyActiveAccountMakesCredentialsValid(t *testing.T) {
	cfg := config.Config{
		Channels:  config.Channels{Live: config.Channel{Enabled: true}},
		Collector: config.Collectors{Live: config.LiveCollector{RoomID: "1"}},
	}
	if err := cfg.ValidateCredentials(); err == nil {
		t.Fatal("没有 DB 账号回填时不应通过凭据校验")
	}
	resolver := &fakeCredentialResolver{cred: account.Credential{
		Cookie: testDBCookie, CSRF: "db-csrf", MID: "42", AccountID: "account-x", Source: account.SourceDB,
	}}
	applyActiveAccount(context.Background(), &cfg, resolver, zap.NewNop())
	if err := cfg.ValidateCredentials(); err != nil {
		t.Fatalf("DB 活跃账号回填后应通过：%v", err)
	}
	if cfg.Collector.Live.Cookie != testDBCookie || cfg.Collector.Live.SelfID != "42" {
		t.Fatalf("回填结果不符：cookie=%q self=%q", cfg.Collector.Live.Cookie, cfg.Collector.Live.SelfID)
	}
}

// TestAccountResolverPassesThroughError 非「无凭据」错误原样透传，便于主链路观察。
func TestAccountResolverPassesThroughError(t *testing.T) {
	sentinel := errors.New("存储异常")
	resolve := accountResolver(&fakeCredentialResolver{err: sentinel})
	if _, _, err := resolve(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("应透传原错误，实际 %v", err)
	}
	resolve = accountResolver(&fakeCredentialResolver{cred: account.Credential{
		Cookie: "SESSDATA=x", MID: "7", Source: account.SourceDB,
	}})
	cookie, mid, err := resolve(context.Background())
	if err != nil || cookie != "SESSDATA=x" || mid != "7" {
		t.Fatalf("正常凭据输出不符：cookie=%q mid=%q err=%v", cookie, mid, err)
	}
}
