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
}
