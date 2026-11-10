// Package store 定义持久化接口，不依赖数据库实现。
package store

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/llmmeta"
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

// Usage 保存一次实际Provider调用结果；nil明确表示未知。
//
// Channel 为空表示这次调用不属于任何对话通道（后台提炼、梗白学习、
// embedding 索引/查询），落库为 NULL；现场回复/ReAct/视觉必须带通道。
// Purpose 是稳定用途分类；CorrelationID 只放事件 ID 等安全标识。
type Usage struct {
	At      time.Time
	Channel event.Channel
	Purpose string
	Model   string
	// CorrelationID 为事件 ID 等可关联标识；没有或格式不安全时为空。
	CorrelationID     string
	InputTokens       *int
	OutputTokens      *int
	CachedInputTokens *int
	CostUSD           *float64
	Milliseconds      int
	OK                bool
}

// UsageRepo 是Brain唯一需要的账本接口。
type UsageRepo interface {
	RecordUsage(context.Context, Usage) error
}

// Cost 按「输入/输出每百万 token 单价」算一次调用的费用。
//
// 缺 token 或缺报价就是 nil（unknown），绝不编造 0 —— 与 Brain 的口径一致。
// 结果四舍五入到 1e-6（llm_usage.cost_usd 是 NUMERIC(10,6)）。
func Cost(in, out *int, inUSD, outUSD *float64) *float64 {
	if in == nil || out == nil || inUSD == nil || outUSD == nil {
		return nil
	}
	v := math.Round((float64(*in)**inUSD + float64(*out)**outUSD)) / 1e6
	return &v
}

// Validate 在SQL之前检查数字范围，不把未知数据改成0。
func (u Usage) Validate() error {
	if u.At.IsZero() || strings.TrimSpace(u.Model) == "" || u.Milliseconds < 0 {
		return errors.New("LLM用量基本字段无效")
	}
	if !llmmeta.KnownPurpose(u.Purpose) {
		return errors.New("LLM调用用途无效")
	}
	if u.Channel == "" {
		if llmmeta.ChannelBound(llmmeta.Purpose(u.Purpose)) {
			return errors.New("该用途必须带通道")
		}
	} else if !u.Channel.Valid() {
		return errors.New("LLM用量通道无效")
	}
	if u.CorrelationID != "" && llmmeta.SafeCorrelationID(u.CorrelationID) != u.CorrelationID {
		return errors.New("LLM调用关联标识不安全")
	}
	for _, n := range []*int{u.InputTokens, u.OutputTokens, u.CachedInputTokens} {
		if n != nil && (*n < 0 || int64(*n) > 2147483647) {
			return errors.New("LLM token计数无效")
		}
	}
	if u.CachedInputTokens != nil && u.InputTokens != nil && *u.CachedInputTokens > *u.InputTokens {
		return errors.New("缓存命中token超过输入token")
	}
	if u.CostUSD != nil && (math.IsNaN(*u.CostUSD) || math.IsInf(*u.CostUSD, 0) || *u.CostUSD < 0 || *u.CostUSD > 9999.999999) {
		return errors.New("LLM费用无效或超出账本精度")
	}
	if int64(u.Milliseconds) > 2147483647 {
		return errors.New("LLM耗时超出账本范围")
	}
	return nil
}
