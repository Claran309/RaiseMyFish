package extractworker

// 本文件定义「提炼任务队列」的契约：任务表驱动的入队 / 领取 / 完成 / 失败 / 续租。
//
// 与 DoneSource 的区别：
//   - DoneSource 按事件时间游标扫 status=done：未回复的入向消息永远轮不到，
//     晚完成的事件又会被水位跳过去；
//   - JobSource 用 event_id 主键任务表：事件一入库就能被入队，
//     与「是否已回复」「完成得多晚」彻底解耦。

import (
	"context"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

// JobStatus 是提炼任务状态。
type JobStatus string

const (
	// JobPending 待处理（或失败后等待重试）。
	JobPending JobStatus = "pending"
	// JobRunning 已被某个 worker 领取（持有租约）。
	JobRunning JobStatus = "running"
	// JobDone 已处理完成（含「确认跳过」：没提炼出候选也算处理过）。
	JobDone JobStatus = "done"
	// JobFailed 重试次数耗尽，不再自动重试。
	JobFailed JobStatus = "failed"
)

// ExtractionVersion 是当前提炼规则的版本号。
//
// 规则或提示词实质变化时递增：任务表按 (event_id, extraction_version) 记录，
// 未来可用「版本落后」的条件批量重新入队，而不需要清空游标。
const ExtractionVersion = 1

// Job 是一条提炼任务。
type Job struct {
	// EventID 是事件幂等键（任务主键）。
	EventID string
	// Version 是入队时的提炼版本。
	Version int
	// Status 是任务状态。
	Status JobStatus
	// Attempts 是被领取的次数（含本次）。
	Attempts int
	// LeaseOwner / LeaseUntil 是租约持有者与到期时间。
	LeaseOwner string
	LeaseUntil time.Time
	// LastError 是最近一次失败原因（成功后清空）。
	LastError string
	// Event 是领取时一并带出的事件正文；Enqueue/Complete 场景为空。
	Event event.Event
}

// RecordOutcome 是一条记忆记录在事务内的落库结果。
type RecordOutcome struct {
	// Inserted 为 true 表示新增或更新了记录；false 表示内容幂等或冲突被拒绝。
	Inserted bool
}

// JobSource 是提炼任务队列的持久化能力（database.ExtractionJobsRepo 实现）。
//
// 所有方法都必须保证：
//   - Enqueue 幂等（同一事件不会出现两条任务）；
//   - Claim 并发安全（SKIP LOCKED，不重复领取）；
//   - Complete/CompleteWithRecords/Fail/Renew 带租约栅栏（租约被抢走后旧 worker 不能覆盖结果）。
type JobSource interface {
	// Enqueue 把「还没入队的有价值入向事件」补成 pending 任务，返回新增条数。
	Enqueue(ctx context.Context, limit int) (int, error)
	// Claim 领取一批到期任务并把 attempts +1；返回的任务带事件正文。
	Claim(ctx context.Context, owner string, ttl time.Duration, limit int) ([]Job, error)
	// Complete 标记完成；租约已失效时返回 store.ErrConflict。
	Complete(ctx context.Context, job Job) error
	// CompleteWithRecords 在同一事务里做租约栅栏、写入 records 并标记完成。
	//
	// 栅栏先于写入：租约已被抢占/过期时返回 store.ErrConflict 且**不写任何记录**；
	// 任一记录写入失败则整体回滚，任务保持 running 由租约到期后重新领取。
	CompleteWithRecords(ctx context.Context, job Job, records []store.MemoryRecord) ([]RecordOutcome, error)
	// Fail 记录失败：retryable 且未超上限时回 pending，否则置 failed。
	Fail(ctx context.Context, job Job, reason string, retryable bool, maxAttempts int) error
	// Renew 续租；租约已失效时返回 store.ErrConflict。
	Renew(ctx context.Context, job Job, ttl time.Duration) (Job, error)
}
