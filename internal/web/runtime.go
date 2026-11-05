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
type WatchState struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	RoomID    string `json:"room_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
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
	Videos []string `json:"videos"`
}

// DMState 是私信监听与白名单状态。
type DMState struct {
	CollectorState
	AllowedMIDs        []string `json:"allowed_mids"`
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
