// Package llmmeta 定义一次模型调用的用途与安全关联标识。
//
// 本包刻意不依赖任何其他内部包：store 用它校验账本字段，
// provider/embedding 的调用方用它把用途沿 context 传下去。
package llmmeta

import (
	"context"
	"strings"
)

// Purpose 是一次调用的用途分类；账本据此区分现场回复与后台增强。
type Purpose string

const (
	// PurposeReply 是 Brain 单步生成回复。
	PurposeReply Purpose = "reply"
	// PurposeReact 是 ReAct 多步工具循环里的模型调用。
	PurposeReact Purpose = "react"
	// PurposeVision 是看直播的画面理解。
	PurposeVision Purpose = "vision"
	// PurposeMemoryExtract 是记忆提炼。
	PurposeMemoryExtract Purpose = "memory_extract"
	// PurposeMemeLearn 是梗白学习。
	PurposeMemeLearn Purpose = "meme_learn"
	// PurposeViewerSummary 是观众侧写定时总结。
	PurposeViewerSummary Purpose = "viewer_summary"
	// PurposeDailySummary 是每日总结（日报生成）。
	PurposeDailySummary Purpose = "daily_summary"
	// PurposeEmbedIndex 是写索引的批量向量化。
	PurposeEmbedIndex Purpose = "embed_index"
	// PurposeEmbedQuery 是查询时的单条向量化。
	PurposeEmbedQuery Purpose = "embed_query"
	// PurposeUnknown 用于调用方未标注用途的 embedding 尝试；
	// 保留 unknown 而不是猜测成某个具体用途。
	PurposeUnknown Purpose = "unknown"
)

// knownPurposes 是账本接受的用途集合。
var knownPurposes = map[Purpose]bool{
	PurposeReply:         true,
	PurposeReact:         true,
	PurposeVision:        true,
	PurposeMemoryExtract: true,
	PurposeMemeLearn:     true,
	PurposeViewerSummary: true,
	PurposeDailySummary:  true,
	PurposeEmbedIndex:    true,
	PurposeEmbedQuery:    true,
	PurposeUnknown:       true,
}

// ChannelBound 判断该用途是否必须归属某个对话通道。
func ChannelBound(p Purpose) bool {
	switch p {
	case PurposeReply, PurposeReact, PurposeVision:
		return true
	default:
		return false
	}
}

// KnownPurpose 判断用途是否在账本约定内。
func KnownPurpose(p string) bool {
	return knownPurposes[Purpose(p)]
}

// Call 是一次调用的元数据；CorrelationID 只能是安全标识（如事件 ID），不放正文。
type Call struct {
	Purpose       Purpose
	CorrelationID string
}

type contextKey struct{}

// WithCall 把调用元数据放进 context；已有字段不会被空值覆盖。
func WithCall(ctx context.Context, call Call) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	current := From(ctx)
	if call.Purpose != "" {
		current.Purpose = call.Purpose
	}
	if call.CorrelationID != "" {
		current.CorrelationID = call.CorrelationID
	}
	return context.WithValue(ctx, contextKey{}, current)
}

// From 读取调用元数据；没有标注时返回零值。
func From(ctx context.Context) Call {
	if ctx == nil {
		return Call{}
	}
	call, _ := ctx.Value(contextKey{}).(Call)
	return call
}

// SafeCorrelationID 只接受可打印 ASCII 标识（事件 ID 等）。
//
// 昵称、弹幕正文、URL 等一律返回空串：账本不需要也不允许保存它们。
// 超长标识同样丢弃，避免把未知格式的数据带进账本。
func SafeCorrelationID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 128 {
		return ""
	}
	for _, r := range raw {
		if r < 0x21 || r > 0x7e {
			return ""
		}
	}
	return raw
}
