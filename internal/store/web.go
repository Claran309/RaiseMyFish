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
//
// 费用拆成「已计价 / 未知」：llm_usage.cost_usd 可为 NULL（未配置报价），
// 不能把 NULL 当 0。KnownCostUSD 为 nil 表示没有任何已计价调用；
// Coverage 是已计价调用占比，TotalCalls 为 0（空账本）时按 0 处理。
type WebStatus struct {
	EventsTotal   int64
	EventsPending int64
	MemoryTotal   int64
	MemoryByKind  map[string]int64
	SendTotal     int64
	SendFailed    int64
	KnownCostUSD  *float64
	UnknownCalls  int64
	TotalCalls    int64
	Coverage      float64
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
	EventID  string
	Channel  string
	OK       bool
	Verified bool
	MsgID    string
	Error    string
	At       time.Time
}

// WebEventDetail 是事件详情：原事件 + 回复计划/尝试。
//
// Meta 由数据库以文本形式给出，调用方负责截断/脱敏。
type WebEventDetail struct {
	WebEvent
	FromID    string
	ReplyTo   string
	Private   bool
	Meta      string
	CreatedAt time.Time
	Reply     *WebReply
}

// WebReply 是 send_plans 的只读投影。
type WebReply struct {
	ActorID      string
	RoomID       string
	Text         string
	State        string
	AttemptCount int
	CreatedAt    time.Time
	Attempts     []WebAttempt
}

// WebAttempt 是一次投递尝试。
type WebAttempt struct {
	Number    int
	State     string
	OK        *bool
	Verified  bool
	MsgID     string
	Evidence  string
	ErrorKind string
	CreatedAt time.Time
}

// WebEmbeddingSection 是单个来源的向量入库进度；知识来源的 Revision 由快照注入。
type WebEmbeddingSection struct {
	Total       int64
	Embedded    int64
	Pending     int64
	LastUpdated time.Time
	Revision    string
}

// WebEmbeddingStatus 是知识/记忆/档案三路向量入库状态。
type WebEmbeddingStatus struct {
	Knowledge   WebEmbeddingSection
	Memory      WebEmbeddingSection
	Viewer      WebEmbeddingSection
	GeneratedAt time.Time
}

// WebRepo 是面板的数据访问口。
type WebRepo interface {
	// Status 汇总计数与费用（区分已计价与未知）。
	Status(ctx context.Context) (WebStatus, error)
	// EmbeddingStatus 汇总知识/记忆/档案的向量入库进度。
	EmbeddingStatus(ctx context.Context) (WebEmbeddingStatus, error)
	// Memory 按 kind/关键词过滤记忆；order 为 time（默认，新的在前）或 importance（重要度高在前）。
	Memory(ctx context.Context, kind, query, order string, limit int) ([]WebMemory, error)
	// ArchiveMemory 软删除一条记忆（归档 + 置失效时间）。
	ArchiveMemory(ctx context.Context, id string) error
	// Events 取最近事件（dir 为空表示不筛）。
	Events(ctx context.Context, dir string, limit int) ([]WebEvent, error)
	// EventsByChannel 按通道取最近事件（dir 为空表示不筛）。
	EventsByChannel(ctx context.Context, channel, dir string, limit int) ([]WebEvent, error)
	// Event 取单条事件详情（含回复计划与尝试）。
	Event(ctx context.Context, id string) (WebEventDetail, error)
	// Errors 取最近未清理的异常事件（status 为 dead/retry）。
	Errors(ctx context.Context, limit int) ([]WebEvent, error)
	// ClearError 标记单条异常已清理；只影响面板列表，不改事件状态机。
	ClearError(ctx context.Context, id string) error
	// ClearErrorsBefore 清理 at 早于 before 的异常，返回清理条数。
	ClearErrorsBefore(ctx context.Context, before time.Time) (int64, error)
	// SendLogs 取最近发送记录。
	SendLogs(ctx context.Context, limit int) ([]WebSend, error)
}

// ErrWebNotFound 表示目标记录不存在。
var ErrWebNotFound = errors.New("记录不存在")
