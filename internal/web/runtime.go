package web

// 本文件定义面板运行时控制面的契约与状态结构。
//
// 面板只做「校验参数 + 调用控制器 + 返回状态」，真正的启停逻辑在 cmd/fishd/control.go。
// 所有错误文案都不包含凭据；状态里也只给白名单字段。

import (
	"context"
	"time"

	"RaiseMyFish/internal/logview"
	"RaiseMyFish/internal/watchdog"
)

// RuntimeController 是面板可操作的运行时开关集合。
//
// 实现需并发安全；所有 Set 操作幂等，重复开/关不报错。
type RuntimeController interface {
	Status() RuntimeStatus
	SetLiveCollect(enabled bool) error
	SetWatch(enabled bool, roomID string) error
	SetComment(enabled bool, videos []string) error
	SetDM(enabled bool, allowed []string) error
	SetProactive(enabled bool, intervalSeconds int) error
	SetGift(enabled bool) error
	SetMemoryFrozen(frozen bool) error
	SetHotReload(enabled bool) error
	SetMemeLearn(enabled bool) error
	Watchdog() WatchdogStatus
}

// LogSource 是日志读取口（*logview.Source 实现）。
type LogSource interface {
	Recent(q logview.Query) ([]logview.Entry, error)
	Tail(ctx context.Context, fn func(logview.Entry) error) error
}

// CollectorState 是单个采集器的运行状态。
type CollectorState struct {
	Available   bool   `json:"available"`
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	StartedAt   string `json:"started_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastEventAt string `json:"last_event_at,omitempty"`
	Reconnects  int    `json:"reconnects"`
}

// WatchState 是看直播（analysis pipeline）的运行状态。
//
// O09：把「循环存在」与「业务健康」分开表达。
// running=true 只说明定时器还在跑；healthy 才说明最近一轮真的成功了。
// 录制失败/抽帧失败这类持续业务失败过去完全不体现，面板一直显示「运行中」。
type WatchState struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	RoomID    string `json:"room_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// Healthy 表示循环在跑且最近一轮业务成功（无整轮错误、无降级环节）。
	Healthy bool `json:"healthy"`
	// LastAttemptAt 是最近一次尝试的时间（无论成败）。
	LastAttemptAt string `json:"last_attempt_at,omitempty"`
	// LastSuccessAt 是最近一次业务成功的时间。
	LastSuccessAt string `json:"last_success_at,omitempty"`
	// ConsecutiveFailures 是连续失败轮数；成功一轮即归零。
	ConsecutiveFailures int `json:"consecutive_failures"`
	// LastFailureStage 是最近失败发生在哪一环（record/frames/describe/...）。
	LastFailureStage string `json:"last_failure_stage,omitempty"`
}

// FeatureState 是定时/后台能力的通用状态。
type FeatureState struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	LastError string `json:"last_error,omitempty"`
}

// CommentState 是评论监听状态与当前视频列表。
type CommentState struct {
	CollectorState
	Videos       []string `json:"videos"`
	AutoDiscover bool     `json:"auto_discover"`
}

// DMState 是私信监听与白名单状态。
type DMState struct {
	CollectorState
	AllowedMIDs        []string `json:"allowed_mids"`
	AllFollowers       bool     `json:"all_followers"`
	WhitelistAvailable bool     `json:"whitelist_available"`
	WhitelistError     string   `json:"whitelist_error,omitempty"`
}

// ProactiveState 是主动弹幕开关与间隔。
type ProactiveState struct {
	FeatureState
	IntervalSeconds int `json:"interval_seconds"`
}

// GiftState 是送礼开关与当日额度。
type GiftState struct {
	Enabled    bool   `json:"enabled"`
	Real       bool   `json:"real"`
	GiftID     string `json:"gift_id"`
	DailyLimit int    `json:"daily_limit"`
	MaxNum     int    `json:"max_num"`
	Used       int    `json:"used"`
	Day        string `json:"day"`
	Error      string `json:"error,omitempty"`
}

// MemoryState 是记忆冻结状态。
type MemoryState struct {
	Frozen bool   `json:"frozen"`
	Error  string `json:"error,omitempty"`
}

// HeartbeatState 是 fishd 心跳文件快照。
type HeartbeatState struct {
	Exists     bool   `json:"exists"`
	At         string `json:"at,omitempty"`
	PID        int    `json:"pid,omitempty"`
	AgeSeconds int64  `json:"age_seconds"`
	Stale      bool   `json:"stale"`
}

// WatchdogStatus 是看门狗配置、心跳与重启历史。
type WatchdogStatus struct {
	Enabled                  bool                     `json:"enabled"`
	Heartbeat                HeartbeatState           `json:"heartbeat"`
	StaleSeconds             int                      `json:"stale_seconds"`
	UnhealthyThreshold       int                      `json:"unhealthy_threshold"`
	CooldownSeconds          int                      `json:"cooldown_seconds"`
	RestartCommandConfigured bool                     `json:"restart_command_configured"`
	Restarts                 []watchdog.RestartRecord `json:"restarts"`
	Error                    string                   `json:"error,omitempty"`
}

// RuntimeStatus 是面板一次取全的运行时状态。
type RuntimeStatus struct {
	Live        CollectorState `json:"live"`
	Comment     CommentState   `json:"comment"`
	DM          DMState        `json:"dm"`
	Watch       WatchState     `json:"watch"`
	Proactive   ProactiveState `json:"proactive"`
	Gift        GiftState      `json:"gift"`
	Memory      MemoryState    `json:"memory"`
	HotReload   FeatureState   `json:"hot_reload"`
	MemeLearn   FeatureState   `json:"meme_learn"`
	Watchdog    WatchdogStatus `json:"watchdog"`
	GeneratedAt time.Time      `json:"generated_at"`
}
