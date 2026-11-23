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
	// Latency 是近 24 小时各通道回复延迟分位（live/comment/dm）。
	// 空 map 表示窗口内没有成功的入向回复，不是「延迟为 0」。
	Latency map[string]WebLatency
	// LatencyDetail 是 all/fresh 与五段拆分；空 map 表示历史数据不足。
	LatencyDetail map[string]WebLatencyDetail
	// DeadTop 是近 24 小时「通道×环节×error_kind」的死信 Top5。
	DeadTop []WebDeadStat
	// ReplyCost 是近 24 小时单条回复成本与缓存命中率。
	ReplyCost   WebReplyCost
	Pool        *WebPool
	GeneratedAt time.Time
}

// WebLatency 是单通道回复延迟（毫秒）；口径为 send_log.at - events.at。
type WebLatency struct {
	Count int64
	P50MS int64
	P90MS int64
	AvgMS int64
	MaxMS int64
}

// WebLatencyWindow 是一个统计窗口的分位；SampleSufficient=false 时页面不展示分位。
type WebLatencyWindow struct {
	Count            int64
	P50MS            int64
	P90MS            int64
	P99MS            int64
	AvgMS            int64
	MaxMS            int64
	SampleSufficient bool
}

// WebLatencySegments 是采集/排队/生成/发送拆分。
type WebLatencySegments struct {
	Collect     WebLatencyWindow
	Queue       WebLatencyWindow
	Generate    WebLatencyWindow
	Send        WebLatencyWindow
	E2EUser     WebLatencyWindow
	E2EPlatform WebLatencyWindow
}

// WebLatencyDetail 是同一通道的全量与 fresh 口径。
type WebLatencyDetail struct {
	All      WebLatencyWindow
	Fresh    WebLatencyWindow
	Segments WebLatencySegments
}

type WebDeadStat struct {
	Channel   string
	Stage     string
	ErrorKind string
	Count     int64
}

// WebReplyCost 是成本面板投影；CostPerReply 为 nil 表示窗口内没有已计价调用。
type WebReplyCost struct {
	Replies           int64
	CostPerReplyUSD   *float64
	CostPerReplyCNY   *float64
	CachedInputTokens int64
	InputTokens       int64
	CacheHitRate      float64
}

// WebPool 是连接池运行态（database/sql DBStats 的只读投影）。
type WebPool struct {
	Open           int
	InUse          int
	Idle           int
	MaxOpen        int
	WaitCount      int64
	WaitDurationMS int64
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
