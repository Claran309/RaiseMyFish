package main

// 本文件把 ReAct 工具的 nil 执行器换成真实 B站客户端。
//
// 风控底线：
//   - 无 cookie 时不装配（保持 nil → 工具回「未配置」），不报错、不打凭据；
//   - 限频/额度/白名单护栏在工具层，这里只管「怎么调平台」；
//   - 失败不重试；
//   - 日志只记「装了哪些/缺哪些」，不记 cookie 与正文。

import (
	"context"
	"path/filepath"
	"time"

	"RaiseMyFish/internal/bili"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/gift"
	"RaiseMyFish/internal/proactive"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/watch"
	"RaiseMyFish/internal/webtools"
	"go.uber.org/zap"
)

// toolExecutors 是 ReAct 工具的真实执行器集合；缺凭据时字段为 nil（工具回「未配置」）。
type toolExecutors struct {
	Live      reasoning.LiveStatusSource
	Social    reasoning.SocialExecutor
	Gift      reasoning.GiftExecutor
	Frame     reasoning.FrameSource
	FrameRead reasoning.FrameReader
	Search    reasoning.WebSearcher
}

// buildToolExecutors 按环境装配执行器；任何一项装配失败只降级该项。
func buildToolExecutors(cfg config.Config, log *zap.Logger) toolExecutors {
	var execs toolExecutors
	// 搜索不依赖B站凭据：先装配，保证无 cookie 时也能联网查证。
	if cfg.Web.SearchURL != "" {
		searcher, err := webtools.NewHTTP(webtools.Options{URL: cfg.Web.SearchURL, Key: cfg.Web.SearchKey})
		if err != nil {
			log.Warn("搜索执行器未装配",
				zap.String("action", "tool_executor_setup"),
				zap.String("reason", "bad_search_url"),
				zap.Error(err),
			)
		} else {
			execs.Search = searcher
		}
	}

	cookie := cfg.Collector.Live.Cookie
	if cookie == "" {
		log.Info("工具执行器未装配：未配置凭据",
			zap.String("action", "tool_executor_setup"),
			zap.String("reason", "no_cookie"),
			zap.Bool("web_search", execs.Search != nil),
		)
		return execs
	}

	client, err := bili.New(bili.Options{
		Cookie:  cookie,
		SelfMID: selfMID(cfg),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		log.Warn("工具执行器未装配：凭据不可用",
			zap.String("action", "tool_executor_setup"),
			zap.String("reason", "bad_cookie"),
			zap.Error(err),
		)
		return execs
	}
	execs.Live = client
	if client.HasCSRF() {
		// 写操作需要 bili_jct；缺它就只保留只读能力
		execs.Social = client
	} else {
		log.Warn("工具执行器缺少 bili_jct，写操作不可用",
			zap.String("action", "tool_executor_setup"),
			zap.String("reason", "no_csrf"),
		)
	}

	// 送礼复用既有 gift.NodeSender（dry-run/real 由 [gift] 决定）
	if sender, err := buildGiftSender(cfg); err == nil {
		execs.Gift = giftExecutor{sender: sender}
	} else {
		log.Info("送礼执行器未装配", zap.String("action", "tool_executor_setup"), zap.String("reason", err.Error()))
	}

	// 画面：复用 watch 的截帧 + 视觉描述
	recorder, err := watch.NewStreamRecorder(watch.StreamRecorderOptions{
		RoomID:     cfg.Collector.Live.RoomID,
		Cookie:     cfg.Watch.Cookie,
		Quality:    cfg.Watch.StreamQuality,
		FFmpegPath: cfg.Watch.FFmpegPath,
		TempDir:    cfg.Watch.TempDir,
	})
	if err == nil {
		execs.Frame = recorder
		if cfg.LLM.Enabled {
			if _, provider, model, err := cfg.LLM.Resolve(cfg.Watch.LLMRoute); err == nil {
				if describer, err := watch.NewOpenAIDescriber(provider.BaseURL, provider.APIKey, model, nil, 0); err == nil {
					execs.FrameRead = frameReader{inner: describer}
				}
			}
		}
	}

	log.Info("工具执行器装配完成",
		zap.String("action", "tool_executor_setup"),
		zap.Bool("live_status", execs.Live != nil),
		zap.Bool("social", execs.Social != nil),
		zap.Bool("gift", execs.Gift != nil),
		zap.Bool("vision", execs.Frame != nil && execs.FrameRead != nil),
		zap.Bool("web_search", execs.Search != nil),
	)
	return execs
}

// buildGiftSender 用现有 gift 配置装配 NodeSender。
//
// 与 fishctl 的映射保持一致：账本落在 runtime.state_file 同目录，dry-run 由 [gift] real 决定。
func buildGiftSender(cfg config.Config) (*gift.NodeSender, error) {
	quota, err := gift.NewFileQuota(filepath.Join(filepath.Dir(cfg.Runtime.StateFile), "gift-ledger.json"))
	if err != nil {
		return nil, err
	}
	return gift.New(gift.Options{
		NodePath:     cfg.Gift.NodePath,
		ScriptPath:   cfg.Gift.ScriptPath,
		Cookie:       cfg.Gift.Cookie,
		CookieFile:   cfg.Gift.CookieFile,
		Timeout:      time.Duration(cfg.Gift.TimeoutMS) * time.Millisecond,
		DryRun:       !cfg.Gift.Real,
		DailyLimit:   cfg.Gift.DailyLimit,
		MaxPrice:     cfg.Gift.MaxPrice,
		MaxNum:       cfg.Gift.MaxNum,
		AllowedRooms: cfg.Gift.AllowedRooms,
		Quota:        quota,
	})
}

// giftExecutor 把 gift.NodeSender 适配成 reasoning.GiftExecutor。
type giftExecutor struct{ sender *gift.NodeSender }

func (g giftExecutor) SendGift(ctx context.Context, req reasoning.GiftRequest) (string, error) {
	res, err := g.sender.Send(ctx, req.Room, req.GiftID, req.Num)
	if err != nil {
		return "", err
	}
	return res.GiftID + "×" + itoa(res.Num), nil
}

// frameReader 把 watch 的多帧描述器适配成 reasoning.FrameReader（单帧 + 详细识图词）。
type frameReader struct{ inner *watch.OpenAIDescriber }

func (f frameReader) Describe(ctx context.Context, framePath string) (string, error) {
	return f.inner.Describe(ctx, []string{framePath}, proactive.VisionPrompt)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
