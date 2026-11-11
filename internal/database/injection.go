package database

// 看板注入仓储：内容合法性在这里兜底，面板与 ContextBuilder 共用同一份规则。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

type injectionRow struct {
	ID        int64
	Content   string
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt sql.NullTime
}

func (row injectionRow) view() store.WebInjection {
	out := store.WebInjection{
		ID: row.ID, Content: row.Content,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.ExpiresAt.Valid {
		expires := row.ExpiresAt.Time
		out.ExpiresAt = &expires
	}
	return out
}

// validateInjection 拒绝空内容、超长与控制字符；尖括号由 prompt 渲染层转义。
func validateInjection(content string) error {
	content = strings.TrimSpace(content)
	if content == "" || !utf8.ValidString(content) || utf8.RuneCountInString(content) > store.MaxInjectionRunes || strings.ContainsRune(content, '\x00') {
		return store.ErrInjectionInvalid
	}
	return nil
}

// ActiveInjections 返回 now 时仍有效的注入，新的在前。
func (r *WebRepo) ActiveInjections(ctx context.Context, now time.Time) ([]store.WebInjection, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []injectionRow
	if err := r.db.WithContext(ctx).Table("context_injections").
		Where("expires_at IS NULL OR expires_at > ?", now).
		Order("created_at DESC, id DESC").Limit(50).Find(&rows).Error; err != nil {
		return nil, &safeError{"查询看板注入失败", err}
	}
	out := make([]store.WebInjection, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.view())
	}
	return out, nil
}

// CreateInjection 写入一条注入；expiresAt 为 nil 表示永久。
func (r *WebRepo) CreateInjection(ctx context.Context, content string, expiresAt *time.Time) (store.WebInjection, error) {
	if ctx == nil {
		return store.WebInjection{}, store.ErrInvalid
	}
	content = strings.TrimSpace(content)
	if err := validateInjection(content); err != nil {
		return store.WebInjection{}, err
	}
	var row injectionRow
	if err := r.db.WithContext(ctx).Raw(
		`INSERT INTO context_injections(content, expires_at) VALUES (?, ?)
		 RETURNING id, content, created_at, updated_at, expires_at`, content, expiresAt,
	).Scan(&row).Error; err != nil {
		return store.WebInjection{}, &safeError{"写入看板注入失败", err}
	}
	return row.view(), nil
}

// UpdateInjection 覆盖正文与有效期；不存在的 id 返回 ErrWebNotFound。
func (r *WebRepo) UpdateInjection(ctx context.Context, id int64, content string, expiresAt *time.Time) (store.WebInjection, error) {
	if ctx == nil || id < 1 {
		return store.WebInjection{}, store.ErrInvalid
	}
	content = strings.TrimSpace(content)
	if err := validateInjection(content); err != nil {
		return store.WebInjection{}, err
	}
	res := r.db.WithContext(ctx).Exec(
		"UPDATE context_injections SET content = ?, expires_at = ?, updated_at = now() WHERE id = ?", content, expiresAt, id)
	if res.Error != nil {
		return store.WebInjection{}, &safeError{"更新看板注入失败", res.Error}
	}
	if res.RowsAffected == 0 {
		return store.WebInjection{}, store.ErrWebNotFound
	}
	var row injectionRow
	if err := r.db.WithContext(ctx).Table("context_injections").
		Where("id = ?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.WebInjection{}, store.ErrWebNotFound
		}
		return store.WebInjection{}, &safeError{"读取看板注入失败", err}
	}
	return row.view(), nil
}

// DeleteInjection 物理删除；删除后下一轮上下文立即不再注入。
func (r *WebRepo) DeleteInjection(ctx context.Context, id int64) error {
	if ctx == nil || id < 1 {
		return store.ErrInvalid
	}
	res := r.db.WithContext(ctx).Exec("DELETE FROM context_injections WHERE id = ?", id)
	if res.Error != nil {
		return &safeError{"删除看板注入失败", res.Error}
	}
	if res.RowsAffected == 0 {
		return store.ErrWebNotFound
	}
	return nil
}
