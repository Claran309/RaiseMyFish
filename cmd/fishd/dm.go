package main

// 本文件装配私信通道：采集器 + 发送器 + 通道注册。
//
// 默认关（`[collector.dm]`/`[sender.dm]` 都不开时不装任何东西）；
// 缺 cookie 时不装配采集/发送，只记日志「未配置凭据」。

import (
	"strings"
	"time"

	"RaiseMyFish/internal/bili"
	dmcollector "RaiseMyFish/internal/collector/dm"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/sender"
	senderdm "RaiseMyFish/internal/sender/dm"
	"go.uber.org/zap"
)

// buildBiliClientForDM 复用 bili 包的 cookie/csrf 装配；无凭据返回 nil（不报错）。
func buildBiliClientForDM(cfg config.Config, log *zap.Logger) *bili.Client {
	cookie := cfg.Collector.Live.Cookie
	if strings.TrimSpace(cookie) == "" && strings.TrimSpace(cfg.Collector.Live.CookieFile) == "" {
		log.Info("私信通道未装配：未配置凭据", zap.String("action", "dm_setup"), zap.String("reason", "no_cookie"))
		return nil
	}
	client, err := bili.New(bili.Options{Cookie: cookie, CookieFile: cfg.Collector.Live.CookieFile, SelfMID: selfMID(cfg), Timeout: 10 * time.Second})
	if err != nil {
		log.Warn("私信通道未装配：凭据不可用", zap.String("action", "dm_setup"), zap.String("reason", "bad_cookie"), zap.Error(err))
		return nil
	}
	return client
}

// buildDMCollector 只在 [collector.dm] enabled 时装配。
func buildDMCollector(cfg config.Config, log *zap.Logger) (*dmcollector.Collector, error) {
	if !cfg.Collector.DM.Enabled {
		return nil, nil
	}
	client := buildBiliClientForDM(cfg, log)
	if client == nil {
		return nil, nil
	}
	opts := dmcollector.Options{
		Interval: time.Duration(cfg.Collector.DM.IntervalSeconds) * time.Second,
		Log:      log,
	}
	return dmcollector.New(dmcollector.NewBiliFetcher(client, selfMID(cfg), 20), opts)
}

// buildDMSender 只在 [sender.dm] enabled 时装配；未装配返回 nil（不报错）。
func buildDMSender(cfg config.Config, log *zap.Logger) (sender.Sender, error) {
	if !cfg.Sender.DM.Enabled {
		return nil, nil
	}
	client := buildBiliClientForDM(cfg, log)
	if client == nil {
		return nil, nil
	}
	if !client.HasCSRF() {
		log.Warn("私信发送未装配：凭据缺少 bili_jct", zap.String("action", "dm_setup"), zap.String("reason", "no_csrf"))
		return nil, nil
	}
	allowed := make([]string, 0, len(cfg.Sender.DM.AllowedMIDs)+1)
	allowed = append(allowed, cfg.Sender.DM.AllowedMIDs...)
	// 主人始终在白名单里（去重交给 New 内部 map）
	if owner := ownerMID(cfg); owner != "" {
		allowed = append(allowed, owner)
	}
	snd, err := senderdm.New(
		senderdm.NewBiliTransporter(client),
		senderdm.NewBiliRelation(client),
		senderdm.Options{AllowedMIDs: allowed, RecipientPath: cfg.Sender.DM.StatePath},
	)
	if err != nil {
		return nil, err
	}
	log.Info("私信发送已装配",
		zap.String("action", "dm_setup"),
		zap.Int("allowed", len(allowed)),
	)
	return snd, nil
}
