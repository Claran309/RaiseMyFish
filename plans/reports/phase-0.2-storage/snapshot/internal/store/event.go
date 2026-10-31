package store

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// EventRepo 是事件事实及条件状态更新接口；Claim/恢复留0.3。
type EventRepo interface {
	Insert(context.Context, event.Event) (bool, error)
	Get(context.Context, string) (event.Event, error)
	Transition(context.Context, string, []event.Status, event.Status) error
}

// ValidateEvent 校验新事件，不处理后续状态更新。
func ValidateEvent(e event.Event) error {
	parts := strings.SplitN(e.ID, ":", 3)
	if len(parts) != 3 || parts[0] != string(e.Channel) || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" || !e.Dir.Valid() || e.At.IsZero() || strings.TrimSpace(e.Text) == "" || !utf8.ValidString(e.Text) || utf8.RuneCountInString(e.Text) > 4000 {
		return fmt.Errorf("事件基本字段错误：%w", ErrInvalid)
	}
	if err := memory.ValidateScope(memory.Scope{Channel: e.Channel, Session: e.Session, Private: e.Private}); err != nil {
		return fmt.Errorf("事件范围错误：%w", ErrInvalid)
	}
	if len(e.Meta) > 0 && !json.Valid(e.Meta) {
		return fmt.Errorf("事件Meta不是JSON：%w", ErrInvalid)
	}
	if (e.Dir == event.DirIn && e.Status != event.StatusPending) || (e.Dir == event.DirOut && e.Status != event.StatusDone && e.Status != event.StatusRetry && e.Status != event.StatusDead) {
		return fmt.Errorf("事件初始状态错误：%w", ErrInvalid)
	}
	return nil
}

// ValidateTransition 校验from集合每一条边，避免用混合非法状态绕过状态机。
func ValidateTransition(from []event.Status, to event.Status) error {
	if len(from) == 0 {
		return fmt.Errorf("状态条件不能为空：%w", ErrInvalid)
	}
	for _, f := range from {
		ok := false
		switch f {
		case event.StatusPending:
			ok = to == event.StatusSkipped || to == event.StatusQueued
		case event.StatusQueued:
			ok = to == event.StatusReplying || to == event.StatusRetry || to == event.StatusDead
		case event.StatusReplying:
			ok = to == event.StatusSent || to == event.StatusRetry || to == event.StatusDead || to == event.StatusQueued
		case event.StatusSent:
			ok = to == event.StatusDone || to == event.StatusRetry
		case event.StatusRetry:
			ok = to == event.StatusQueued || to == event.StatusDead
		}
		if !ok {
			return fmt.Errorf("非法事件状态迁移：%w", ErrInvalid)
		}
	}
	return nil
}
