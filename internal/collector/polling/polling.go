// Package polling 提供轮询间隔的自适应与抖动策略。
//
// 目的：评论/私信采集在 412/429 时不能继续按固定间隔撞墙；
// 空闲时可以放慢，活跃时保持快节奏。策略是纯函数，便于测试与回放。
package polling

import (
	"math/rand"
	"time"
)

// Policy 是轮询节奏参数。
type Policy struct {
	// Min 是最快间隔（活跃期）。
	Min time.Duration
	// Max 是最慢间隔（空闲期）。
	Max time.Duration
	// Jitter 是 ±抖动比例（0..0.5），避免多实例同相位。
	Jitter float64
	// Backoff 是 412/429 时的指数退避上限倍数。
	BackoffFactor float64
}

// Resolve 补默认值并夹紧非法输入。
func (p Policy) Resolve() Policy {
	if p.Min <= 0 {
		p.Min = 20 * time.Second
	}
	if p.Max <= 0 || p.Max < p.Min {
		p.Max = p.Min * 3
	}
	if p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.Jitter > 0.5 {
		p.Jitter = 0.5
	}
	if p.BackoffFactor < 1 {
		p.BackoffFactor = 2
	}
	return p
}

// Next 根据本轮是否有新事件与是否被限流，返回下一次间隔。
//
// active=true 用 Min，idle 用 Max；rateLimited=true 时按 BackoffFactor 指数退避，
// 但不超过 Max×BackoffFactor。jitter 由调用方注入随机源，便于测试确定性。
func (p Policy) Next(active, rateLimited bool, rateStreak int, rnd *rand.Rand) time.Duration {
	p = p.Resolve()
	base := p.Max
	if active {
		base = p.Min
	}
	if rateLimited {
		streak := rateStreak
		if streak < 1 {
			streak = 1
		}
		if streak > 5 {
			streak = 5
		}
		multiplier := 1.0
		for i := 0; i < streak; i++ {
			multiplier *= p.BackoffFactor
		}
		base = time.Duration(float64(base) * multiplier)
		capValue := time.Duration(float64(p.Max) * p.BackoffFactor * p.BackoffFactor)
		if base > capValue {
			base = capValue
		}
	}
	if p.Jitter > 0 && rnd != nil {
		delta := (rnd.Float64()*2 - 1) * p.Jitter
		base = time.Duration(float64(base) * (1 + delta))
	}
	if base < p.Min/2 {
		base = p.Min / 2
	}
	return base
}
