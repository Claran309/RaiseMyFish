package store

// 看板注入的读写接口：面板负责管理，ContextBuilder 只读有效行。

import (
	"context"
	"errors"
	"time"
)

// MaxInjectionRunes 是单条注入的正文上限，与 prompt 的注入段预算一致。
const MaxInjectionRunes = 500

// ErrInjectionInvalid 表示注入正文为空、超长或包含控制字符。
var ErrInjectionInvalid = errors.New("注入内容不合法")

// WebInjection 是一条看板注入；ExpiresAt 为 nil 表示永久有效。
type WebInjection struct {
	ID        int64
	Content   string
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt *time.Time
}

// InjectionReader 是上下文装配需要的只读能力。
type InjectionReader interface {
	// ActiveInjections 返回 now 时仍有效的注入，新的在前。
	ActiveInjections(ctx context.Context, now time.Time) ([]WebInjection, error)
}

// InjectionRepo 是面板管理入口；有效性规则由查询层执行。
type InjectionRepo interface {
	InjectionReader
	CreateInjection(ctx context.Context, content string, expiresAt *time.Time) (WebInjection, error)
	UpdateInjection(ctx context.Context, id int64, content string, expiresAt *time.Time) (WebInjection, error)
	DeleteInjection(ctx context.Context, id int64) error
}
