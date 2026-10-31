// Package store 定义持久化接口，不依赖数据库实现。
package store

import (
	"RaiseMyFish/internal/event"
	"context"
	"errors"
	"math"
	"strings"
	"time"
)

// Usage 保存一次实际Provider调用结果；nil明确表示未知。
type Usage struct {
	At           time.Time
	Channel      event.Channel
	Model        string
	InputTokens  *int
	OutputTokens *int
	CostUSD      *float64
	Milliseconds int
	OK           bool
}

// UsageRepo 是Brain唯一需要的账本接口。
type UsageRepo interface {
	RecordUsage(context.Context, Usage) error
}

// Validate 在SQL之前检查数字范围，不把未知数据改成0。
func (u Usage) Validate() error {
	if u.At.IsZero() || !u.Channel.Valid() || strings.TrimSpace(u.Model) == "" || u.Milliseconds < 0 {
		return errors.New("LLM用量基本字段无效")
	}
	for _, n := range []*int{u.InputTokens, u.OutputTokens} {
		if n != nil && (*n < 0 || int64(*n) > 2147483647) {
			return errors.New("LLM token计数无效")
		}
	}
	if u.CostUSD != nil && (math.IsNaN(*u.CostUSD) || math.IsInf(*u.CostUSD, 0) || *u.CostUSD < 0 || *u.CostUSD > 9999.999999) {
		return errors.New("LLM费用无效或超出账本精度")
	}
	if int64(u.Milliseconds) > 2147483647 {
		return errors.New("LLM耗时超出账本范围")
	}
	return nil
}
