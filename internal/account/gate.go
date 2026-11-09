package account

// 本文件实现账号切换的准入屏障。
//
// 语义：
//   - 每次采集/发送操作先 BeginOperation 取得准入，操作结束后释放；
//   - Activate 通过 Switch 暂停新准入，等待在途操作结清，再在独占窗口内验证并切号；
//   - 不取消已经在途的 POST：请求可能已被平台接收，取消只会把结果变成未知；
//   - 切换失败或超时会恢复准入，调用方按普通错误处理。
//
// 恢复准入的前提是独占窗口内的提交顺序：活跃标志只在目标账号验证通过后
// 才由 Activate 提交，fn 返回错误时当前活跃账号仍是旧的已验证账号。
// 任何改动都必须保留这个顺序，否则恢复准入会暴露未验证的账号。

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrSwitchInProgress 表示已有一次账号切换在独占窗口内。
var ErrSwitchInProgress = errors.New("账号切换正在进行")

// Gate 是账号操作的准入闸门。
type Gate struct {
	mu        sync.Mutex
	switching bool
	inflight  int
	// idle 在切换期间、仍有在途操作时非 nil；在途归零时关闭。
	idle chan struct{}
	// resumed 在切换期间非 nil；切换结束时关闭以唤醒等待者。
	resumed chan struct{}
}

// BeginOperation 取得一次操作准入；切换期间阻塞等待，ctx 取消则返回。
func (g *Gate) BeginOperation(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		if !g.switching {
			g.inflight++
			g.mu.Unlock()
			var once sync.Once
			return func() { once.Do(g.release) }, nil
		}
		wait := g.resumed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

func (g *Gate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight > 0 {
		g.inflight--
	}
	if g.switching && g.inflight == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

// Switch 暂停准入并等待在途操作结清，然后执行 fn（独占窗口）；结束后恢复准入。
//
// 同一时刻只允许一次切换；重复调用返回 ErrSwitchInProgress。
// 等待超时不执行 fn，但也恢复准入，避免把账号服务永久锁死。
func (g *Gate) Switch(ctx context.Context, fn func(context.Context) error) error {
	if g == nil {
		if fn == nil {
			return nil
		}
		return fn(ctx)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	g.mu.Lock()
	if g.switching {
		g.mu.Unlock()
		return ErrSwitchInProgress
	}
	g.switching = true
	g.resumed = make(chan struct{})
	var idle chan struct{}
	if g.inflight > 0 {
		idle = make(chan struct{})
		g.idle = idle
	}
	resumed := g.resumed
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		if idle != nil && g.idle == idle {
			g.idle = nil
		}
		g.switching = false
		close(resumed)
		if g.resumed == resumed {
			g.resumed = nil
		}
		g.mu.Unlock()
	}()

	if idle != nil {
		select {
		case <-idle:
		case <-ctx.Done():
			return fmt.Errorf("等待在途操作结清失败：%w", ctx.Err())
		}
	}
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

// Inflight 返回当前在途操作数（诊断/测试用）。
func (g *Gate) Inflight() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inflight
}
