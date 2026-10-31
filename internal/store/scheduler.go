package store

import (
	"RaiseMyFish/internal/event"
	"context"
	"time"
)

// ScheduleQuota 约束新Task准入，不能当作实际发送账本。
type ScheduleQuota struct {
	Limit int
	From  time.Time
	Until time.Time
}

// ScheduleAdmission 的WaitUntil表示顺序/每日配额暂时不允许准入。
type ScheduleAdmission struct {
	Inserted  bool
	WaitUntil *time.Time
}

// SchedulerRepo 保持查询/事务在存储层，不让调度器拼SQL。
type SchedulerRepo interface {
	Admit(context.Context, event.Claim, Task, ScheduleQuota) (ScheduleAdmission, error)
	Earlier(context.Context, event.Claim) (*time.Time, error)
	Task(context.Context, string) (Task, error)
	Snapshot(context.Context, string) (event.Claim, error)
}
