package store

import (
	"context"
	"time"
)

// EventError 是一次失败/跳过的安全摘要；message 不含 prompt、正文或凭据。
type EventError struct {
	EventID   string
	Stage     string
	ErrorKind string
	Message   string
}

// DeadStat 是「通道 × 环节 × error_kind」的死信聚合。
type DeadStat struct {
	Channel   string
	Stage     string
	ErrorKind string
	Count     int64
}

// ErrorRepo 记录失败原因并提供死信分布；只写分类与安全摘要。
type ErrorRepo interface {
	Record(context.Context, EventError) error
	DeadStats(context.Context, time.Time, time.Time, int) ([]DeadStat, error)
}

// DLQItem 是 dlq 表的只读投影。
type DLQItem struct {
	ID       int64
	EventID  string
	Stage    string
	Error    string
	Payload  string
	At       time.Time
	Replayed bool
}

// DLQReplay 是重放结果；Reason 说明为何未重放。
type DLQReplay struct {
	ID       int64
	EventID  string
	Replayed bool
	Reason   string
}
