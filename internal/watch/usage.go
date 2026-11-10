package watch

// 视觉调用的账本适配（O08）。
//
// 这里只做「把一次视觉调用的 usage 写进 store.UsageRepo」，
// 费用算法复用 store.Cost —— 保证 Watch / Proactive / MCP 三条路径同口径。
//
// 边界：
//   - usage repo 为 nil 时返回 nil sink（调用方无需判空，视觉照常可用）；
//   - 记账失败只 Warn，不影响看直播/主动弹幕；
//   - 通道固定 live：视觉请求只可能来自直播链路。

import (
	"context"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// UsageSinkFor 构造写入 llm_usage 的 sink；price 的两个指针可为 nil（→ 费用 unknown）。
func UsageSinkFor(usage store.UsageRepo, model string, inUSD, outUSD *float64, logger *zap.Logger) UsageSink {
	if usage == nil {
		return nil
	}
	return func(ctx context.Context, u VisionUsage, ok bool, elapsed time.Duration) {
		row := store.Usage{
			At:           time.Now().UTC(),
			Channel:      event.ChLive,
			Purpose:      string(llmmeta.PurposeVision),
			Model:        model,
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			CostUSD:      store.Cost(u.InputTokens, u.OutputTokens, inUSD, outUSD),
			Milliseconds: int(elapsed.Milliseconds()),
			OK:           ok,
		}
		// 父 ctx 可能已超时/取消；记账脱离取消，但只给 2 秒。
		accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := usage.RecordUsage(accountCtx, row); err != nil && logger != nil {
			logger.Warn("视觉调用记账失败",
				zap.String("action", "vision_usage"),
				zap.String("result", "failed"),
				zap.String("model", model),
				zap.Bool("ok", ok),
				zap.Int("cost_ms", row.Milliseconds),
				zap.Error(err),
			)
		}
	}
}
