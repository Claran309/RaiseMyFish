// Package scheduler 只决定任务准入与分流，不生成文本、不发送平台消息。
package scheduler

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"context"
	"errors"
	"time"
)

// Timer/Clock 允许测试显式推进冷却；生产实现使用time.AfterFunc。
type Timer interface{ Stop() bool }
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}
type systemClock struct{}

func (systemClock) Now() time.Time                            { return time.Now() }
func (systemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// ChannelPolicy 由可信装配提供，ID不从消息正文推断。
type ChannelPolicy struct {
	Public        bool
	Private       bool
	Brain         string
	RatePerMinute float64
	Burst         int
	DailyQuota    int
	Cooldown      time.Duration
	MinRunes      int
	RepeatWindow  time.Duration
	OwnerIDs      []string
	SelfIDs       []string
	BotIDs        []string
	Names         []string
	Ads           []string
	Whitelist     []string
}

// Options 只控制当前调度实例；不改变fishd默认业务开关。
type Options struct {
	MaxConcurrent    int
	MaxPending       int
	ClaimBatch       int
	PollInterval     time.Duration
	RetryDelay       time.Duration
	OperationTimeout time.Duration
	WorkerTimeout    time.Duration
	Location         *time.Location
	Clock            Clock
	Channels         map[event.Channel]ChannelPolicy
}

// DefaultOptions 使用单进程P0保守参数，调用New本身不会启用生产通道。
func DefaultOptions() Options {
	return Options{MaxConcurrent: 4, MaxPending: 100, ClaimBatch: 32, PollInterval: time.Second, RetryDelay: time.Second, OperationTimeout: 5 * time.Second, WorkerTimeout: 2 * time.Minute, Location: time.UTC, Channels: map[event.Channel]ChannelPolicy{event.ChLive: {Public: true, Brain: "openai", RatePerMinute: 6, Burst: 1, DailyQuota: 300, Cooldown: 15 * time.Second, MinRunes: 2, RepeatWindow: time.Minute}}}
}

// Work中generate有已有Task；verify无Task，不能借此创建生成任务。
type Work struct {
	Claim event.Claim
	Task  *store.Task
}

// Result.Replied是可信下游的投递事实回执，只用于本实例冷却。
type Result struct{ Replied bool }

// Handler必须响应Context，并在返回前经Bus结清为终态或持久化retry。
type Handler func(context.Context, Work) (Result, error)

// Workers分离生成/核验，避免恢复时误进生成器。
type Workers struct {
	Generate Handler
	Verify   Handler
}

var (
	ErrUnsettled = errors.New("调度worker返回但事件尚未结清")
	ErrStarted   = errors.New("调度器Run仅允许调用一次")
)

type safeError struct {
	message string
	cause   error
}

func (e *safeError) Error() string { return e.message }
func (e *safeError) Unwrap() error { return e.cause }
