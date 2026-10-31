package event

import (
	"context"
	"errors"
	"time"
)

// Stage 区分首次调度、已有任务的生成和只允许核验的补偿。
type Stage string

const (
	StageSchedule Stage = "schedule"
	StageGenerate Stage = "generate"
	StageVerify   Stage = "verify"
)

// Lease 的代次是持久化栅栏，旧worker即使同Owner也不能越过它。
type Lease struct {
	Owner   string
	Version int64
}

// Claim 是一次领取快照，后续授权仍以数据库实际租约为准。
type Claim struct {
	Event      Event
	Stage      Stage
	Lease      Lease
	LeaseUntil time.Time
	StateAt    time.Time
}

// ErrLeaseLost 表示过期、已释放或已被更高代次持有，调用方必须停止处理。
var ErrLeaseLost = errors.New("事件租约已失效")

// Bus 是P0-EVENT-2，Notify只是合并提示，不能代替持久化轮询。
type Bus interface {
	Publish(context.Context, Event) (bool, error)
	ClaimPending(context.Context, int) ([]Claim, error)
	Mark(context.Context, Claim, []Status, Status) error
	Defer(context.Context, Claim, time.Time) error
	Renew(context.Context, Claim) (Claim, error)
	Recover(context.Context, int) (int, error)
	Notify() <-chan struct{}
}
