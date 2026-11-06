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

// PlanKey 标识一份非直播投递计划：通道、发送账号与目标。
type PlanKey struct {
	Channel     event.Channel
	AccountID   string
	Destination string
}

// PlanOutcome 是一次平台调用后要落库的结果。
type PlanOutcome struct {
	State     string
	OK        bool
	Verified  bool
	MsgID     string
	ErrorKind string
}

// PlannedRepo 负责评论/私信通道的不可变投递计划与尝试。
//
// 计划必须在首次 POST 前落库；ArmPlan 成功提交后才允许 POST。
type PlannedRepo interface {
	PreparePlan(context.Context, event.Claim, PlanKey, string) (sender.Attempt, error)
	LoadPlan(context.Context, event.Claim, PlanKey) (sender.Attempt, error)
	ArmPlan(context.Context, event.Claim, PlanKey, string) (sender.Attempt, error)
	RecordPlan(context.Context, event.Claim, PlanKey, string, PlanOutcome) (sender.Attempt, error)
}
