package database

// B站账号仓储：读写 accounts 表，活跃账号由部分唯一索引兜底。
// SQL 全部参数化；错误信息不携带 cookie/csrf。

import (
	"context"
	"time"

	"RaiseMyFish/internal/account"
	"RaiseMyFish/internal/store"
	"gorm.io/gorm"
)

// AccountRepo 实现 account.Store。
type AccountRepo struct{ db *gorm.DB }

func NewAccountRepo(db *gorm.DB) (*AccountRepo, error) {
	if db == nil {
		return nil, store.ErrInvalid
	}
	return &AccountRepo{db: db}, nil
}

type accountRow struct {
	ID        string
	MID       string
	Name      string
	Cookie    string
	CSRF      string
	Status    string
	LastCheck *time.Time
	Note      string
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (row accountRow) toAccount() account.Account {
	result := account.Account{
		ID: row.ID, MID: row.MID, Name: row.Name, Cookie: row.Cookie, CSRF: row.CSRF,
		Status: row.Status, Note: row.Note, IsActive: row.IsActive,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.LastCheck != nil {
		result.LastCheck = *row.LastCheck
	}
	return result
}

const accountColumns = `id, mid, name, cookie, csrf, status, last_check, note, is_active, created_at, updated_at`

// List 按活跃优先、创建时间升序返回全部账号。
func (r *AccountRepo) List(ctx context.Context) ([]account.Account, error) {
	if ctx == nil {
		return nil, store.ErrInvalid
	}
	var rows []accountRow
	if err := r.db.WithContext(ctx).Raw(
		"SELECT " + accountColumns + " FROM accounts ORDER BY is_active DESC, created_at ASC, id ASC").Scan(&rows).Error; err != nil {
		return nil, &safeError{"读取账号列表失败", err}
	}
	result := make([]account.Account, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.toAccount())
	}
	return result, nil
}

// Get 按内部 id 取账号。
func (r *AccountRepo) Get(ctx context.Context, id string) (account.Account, error) {
	if ctx == nil {
		return account.Account{}, store.ErrInvalid
	}
	return r.get(ctx, r.db, id)
}

func (r *AccountRepo) get(ctx context.Context, db *gorm.DB, id string) (account.Account, error) {
	var rows []accountRow
	if err := db.WithContext(ctx).Raw(
		"SELECT "+accountColumns+" FROM accounts WHERE id = ?", id).Scan(&rows).Error; err != nil {
		return account.Account{}, &safeError{"读取账号失败", err}
	}
	if len(rows) == 0 {
		return account.Account{}, account.ErrNotFound
	}
	return rows[0].toAccount(), nil
}

// Active 返回当前活跃账号；不存在时返回 account.ErrNotFound。
func (r *AccountRepo) Active(ctx context.Context) (account.Account, error) {
	if ctx == nil {
		return account.Account{}, store.ErrInvalid
	}
	var rows []accountRow
	if err := r.db.WithContext(ctx).Raw(
		"SELECT " + accountColumns + " FROM accounts WHERE is_active LIMIT 1").Scan(&rows).Error; err != nil {
		return account.Account{}, &safeError{"读取活跃账号失败", err}
	}
	if len(rows) == 0 {
		return account.Account{}, account.ErrNotFound
	}
	return rows[0].toAccount(), nil
}

const upsertAccountSQL = `
INSERT INTO accounts (id, mid, name, cookie, csrf, status, last_check, note)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (mid) DO UPDATE SET
    name = EXCLUDED.name,
    cookie = EXCLUDED.cookie,
    csrf = EXCLUDED.csrf,
    status = EXCLUDED.status,
    last_check = EXCLUDED.last_check,
    note = CASE WHEN EXCLUDED.note = '' THEN accounts.note ELSE EXCLUDED.note END,
    updated_at = clock_timestamp()
RETURNING id`

// Upsert 按 mid 幂等写入；当前没有活跃账号时自动激活该账号。
func (r *AccountRepo) Upsert(ctx context.Context, a account.Account) (account.Account, error) {
	if ctx == nil {
		return account.Account{}, store.ErrInvalid
	}
	var result account.Account
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var id string
		var lastCheck *time.Time
		if !a.LastCheck.IsZero() {
			value := a.LastCheck.UTC()
			lastCheck = &value
		}
		var written struct{ ID string }
		if err := tx.Raw(upsertAccountSQL, a.ID, a.MID, a.Name, a.Cookie, a.CSRF, a.Status, lastCheck, a.Note).Scan(&written).Error; err != nil {
			return &safeError{"写入账号失败", err}
		}
		id = written.ID
		if err := tx.Exec(
			"UPDATE accounts SET is_active = TRUE, updated_at = clock_timestamp() WHERE id = ? AND NOT EXISTS (SELECT 1 FROM accounts WHERE is_active)",
			id).Error; err != nil {
			return &safeError{"激活首个账号失败", err}
		}
		stored, err := r.get(ctx, tx, id)
		if err != nil {
			return err
		}
		result = stored
		return nil
	})
	if err != nil {
		return account.Account{}, err
	}
	return result, nil
}

// SetActive 在事务内先清空旧活跃再设置目标，避免唯一索引冲突。
func (r *AccountRepo) SetActive(ctx context.Context, id string) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var found struct{ Count int64 }
		if err := tx.Raw("SELECT COUNT(*) AS count FROM accounts WHERE id = ?", id).Scan(&found).Error; err != nil {
			return &safeError{"读取账号失败", err}
		}
		if found.Count == 0 {
			return account.ErrNotFound
		}
		if err := tx.Exec(
			"UPDATE accounts SET is_active = FALSE, updated_at = clock_timestamp() WHERE is_active AND id <> ?", id).Error; err != nil {
			return &safeError{"清理旧活跃账号失败", err}
		}
		if err := tx.Exec(
			"UPDATE accounts SET is_active = TRUE, updated_at = clock_timestamp() WHERE id = ?", id).Error; err != nil {
			return &safeError{"切换活跃账号失败", err}
		}
		return nil
	})
}

// ClearActive 清空全部活跃标记；切换验证失败且没有旧账号可回滚时使用。
func (r *AccountRepo) ClearActive(ctx context.Context) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	if err := r.db.WithContext(ctx).Exec(
		"UPDATE accounts SET is_active = FALSE, updated_at = clock_timestamp() WHERE is_active").Error; err != nil {
		return &safeError{"清空活跃账号失败", err}
	}
	return nil
}

// Delete 按 id 删除账号。
func (r *AccountRepo) Delete(ctx context.Context, id string) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	result := r.db.WithContext(ctx).Exec("DELETE FROM accounts WHERE id = ?", id)
	if result.Error != nil {
		return &safeError{"删除账号失败", result.Error}
	}
	if result.RowsAffected == 0 {
		return account.ErrNotFound
	}
	return nil
}

// UpdateCheck 更新验证结果与时间。
func (r *AccountRepo) UpdateCheck(ctx context.Context, id, name, status string, at time.Time) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	result := r.db.WithContext(ctx).Exec(
		"UPDATE accounts SET name = ?, status = ?, last_check = ?, updated_at = clock_timestamp() WHERE id = ?",
		name, status, at.UTC(), id)
	if result.Error != nil {
		return &safeError{"更新账号验证状态失败", result.Error}
	}
	if result.RowsAffected == 0 {
		return account.ErrNotFound
	}
	return nil
}

var _ account.Store = (*AccountRepo)(nil)
