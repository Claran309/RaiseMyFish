package event

import "time"

// Timing 是单个入事件的阶段时间；零值表示该阶段未记录（历史数据/未到达）。
type Timing struct {
	EventID       string
	CollectedAt   time.Time
	EnqueuedAt    time.Time
	ClaimedAt     time.Time
	GeneratedAt   time.Time
	SendStartedAt time.Time
	SentAt        time.Time
	VerifiedAt    time.Time
}
