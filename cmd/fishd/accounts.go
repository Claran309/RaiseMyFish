package main

// 本文件装配 B站账号服务，并把「DB 活跃账号 → 环境变量」解析链接到静态配置与动态客户端。
//
// 边界：
//   - 只记来源/账号 id/mid，绝不打印 cookie/csrf；
//   - 数据库读失败时保持环境变量静态凭据，不阻断启动；
//   - 静态消费方（Node 送礼、截帧）在启动时回填；Go 客户端每次请求动态解析。

import (
	"context"
	"errors"
	"strings"
	"time"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const accountVerifyTimeout = 10 * time.Second

// biliVerifier 用一次性 bili 客户端确认 cookie 对应的 B站身份。
type biliVerifier struct{ timeout time.Duration }

func (v biliVerifier) Verify(ctx context.Context, cookie string) (account.Identity, error) {
	client, err := bili.New(bili.Options{Cookie: cookie, Timeout: v.timeout})
	if err != nil {
		return account.Identity{}, err
	}
	info, err := client.Nav(ctx)
	if err != nil {
		return account.Identity{}, err
	}
	return account.Identity{MID: info.MID, Name: info.Name}, nil
}

// buildAccountService 装配账号服务；环境变量作为解析链兜底。
func buildAccountService(cfg config.Config, db *gorm.DB, log *zap.Logger) (*account.Service, error) {
	repo, err := database.NewAccountRepo(db)
	if err != nil {
		return nil, err
	}
	svc, err := account.NewService(account.Options{
		Store:         repo,
		Verifier:      biliVerifier{timeout: accountVerifyTimeout},
		EnvCookie:     cfg.Collector.Live.Cookie,
		EnvCookieFile: cfg.Collector.Live.CookieFile,
		// 环境凭据的 UID 来自静态配置，旧计划恢复时才能按 MID 找回它。
		EnvMID: selfMID(cfg),
	})
	if err != nil {
		return nil, err
	}
	log.Info("B站账号服务已装配",
		zap.String("action", "account_setup"),
		zap.Bool("env_fallback", cfg.Collector.Live.Cookie != "" || strings.TrimSpace(cfg.Collector.Live.CookieFile) != ""),
	)
	return svc, nil
}

// credentialResolver 是账号服务里装配层需要的最小能力，便于测试注入。
type credentialResolver interface {
	Resolve(ctx context.Context) (account.Credential, error)
}

// applyActiveAccount 应用解析链结果：
//   - DB 活跃账号：回填静态配置，并注入动态 Resolver；
//   - 环境兜底：只注入 Resolver，静态配置保持原样；
//   - 数据库读失败：保留环境静态凭据，不注入 Resolver。
func applyActiveAccount(ctx context.Context, cfg *config.Config, svc credentialResolver, log *zap.Logger) {
	cred, err := svc.Resolve(ctx)
	switch {
	case err == nil && cred.Source == account.SourceDB:
		cfg.Collector.Live.Cookie = cred.Cookie
		cfg.Collector.Live.CookieFile = ""
		cfg.Watch.Cookie = cred.Cookie
		cfg.Gift.Cookie = cred.Cookie
		cfg.Gift.CookieFile = ""
		if cred.MID != "" {
			cfg.Collector.Live.SelfID = cred.MID
			if cfg.Sender != nil {
				cfg.Sender.SelfID = cred.MID
			}
		}
		cfg.BiliResolver = accountResolver(svc)
		log.Info("使用数据库活跃账号",
			zap.String("action", "account_resolve"),
			zap.String("source", cred.Source),
			zap.String("account_id", cred.AccountID),
			zap.String("mid", cred.MID),
		)
	case err == nil:
		cfg.BiliResolver = accountResolver(svc)
		log.Info("使用环境变量B站凭据", zap.String("action", "account_resolve"), zap.String("source", cred.Source))
	case errors.Is(err, account.ErrNoCredential):
		cfg.BiliResolver = accountResolver(svc)
		log.Info("没有可用的B站凭据", zap.String("action", "account_resolve"), zap.String("reason", "no_credential"))
	default:
		log.Warn("账号凭据解析失败，继续使用环境变量", zap.String("action", "account_resolve"), zap.Error(err))
	}
}

// accountResolver 把服务适配成 bili.Options.Resolver；无凭据时映射为 bili 的哨兵错误。
func accountResolver(svc credentialResolver) func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		cred, err := svc.Resolve(ctx)
		if errors.Is(err, account.ErrNoCredential) {
			return "", "", bili.ErrNoCredential
		}
		if err != nil {
			return "", "", err
		}
		return cred.Cookie, cred.MID, nil
	}
}
