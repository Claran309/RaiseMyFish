package store

import (
	"RaiseMyFish/internal/event"
	"context"
	"time"
)

// QueueRepo 提供有租约的原子SQL操作，不依赖具体总线实现。
type QueueRepo interface {
	EventRepo
	Claim(context.Context, string, time.Duration, int) ([]event.Claim, error)
	MarkClaim(context.Context, event.Claim, []event.Status, event.Status, time.Duration) error
	DeferClaim(context.Context, event.Claim, time.Time) error
	RenewClaim(context.Context, event.Claim, time.Duration) (event.Claim, error)
	RecoverExpired(context.Context, int) (int, error)
}

// ClaimedTaskRepo 原子检查持有权、写入唯一Task并切换生成阶段。
type ClaimedTaskRepo interface {
	CreateClaimed(context.Context, event.Claim, Task) (bool, error)
}
