package store

import (
	"context"
	"time"
)

// ConversationState 是会话的摘要与最近轮次元数据；不存正文。
type ConversationState struct {
	ConversationID string
	Channel        string
	AccountID      string
	UserID         string
	TopicID        string
	Summary        string
	Unanswered     string
	TurnCount      int
	LastEventID    string
	LastTurnAt     time.Time
	ExpiresAt      time.Time
}

// ConversationTurn 是会话里的一轮消息（只读投影）。
type ConversationTurn struct {
	EventID  string
	FromID   string
	FromName string
	Dir      string
	Text     string
	At       time.Time
}

// ConversationRepo 读写会话状态与最近轮次；隐私过滤在 SQL 层。
type ConversationRepo interface {
	LoadState(context.Context, string, time.Time) (ConversationState, bool, error)
	Touch(context.Context, ConversationState) error
	RecentTurns(context.Context, string, int) ([]ConversationTurn, error)
}

// ConversationSummaryJob 是一条滚动摘要任务。
//
// 任务按会话唯一：同一会话在任一时刻最多一条待办/执行中的摘要任务，
// 避免「同一批轮次生成两份摘要」。
type ConversationSummaryJob struct {
	// ConversationID 是会话主键。
	ConversationID string
	// TargetEventID 是本次摘要覆盖到的最后一条事件（幂等栅栏）。
	TargetEventID string
	// TurnCount 是入队时的会话轮数（摘要只允许被更晚的轮数覆盖）。
	TurnCount int
	// Status 是 pending/running/done/failed。
	Status string
	// Attempts 是被领取的次数（含本次）。
	Attempts int
	// LeaseOwner / LeaseUntil 是租约持有者与到期时间。
	LeaseOwner string
	LeaseUntil time.Time
	// LastError 是最近一次失败原因（成功后清空）。
	LastError string
}

// ConversationSummaryRepo 是滚动摘要任务的持久化能力。
//
// 与 ConversationRepo 分开：读路径只需要状态与轮次，
// 摘要 worker 才需要任务队列语义，避免把租约强加给所有实现。
type ConversationSummaryRepo interface {
	// EnqueueSummaries 为「该摘要了」的会话补一条 pending 任务，返回新增/重置条数。
	//
	// channels 是当前允许摘要的通道（如 dm / comment）；空切片表示不限制。
	EnqueueSummaries(ctx context.Context, now time.Time, channels []string, everyTurns int, idle time.Duration, limit int) (int, error)
	// ClaimSummaries 领取一批到期任务并把 attempts +1。
	ClaimSummaries(ctx context.Context, owner string, ttl time.Duration, limit int) ([]ConversationSummaryJob, error)
	// CompleteSummary 在租约栅栏内写回摘要；租约失效时返回 ErrConflict。
	//
	// summary 为空表示「本轮没生成内容」：旧摘要保留，但覆盖位置照常推进，
	// 避免同一批轮次被反复重算。
	CompleteSummary(ctx context.Context, job ConversationSummaryJob, summary, unanswered string) error
	// FailSummary 记录失败：可重试且未超上限回 pending，否则置 failed。
	FailSummary(ctx context.Context, job ConversationSummaryJob, reason string, retryable bool, maxAttempts int) error
	// RenewSummary 续租；租约失效时返回 ErrConflict。
	RenewSummary(ctx context.Context, job ConversationSummaryJob, ttl time.Duration) (ConversationSummaryJob, error)
}
