package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"RaiseMyFish/migrations"
	"gorm.io/gorm"
)

// checksum 覆盖初始建表和种子；已落库迁移的内容禁止修改。
func migrationSource() (string, string, error) {
	ddl, err := migrations.Files.ReadFile("0001_init.sql")
	if err != nil {
		return "", "", fmt.Errorf("读取建表资源：%w", err)
	}
	seed, err := migrations.Files.ReadFile("seed.sql")
	if err != nil {
		return "", "", fmt.Errorf("读取种子资源：%w", err)
	}
	source := string(ddl) + "\n" + string(seed)
	hash := sha256.Sum256([]byte(source))
	return source, hex.EncodeToString(hash[:]), nil
}

// Migrate 在单事务及事务锁内应用初始迁移；失败不留下半套表。
func Migrate(ctx context.Context, db *gorm.DB) error {
	source, checksum, err := migrationSource()
	if err != nil {
		return err
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(current_database()), hashtext(current_schema() || ':migrate'))").Error; err != nil {
			return err
		}
		if err := tx.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (version bigint PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())").Error; err != nil {
			return err
		}
		var rows []struct {
			Version  int64
			Checksum string
		}
		if err := tx.Raw("SELECT version,checksum FROM schema_migrations ORDER BY version").Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if len(rows) != 1 || rows[0].Version != 1 || rows[0].Checksum != checksum {
				return errors.New("迁移版本或checksum不匹配")
			}
			return nil
		}
		if err := tx.Exec(source).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO schema_migrations(version,checksum) VALUES (?,?)", 1, checksum).Error
	})
	if err != nil {
		return &safeError{"应用迁移失败（检查版本、checksum或既有表冲突）", err}
	}
	return nil
}

// Ready 只验证迁移账本及必需表，不隐式改库。
func Ready(ctx context.Context, db *gorm.DB) error {
	_, checksum, err := migrationSource()
	if err != nil {
		return err
	}
	var rows []struct {
		Version  int64
		Checksum string
	}
	if err := db.WithContext(ctx).Raw("SELECT version,checksum FROM schema_migrations ORDER BY version").Scan(&rows).Error; err != nil {
		return &safeError{"数据库尚未迁移，请执行 fishd migrate up", err}
	}
	if len(rows) != 1 || rows[0].Version != 1 || rows[0].Checksum != checksum {
		return errors.New("数据库迁移版本或checksum不匹配")
	}
	var exists bool
	err = db.WithContext(ctx).Raw(`SELECT bool_and(to_regclass(name) IS NOT NULL) FROM unnest(ARRAY['events','tasks','memory_sources','memory_records','llm_usage','send_log','dlq']) AS name`).Scan(&exists).Error
	if err != nil {
		return &safeError{"检查数据库表失败", err}
	}
	if !exists {
		return errors.New("数据库业务表不完整")
	}
	return nil
}
