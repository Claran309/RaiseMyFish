// Package database 管理GORM/pgx连接与文件迁移，不承载业务规则。
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// safeError 的展示不包含DSN；Unwrap保留错误链供程序判断，禁止直接打印底层错误。
type safeError struct {
	message string
	cause   error
}

func (e *safeError) Error() string { return e.message }
func (e *safeError) Unwrap() error { return e.cause }

// Open 建立有超时的连接并验证PostgreSQL版本。
func Open(ctx context.Context, dsn string, timeout time.Duration) (*gorm.DB, error) {
	pc, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, &safeError{"解析数据库连接配置失败", err}
	}
	return openConfig(ctx, pc, timeout)
}

func openConfig(ctx context.Context, pc *pgx.ConnConfig, timeout time.Duration) (*gorm.DB, error) {
	pc.ConnectTimeout = timeout
	pool := stdlib.OpenDB(*pc)
	pool.SetMaxOpenConns(30)
	pool.SetMaxIdleConns(10)
	pool.SetConnMaxLifetime(30 * time.Minute)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err == nil {
		check, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err = pool.PingContext(check)
		if err == nil {
			var version int
			err = db.WithContext(check).Raw("SELECT current_setting('server_version_num')::int").Scan(&version).Error
			if err == nil && version < 170000 {
				err = fmt.Errorf("需要PostgreSQL17以上，当前版本号%d", version)
			}
		}
	}
	if err != nil {
		closeErr := pool.Close()
		if closeErr != nil {
			return nil, &safeError{"连接数据库失败且连接池释放失败", fmt.Errorf("%w；关闭：%w", err, closeErr)}
		}
		return nil, &safeError{"连接数据库或版本校验失败", err}
	}
	return db, nil
}

// Close 释放底层连接池。
func Close(db *gorm.DB) error {
	pool, err := db.DB()
	if err != nil {
		return &safeError{"获取数据库连接池失败", err}
	}
	if err := pool.Close(); err != nil {
		return &safeError{"关闭数据库连接池失败", err}
	}
	return nil
}
