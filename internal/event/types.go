// Package event 定义事件契约类型；总线与状态迁移尚未实现。
package event

import (
	"encoding/json"
	"time"
)

// Channel 是平台通道，场景口径不增加新通道。
type Channel string

const (
	ChLive    Channel = "live"
	ChComment Channel = "comment"
	ChDM      Channel = "dm"
	ChQQ      Channel = "qq"
)

// Valid 检查通道枚举。
func (c Channel) Valid() bool { return c == ChLive || c == ChComment || c == ChDM || c == ChQQ }

// Dir 是事件收发方向。
type Dir string

const (
	DirIn  Dir = "in"
	DirOut Dir = "out"
)

// Valid 检查方向枚举。
func (d Dir) Valid() bool { return d == DirIn || d == DirOut }

// Status 仅定义持久化状态名，不在此实现状态迁移。
type Status string

const (
	StatusPending  Status = "pending"
	StatusSkipped  Status = "skipped"
	StatusQueued   Status = "queued"
	StatusReplying Status = "replying"
	StatusSent     Status = "sent"
	StatusDone     Status = "done"
	StatusRetry    Status = "retry"
	StatusDead     Status = "dead"
)

// Valid 检查状态枚举。
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusSkipped, StatusQueued, StatusReplying, StatusSent, StatusDone, StatusRetry, StatusDead:
		return true
	}
	return false
}

// Event 字段以contracts/event.md为真源，Meta不直接进入模型上下文。
type Event struct {
	ID       string
	Channel  Channel
	Dir      Dir
	Session  string
	FromID   string
	FromName string
	Text     string
	Private  bool
	ReplyTo  string
	At       time.Time
	Status   Status
	Meta     json.RawMessage
	// CollectedAt 是本进程实际收到事件的时间；At 永远是平台时间。
	// 零值表示历史数据未记录，统计必须按 unknown 处理，不能伪造成 0。
	CollectedAt time.Time
	// CollectMode 记录采集方式：feed（直播/消息流）、poll（轮询）、backfill（历史回填）。
	// 空值表示历史数据未标记；fresh 统计只排除明确的 backfill。
	CollectMode string
	// ConversationID 是按通道语义派生的会话键；空表示不参与持久会话。
	ConversationID string
}
