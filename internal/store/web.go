package store

// 本文件定义 Web 面板需要的只读/管理接口。
//
// 面板不直接写 SQL：所有查询走这里，由 database 实现，方便测试替换。

import (
	"context"
	"errors"
	"time"
)

// WebStatus 是概览页需要的聚合数字。
type WebStatus struct {
	EventsTotal   int64
	EventsPending int64
	MemoryTotal   int64
	MemoryByKind  map[string]int64
	SendTotal     int64
	SendFailed    int64
	CostUSD       float64
	GeneratedAt   time.Time
}

// WebMemory 是记忆列表项（正文截断，不含凭据）。
type WebMemory struct {
	ID         string
	Kind       string
	Title      string
	Summary    string
	Importance float64
	ValidFrom  time.Time
	CreatedAt  time.Time
}

// WebEvent 是活动流水项。
type WebEvent struct {
	ID       string
	Channel  string
	Dir      string
	Session  string
	FromName string
	Text     string
	At       time.Time
	Status   string
}

// WebSend 是发送记录项。
type WebSend struct {
	EventID string
	Channel string
	OK      bool
	MsgID   string
	At      time.Time
}

// WebRepo 是面板的数据访问口。
type WebRepo interface {
	// Status 汇总计数与累计费用。
	Status(ctx context.Context) (WebStatus, error)
	// Memory 按 kind/关键词过滤记忆（新的在前）。
	Memory(ctx context.Context, kind, query string, limit int) ([]WebMemory, error)
	// ArchiveMemory 软删除一条记忆（归档 + 置失效时间）。
	ArchiveMemory(ctx context.Context, id string) error
	// Events 取最近事件（dir 为空表示不筛）。
	Events(ctx context.Context, dir string, limit int) ([]WebEvent, error)
	// SendLogs 取最近发送记录。
	SendLogs(ctx context.Context, limit int) ([]WebSend, error)
}

// ErrWebNotFound 表示目标记录不存在。
var ErrWebNotFound = errors.New("记录不存在")
