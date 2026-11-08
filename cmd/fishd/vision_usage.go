package main

// 本文件是视觉调用记账的装配辅助（O08）。
//
// 真正的记账实现在 internal/watch.UsageSinkFor（Watch / Proactive / MCP 共用），
// 这里只负责把模型路由的报价挑出来 —— 没配报价就传 nil，费用记 unknown。

import (
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
)

// visionSink 为指定模型构造视觉记账 sink；usage 为 nil 时返回 nil（不计账）。
func visionSink(usage store.UsageRepo, model string, provider config.Provider, logger *zap.Logger) watch.UsageSink {
	price := modelPrice(provider, model)
	return watch.UsageSinkFor(usage, model, price.InputUSDPerMillion, price.OutputUSDPerMillion, logger)
}

// modelPrice 取该模型的报价；未配置时返回零值（两个指针都是 nil → 费用记 unknown）。
func modelPrice(provider config.Provider, model string) config.Price {
	if provider.Prices == nil {
		return config.Price{}
	}
	return provider.Prices[model]
}
