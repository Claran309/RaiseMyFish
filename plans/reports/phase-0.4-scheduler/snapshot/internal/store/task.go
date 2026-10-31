package store

import (
	"RaiseMyFish/internal/event"
	"context"
	"fmt"
	"strings"
	"time"
)

// Task 是初次排队的任务投影；执行状态与重试协议留0.3/0.4。
type Task struct {
	ID        string
	EventID   string
	Channel   event.Channel
	Session   string
	Brain     string
	Priority  int
	CreatedAt time.Time
	Deadline  *time.Time
}

// TaskRepo 在同一事务内检查queued入事件和唯一任务，不改变事件状态。
type TaskRepo interface {
	CreateForEvent(context.Context, Task) (bool, error)
	GetByEvent(context.Context, string) (Task, error)
}

// Validate 检查任务创建入参，不改变时间或优先级。
func (t Task) Validate() error {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.EventID) == "" || !t.Channel.Valid() || strings.TrimSpace(t.Session) == "" || strings.TrimSpace(t.Brain) == "" || t.Priority < 0 || t.Priority > 3 || t.CreatedAt.IsZero() || (t.Deadline != nil && t.Deadline.IsZero()) {
		return fmt.Errorf("任务基本字段错误：%w", ErrInvalid)
	}
	return nil
}
