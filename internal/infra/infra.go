// Package infra 统一管理外部连接及基础设施生命周期。
package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/database"
	"gorm.io/gorm"
)

// Resources 由入口创建并注入；不使用全局连接。
type Resources struct {
	DB       *gorm.DB
	lock     *sql.Conn
	once     sync.Once
	closeErr error
}

// Setup 打开并探测PG；不自动迁移或启动业务模块。
func Setup(ctx context.Context, cfg config.Config) (*Resources, error) {
	db, err := database.Open(ctx, cfg.Database.DSN, time.Duration(cfg.Database.TimeoutSeconds)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("初始化PG：%w", err)
	}
	return &Resources{DB: db}, nil
}

// Acquire 为当前数据库/schema获取进程级单实例锁；生命周期由主循环串行调用。
func (r *Resources) Acquire(ctx context.Context) error {
	if r.lock != nil {
		return errors.New("本实例已持有服务锁")
	}
	pool, err := r.DB.DB()
	if err != nil {
		return connectionError("获取连接池失败", err)
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return connectionError("申请服务锁连接失败", err)
	}
	var locked bool
	err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext(current_database()),hashtext(current_schema() || ':fishd'))").Scan(&locked)
	if err != nil || !locked {
		closeErr := conn.Close()
		if closeErr != nil {
			return connectionError("释放服务锁连接失败", errors.Join(err, closeErr))
		}
		if err != nil {
			return connectionError("申请数据库服务锁失败", err)
		}
		return errors.New("无法获取服务锁：已有fishd运行或数据库不可用")
	}
	r.lock = conn
	return nil
}

// Ping 检查数据库及持有服务锁的连接，避免丢锁后继续报健康。
func (r *Resources) Ping(ctx context.Context) error {
	pool, err := r.DB.DB()
	if err != nil {
		return connectionError("获取连接池失败", err)
	}
	if err := pool.PingContext(ctx); err != nil {
		return connectionError("数据库健康检查失败", err)
	}
	if r.lock != nil {
		if err := r.lock.PingContext(ctx); err != nil {
			return connectionError("数据库服务锁连接断开", err)
		}
	}
	return nil
}

// Close 幂等释放服务锁和连接池。
func (r *Resources) Close(ctx context.Context) error {
	r.once.Do(func() {
		if r.lock != nil {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			var unlocked bool
			err := r.lock.QueryRowContext(ctx, "SELECT pg_advisory_unlock(hashtext(current_database()),hashtext(current_schema() || ':fishd'))").Scan(&unlocked)
			cancel()
			if err != nil {
				r.closeErr = connectionError("释放数据库服务锁失败", err)
			} else if !unlocked {
				r.closeErr = errors.New("释放数据库服务锁失败")
			}
			r.closeErr = errors.Join(r.closeErr, r.lock.Close())
		}
		r.closeErr = errors.Join(r.closeErr, database.Close(r.DB))
	})
	return r.closeErr
}
