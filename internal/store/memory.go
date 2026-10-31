package store

import (
	"RaiseMyFish/internal/memory"
	"context"
	"fmt"
	"strings"
)

// MemoryRecord 是写入用数据；只读Records接口仅返回嵌入的摘要投影。
type MemoryRecord struct {
	memory.Record
	Source string
	Kind   string
	Title  string
	Body   string
}

// MemoryRepo 将隐私限制放在SQL层；不等于完整Memory Reader/提炼服务。
type MemoryRepo interface {
	InsertRecord(context.Context, MemoryRecord) (bool, error)
	Recent(context.Context, memory.Scope, int) ([]memory.Message, error)
	CrossChannel(context.Context, memory.Scope, int) ([]memory.Message, error)
	Records(context.Context, memory.Scope, int) ([]memory.Record, error)
}

// Validate 检查持久化输入，拒绝错误降密和无范围私密记录。
func (r MemoryRecord) Validate() error {
	if strings.TrimSpace(r.Source) == "" || strings.TrimSpace(r.Title) == "" || r.ValidFrom.IsZero() {
		return fmt.Errorf("记忆基本字段错误：%w", ErrInvalid)
	}
	switch r.Kind {
	case "talk", "preference", "promise", "fact", "summary", "note":
	default:
		return fmt.Errorf("记忆类型错误：%w", ErrInvalid)
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
