package store

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	"context"
)

// DeliveryRepo负责唯一的发送审计写入与租约/阶段原子转换。
type DeliveryRepo interface {
	Prepare(context.Context, event.Claim, sender.Reply, string) (sender.Attempt, error)
	Load(context.Context, event.Claim, string) (sender.Attempt, error)
	Arm(context.Context, event.Claim, string) (sender.Permit, error)
	Defer(context.Context, event.Claim, string, sender.Deferral) (sender.Result, error)
	Settle(context.Context, event.Claim, string, sender.Verdict) (sender.Result, error)
}
