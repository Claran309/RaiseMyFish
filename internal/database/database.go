// Package database 管理GORM/pgx连接与文件迁移，不承载业务规则。
package database

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 连接池默认值与原硬编码保持一致；PoolOptions 里的零值按这里补齐。
const (
	defaultMaxOpenConns       = 30
	defaultMaxIdleConns       = 10
	defaultConnMaxLifetime    = 30 * time.Minute
	defaultSlowQueryThreshold = 200 * time.Millisecond
)

// PoolOptions 是连接池与慢查询日志的可调参数。
//
// Logger 为 nil 或 SlowThreshold<=0 时不记录慢查询（CLI 等短进程没必要开）。
type PoolOptions struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	SlowThreshold   time.Duration
	Logger          *zap.Logger
}

// normalize 把零值补成默认值；负值视为配置错误，调用方应提前校验。
func (o PoolOptions) normalize() PoolOptions {
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = defaultMaxOpenConns
	}
	if o.MaxIdleConns <= 0 {
		o.MaxIdleConns = defaultMaxIdleConns
	}
	if o.MaxIdleConns > o.MaxOpenConns {
		o.MaxIdleConns = o.MaxOpenConns
	}
	if o.ConnMaxLifetime <= 0 {
		o.ConnMaxLifetime = defaultConnMaxLifetime
	}
	if o.SlowThreshold <= 0 {
		o.SlowThreshold = defaultSlowQueryThreshold
	}
	return o
}

// safeError 的展示不包含DSN；Unwrap保留错误链供程序判断，禁止直接打印底层错误。
type safeError struct {
	message string
	cause   error
}

func (e *safeError) Error() string { return e.message }
func (e *safeError) Unwrap() error { return e.cause }

// Open 建立有超时的连接并验证PostgreSQL版本。
//
// pool 可选：不传时沿用默认池参数（30/10/30m），保持既有调用方语义不变。
func Open(ctx context.Context, dsn string, timeout time.Duration, pool ...PoolOptions) (*gorm.DB, error) {
	pc, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, &safeError{"解析数据库连接配置失败", err}
	}
	var opts PoolOptions
	if len(pool) > 0 {
		opts = pool[0]
	}
	return openConfig(ctx, pc, timeout, opts)
}

func openConfig(ctx context.Context, pc *pgx.ConnConfig, timeout time.Duration, opts PoolOptions) (*gorm.DB, error) {
	opts = opts.normalize()
	pc.ConnectTimeout = timeout
	pool := stdlib.OpenDB(*pc)
	pool.SetMaxOpenConns(opts.MaxOpenConns)
	pool.SetMaxIdleConns(opts.MaxIdleConns)
	pool.SetConnMaxLifetime(opts.ConnMaxLifetime)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: newGormLogger(opts), DisableAutomaticPing: true})
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

// newGormLogger 返回带慢查询阈值的 GORM logger。
//
// 关键约束：ParameterizedQueries=true——日志里只有 ? 占位符，绝不带参数值
// （事件正文、私信内容都可能出现在参数里）。未注入 zap 时保持静默。
func newGormLogger(opts PoolOptions) logger.Interface {
	if opts.Logger == nil {
		return logger.Default.LogMode(logger.Silent)
	}
	return logger.New(log.New(zapLineWriter{logger: opts.Logger}, "", 0), logger.Config{
		SlowThreshold:             opts.SlowThreshold,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
		Colorful:                  false,
	})
}

// zapLineWriter 把 GORM 的单行输出转成 zap 字段，避免慢查询只落在 stderr。
type zapLineWriter struct{ logger *zap.Logger }

func (w zapLineWriter) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	if line == "" {
		return len(p), nil
	}
	w.logger.Warn("数据库慢查询或告警", zap.String("action", "db_slow"), zap.String("detail", line))
	return len(p), nil
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
