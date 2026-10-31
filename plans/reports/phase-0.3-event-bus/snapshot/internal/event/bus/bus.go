// Package bus 实现PG仓储之上的进程内唤醒，不管理后台worker。
package bus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

// Bus 的通知channel可并发写，持久化行为由注入的QueueRepo实现。
type Bus struct {
	repo   store.QueueRepo
	owner  string
	ttl    time.Duration
	notify chan struct{}
}

// New 分配每实例Owner，默认5分钟租期，不启动goroutine。
func New(repo store.QueueRepo, ttl time.Duration) (*Bus, error) {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if nilRepo(repo) || ttl < time.Second || ttl > 24*time.Hour {
		return nil, store.ErrInvalid
	}
	var owner [16]byte
	if _, err := rand.Read(owner[:]); err != nil {
		return nil, fmt.Errorf("创建总线实例标识：%w", err)
	}
	return &Bus{repo: repo, owner: hex.EncodeToString(owner[:]), ttl: ttl, notify: make(chan struct{}, 1)}, nil
}

func nilRepo(repo store.QueueRepo) bool {
	if repo == nil {
		return true
	}
	value := reflect.ValueOf(repo)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return value.IsNil()
	}
	return false
}
func (b *Bus) wake() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// Publish 仅在首次成功写入入事件后唤醒，不阻塞采集。
func (b *Bus) Publish(ctx context.Context, e event.Event) (bool, error) {
	if ctx == nil {
		return false, store.ErrInvalid
	}
	ok, err := b.repo.Insert(ctx, e)
	if err == nil && ok && e.Dir == event.DirIn {
		b.wake()
	}
	return ok, err
}

// ClaimPending 按PG时间原子抢占到期事件。
func (b *Bus) ClaimPending(ctx context.Context, n int) ([]event.Claim, error) {
	if ctx == nil || n < 1 || n > 100 {
		return nil, store.ErrInvalid
	}
	return b.repo.Claim(ctx, b.owner, b.ttl, n)
}

// Mark 带持有权和状态前置条件更新。
func (b *Bus) Mark(ctx context.Context, c event.Claim, from []event.Status, to event.Status) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	return b.repo.MarkClaim(ctx, c, from, to, b.ttl)
}

// Defer 持久化未来期限并释放持有权。
func (b *Bus) Defer(ctx context.Context, c event.Claim, until time.Time) error {
	if ctx == nil {
		return store.ErrInvalid
	}
	return b.repo.DeferClaim(ctx, c, until)
}

// Renew 返回数据库续租后的快照，不改变状态起点。
func (b *Bus) Renew(ctx context.Context, c event.Claim) (event.Claim, error) {
	if ctx == nil {
		return event.Claim{}, store.ErrInvalid
	}
	return b.repo.RenewClaim(ctx, c, b.ttl)
}

// Recover 有界恢复过期工作并提示本实例轮询。
func (b *Bus) Recover(ctx context.Context, n int) (int, error) {
	if ctx == nil || n < 1 || n > 100 {
		return 0, store.ErrInvalid
	}
	count, err := b.repo.RecoverExpired(ctx, n)
	if err == nil && count > 0 {
		b.wake()
	}
	return count, err
}

// Notify 返回合并的唤醒提示，channel随Bus生命周期存在且不关闭。
func (b *Bus) Notify() <-chan struct{} { return b.notify }
