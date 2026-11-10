package store

import (
	"RaiseMyFish/internal/memory"
	"context"
	"fmt"
	"strings"
	"time"
)

// MemoryRecord 是写入用数据；只读Records接口仅返回嵌入的摘要投影。
type MemoryRecord struct {
	memory.Record
	Source string
	Kind   string
	Title  string
	Body   string
	// FromID/FromName 是已验证的陈述主体身份（说的是谁）：观众自述填发言人 UID，
	// 主人自述填主人 UID；观众转述主人时留空，转述内容不绑定真实主人身份。
	FromID   string
	FromName string
	// AssertedByID/AssertedByName 是说话人（谁说的），来自可信事件，不能由模型编造。
	AssertedByID   string
	AssertedByName string
	// Subject 是陈述主体（说的是谁）：owner/viewer/fish；空表示旧数据未知。
	Subject string
	// CreatedAt 只由 ListRecords/SearchRecords 填充，用于运维查看；写入时忽略。
	CreatedAt time.Time
}

// MemoryRepo 将隐私限制放在SQL层；不等于完整Memory Reader/提炼服务。
type MemoryRepo interface {
	InsertRecord(context.Context, MemoryRecord) (bool, error)
	Recent(context.Context, memory.Scope, int) ([]memory.Message, error)
	CrossChannel(context.Context, memory.Scope, int) ([]memory.Message, error)
	Records(context.Context, memory.Scope, int) ([]memory.Record, error)
	// ListRecords 返回最近创建的记录，供本机运维查看（含私密与归档行）。
	ListRecords(ctx context.Context, limit int) ([]MemoryRecord, error)
	// SearchRecords 按 title/summary 模糊搜索，返回顺序与 ListRecords 一致。
	SearchRecords(ctx context.Context, q string, limit int) ([]MemoryRecord, error)
	// CountByKind 按 kind 统计记录数量。
	CountByKind(ctx context.Context) (map[string]int, error)
}

// Validate 检查持久化输入，拒绝错误降密和无范围私密记录。
func (r MemoryRecord) Validate() error {
	if strings.TrimSpace(r.Source) == "" || strings.TrimSpace(r.Title) == "" || r.ValidFrom.IsZero() {
		return fmt.Errorf("记忆基本字段错误：%w", ErrInvalid)
	}
	switch r.Kind {
	case "talk", "preference", "promise", "fact", "summary", "note", "viewer_profile":
	default:
		return fmt.Errorf("记忆类型错误：%w", ErrInvalid)
	}
	switch r.Subject {
	case "", memory.SoulSubjectOwner, memory.SoulSubjectViewer, memory.SoulSubjectFish:
	default:
		return fmt.Errorf("记忆主体错误：%w", ErrInvalid)
	}
	scope := memory.Scope{Channel: r.Channel, Session: r.Session, Private: r.Private}
	if r.Channel == "" && r.Session == "" && !r.Private {
		scope = memory.Scope{Channel: "live", Session: "global"}
	} else if err := memory.ValidateScope(scope); err != nil {
		return fmt.Errorf("记忆来源范围错误：%w", ErrInvalid)
	}
	if err := (memory.Context{Scope: scope, Records: []memory.Record{r.Record}}).Validate(); err != nil {
		return fmt.Errorf("记忆摘要错误：%w", ErrInvalid)
	}
	for _, id := range r.EventIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("记忆来源事件为空：%w", ErrInvalid)
		}
	}
	return nil
}

// FactSource 按范围查询当前有效的共享事实（0.9d）。
//
// 刻意不并进 MemoryRepo：并进去会让所有既有 fake 与实现都要多实现一个方法，
// 而事实查询是可选能力（Brain 的 SharedState 用得上，提炼 worker 用不上）。
// 隐私过滤必须在实现的 SQL 层完成，不能靠上层自觉。
//
// 该接口与 memory.FactSource 结构一致：database.MemoryRepo 同时满足两者，
// 装配层把它传给 memory.NewReader 即可直接当事实来源用。
type FactSource interface {
	// Facts 返回 scope 可见的、当前有效的事实，顺序为 Key → At → SourceID 升序。
	Facts(ctx context.Context, scope memory.Scope, limit int) ([]memory.Fact, error)
}
