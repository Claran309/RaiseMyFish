package config

// 0.11：消费循环（Scheduler + Worker）的策略配置。
//
// 存在 `[scheduler]` 段才表示「要接消费循环」；缺失即保持只采集的现状。
// 段内省略的字段用默认值补齐（见 ApplyDefaults），不要求用户写全。

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"RaiseMyFish/internal/event"
)

// Scheduler 是调度与消费循环参数，字段名对应 TOML 的 snake_case 键。
type Scheduler struct {
	MaxConcurrent      int                             `toml:"max_concurrent"`
	MaxPending         int                             `toml:"max_pending"`
	ClaimBatch         int                             `toml:"claim_batch"`
	PollIntervalMS     int                             `toml:"poll_interval_ms"`
	RetryDelayMS       int                             `toml:"retry_delay_ms"`
	OperationTimeoutMS int                             `toml:"operation_timeout_ms"`
	WorkerTimeoutMS    int                             `toml:"worker_timeout_ms"`
	Channels           map[event.Channel]ChannelPolicy `toml:"channels"`
}

// ChannelPolicy 是单通道的准入策略；ID 只来自可信配置，不从消息正文推断。
type ChannelPolicy struct {
	Brain          string   `toml:"brain"`
	RatePerMinute  float64  `toml:"rate_per_minute"`
	Burst          int      `toml:"burst"`
	DailyQuota     int      `toml:"daily_quota"`
	CooldownMS     int      `toml:"cooldown_ms"`
	MinRunes       int      `toml:"min_runes"`
	RepeatWindowMS int      `toml:"repeat_window_ms"`
	OwnerIDs       []string `toml:"owner_ids"`
	SelfIDs        []string `toml:"self_ids"`
}

// 消费循环默认值（派发单 §1）。
const (
	defaultMaxConcurrent      = 4
	defaultMaxPending         = 64
	defaultClaimBatch         = 8
	defaultPollIntervalMS     = 500
	defaultRetryDelayMS       = 5000
	defaultOperationTimeoutMS = 10000
	defaultWorkerTimeoutMS    = 30000
)

// DefaultScheduler 返回一份可直接使用的默认调度配置。
func DefaultScheduler() Scheduler {
	return Scheduler{
		MaxConcurrent:      defaultMaxConcurrent,
		MaxPending:         defaultMaxPending,
		ClaimBatch:         defaultClaimBatch,
		PollIntervalMS:     defaultPollIntervalMS,
		RetryDelayMS:       defaultRetryDelayMS,
		OperationTimeoutMS: defaultOperationTimeoutMS,
		WorkerTimeoutMS:    defaultWorkerTimeoutMS,
	}
}

// ApplyDefaults 只补零值，不覆盖用户显式写的值。
func (s *Scheduler) ApplyDefaults() {
	if s == nil {
		return
	}
	if s.MaxConcurrent == 0 {
		s.MaxConcurrent = defaultMaxConcurrent
	}
	if s.MaxPending == 0 {
		s.MaxPending = defaultMaxPending
	}
	if s.ClaimBatch == 0 {
		s.ClaimBatch = defaultClaimBatch
	}
	if s.PollIntervalMS == 0 {
		s.PollIntervalMS = defaultPollIntervalMS
	}
	if s.RetryDelayMS == 0 {
		s.RetryDelayMS = defaultRetryDelayMS
	}
	if s.OperationTimeoutMS == 0 {
		s.OperationTimeoutMS = defaultOperationTimeoutMS
	}
	if s.WorkerTimeoutMS == 0 {
		s.WorkerTimeoutMS = defaultWorkerTimeoutMS
	}
	for ch, p := range s.Channels {
		// 与 scheduler 内部一致的保底：0 表示「用默认」，不是「不限制」
		if p.MinRunes == 0 {
			p.MinRunes = 2
		}
		s.Channels[ch] = p
	}
}

// Validate 校验调度配置；nil 表示未启用消费循环，直接通过。
func (s *Scheduler) Validate() error {
	if s == nil {
		return nil
	}
	if s.MaxConcurrent < 1 || s.MaxConcurrent > 100 {
		return errors.New("调度并发需为1至100")
	}
	if s.MaxPending < s.MaxConcurrent {
		return errors.New("调度待处理上限不能小于并发数")
	}
	if s.MaxPending > 10000 {
		return errors.New("调度待处理上限不超过10000")
	}
	if s.ClaimBatch < 1 || s.ClaimBatch > 100 {
		return errors.New("调度批次需为1至100")
	}
	for name, value := range map[string]int{
		"poll_interval_ms":     s.PollIntervalMS,
		"retry_delay_ms":       s.RetryDelayMS,
		"operation_timeout_ms": s.OperationTimeoutMS,
		"worker_timeout_ms":    s.WorkerTimeoutMS,
	} {
		if value <= 0 {
			return fmt.Errorf("调度参数 %s 必须为正数", name)
		}
	}
	if len(s.Channels) == 0 {
		return errors.New("调度器至少要配置一个通道")
	}
	for ch, p := range s.Channels {
		if !ch.Valid() {
			return fmt.Errorf("调度通道 %q 无效", ch)
		}
		if strings.TrimSpace(p.Brain) == "" {
			return fmt.Errorf("通道 %s 的 brain 不能为空", ch)
		}
		if math.IsNaN(p.RatePerMinute) || math.IsInf(p.RatePerMinute, 0) || p.RatePerMinute <= 0 {
			return fmt.Errorf("通道 %s 的 rate_per_minute 必须为正数", ch)
		}
		if p.Burst < 1 {
			return fmt.Errorf("通道 %s 的 burst 至少为1", ch)
		}
		if p.DailyQuota < 1 {
			return fmt.Errorf("通道 %s 的 daily_quota 至少为1", ch)
		}
		if p.CooldownMS < 0 {
			return fmt.Errorf("通道 %s 的 cooldown_ms 不能为负", ch)
		}
		if p.MinRunes < 0 || p.RepeatWindowMS < 0 {
			return fmt.Errorf("通道 %s 的 min_runes/repeat_window_ms 不能为负", ch)
		}
	}
	return nil
}
