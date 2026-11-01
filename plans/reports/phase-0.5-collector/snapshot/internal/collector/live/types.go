// Package live 通过受控Node子进程采集直播弹幕，不做回复决策或平台发送。
package live

import (
	"RaiseMyFish/internal/collector"
	"context"
	"go.uber.org/zap"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Kind只展示受控类别，底层输出/凭据不进入错误文本。
type Kind string

const (
	KindInput     Kind = "input"
	KindAuth      Kind = "auth"
	KindRuntime   Kind = "runtime"
	KindNetwork   Kind = "network"
	KindProtocol  Kind = "protocol"
	KindRate      Kind = "rate_limit"
	KindTimeout   Kind = "timeout"
	KindExhausted Kind = "exhausted"
)

// Error保留程序可判断的原因链，不打印原始子进程输出。
type Error struct {
	Kind  Kind
	Cause error `json:"-"`
}

func (e *Error) Error() string {
	kind := e.Kind
	switch kind {
	case KindInput, KindAuth, KindRuntime, KindNetwork, KindProtocol, KindRate, KindTimeout, KindExhausted:
	default:
		kind = KindProtocol
	}
	return "直播采集异常（" + string(kind) + "）"
}
func (e *Error) Unwrap() error { return e.Cause }

// Options均由可信装配提供；密钥只进子进程环境，不进命令行。
type Options struct {
	RoomID         string
	SelfID         string
	NodePath       string
	ScriptPath     string
	Cookie         string `json:"-"`
	CookieFile     string `json:"-"`
	HTTPTimeout    time.Duration
	ConnectTimeout time.Duration
	Heartbeat      time.Duration
	IdleTimeout    time.Duration
	StartupTimeout time.Duration
	BackoffMin     time.Duration
	BackoffMax     time.Duration
	StableAfter    time.Duration
	MaxFailures    int
}

// DefaultOptions不访问环境/凭据或平台；构造时还须明确房间/自身身份。
func DefaultOptions() Options {
	return Options{NodePath: "node", ScriptPath: "tools/bili-whale/danmaku-await.mjs", HTTPTimeout: 10 * time.Second, ConnectTimeout: 10 * time.Second, Heartbeat: 30 * time.Second, IdleTimeout: 90 * time.Second, StartupTimeout: 45 * time.Second, BackoffMin: time.Second, BackoffMax: 60 * time.Second, StableAfter: 30 * time.Second, MaxFailures: 8}
}

// Collector只有一个Start拥有子进程，Health并发安全。
type Collector struct {
	options Options
	log     *zap.Logger
	mu      sync.RWMutex
	health  collector.Health
	started atomic.Bool
	command func(context.Context) *exec.Cmd
	pause   func(context.Context, time.Duration) error
}

// Name返回固定组件名，不携带昵称或凭据。
func (c *Collector) Name() string { return "bili-live" }

// Health复制内部状态，调用方不能修改共享状态。
func (c *Collector) Health() collector.Health { c.mu.RLock(); defer c.mu.RUnlock(); return c.health }
func (c *Collector) setOK(ok bool)            { c.mu.Lock(); c.health.OK = ok; c.mu.Unlock() }
func (c *Collector) report(errc chan<- error, err *Error) {
	c.mu.Lock()
	c.health.LastError = err.Error()
	c.mu.Unlock()
	select {
	case errc <- err:
	default:
	}
}
