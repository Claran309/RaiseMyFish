package embedding

// 本文件把 embedding 调用事实写进 llm_usage（F07）。
//
// 边界：
//   - repo 为 nil 时返回 nil sink，调用方无需判空；
//   - 通道恒为 NULL：embedding 是后台增强，不属于任何对话通道；
//   - 用途/关联标识从 context 读取；调用方没标注时 purpose=unknown，
//     不猜测成某个具体用途；
//   - 记账失败只回调 onError，不上抛、不阻塞主链路。

import (
	"context"
	"time"

	"RaiseMyFish/internal/llmmeta"
	"RaiseMyFish/internal/store"
)

// UsageSinkFor 构造写 llm_usage 的 embedding sink；model 是配置里的模型名。
func UsageSinkFor(repo store.UsageRepo, model string, onError func(error)) UsageSink {
	if repo == nil {
		return nil
	}
	return func(ctx context.Context, u Usage) {
		call := llmmeta.From(ctx)
		purpose := call.Purpose
		if purpose == "" {
			purpose = llmmeta.PurposeUnknown
		}
		row := store.Usage{
			At:                u.At,
			Purpose:           string(purpose),
			CorrelationID:     llmmeta.SafeCorrelationID(call.CorrelationID),
			Model:             model,
			InputTokens:       u.InputTokens,
			CachedInputTokens: u.CachedInputTokens,
			Milliseconds:      u.Milliseconds,
			OK:                u.OK,
		}
		// 父 ctx 可能已超时/取消；记账脱离取消，但只给 2 秒。
		accountCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := repo.RecordUsage(accountCtx, row); err != nil && onError != nil {
			onError(err)
		}
	}
}
