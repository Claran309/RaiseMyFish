package config

import (
	"errors"
	"strings"
)

// Watchdog 是看门狗配置（`[watchdog]`）。
type Watchdog struct {
	Enabled bool `toml:"enabled"`
	// IntervalSeconds 是检查间隔（默认 60）。
	IntervalSeconds int `toml:"interval_seconds"`
	// StaleSeconds 是心跳过期阈值（默认 120）。
	StaleSeconds int `toml:"stale_seconds"`
	// UnhealthyThreshold 是连续异常多少次才重启（默认 2）。
	UnhealthyThreshold int `toml:"unhealthy_threshold"`
	// CooldownSeconds 是重启后的冷却（默认 300）。
	CooldownSeconds int `toml:"cooldown_seconds"`
	// RestartCommand 是重启命令（如 systemctl restart fishd，或直接启动常驻 fishd）。
	RestartCommand string `toml:"restart_command"`
	// RestartMode 是命令模式：service-command（短命管理命令，超时即失败）
	// 或 managed-child（直接拉起常驻进程，以子进程写出的新心跳确认就绪）。
	RestartMode string `toml:"restart_mode"`
	// HeartbeatPath 是心跳文件路径（默认 state/fishd.heartbeat）。
	HeartbeatPath string `toml:"heartbeat_path"`
	// HistoryPath 是重启历史 JSONL（默认 state/watchdog-history.jsonl）。
	HistoryPath string `toml:"history_path"`
}

func defaultWatchdog() Watchdog {
	return Watchdog{
		Enabled:            false,
		IntervalSeconds:    60,
		StaleSeconds:       120,
		UnhealthyThreshold: 2,
		CooldownSeconds:    300,
		HeartbeatPath:      "state/fishd.heartbeat",
		HistoryPath:        "state/watchdog-history.jsonl",
	}
}

// Validate 只检查区间与必填项。
func (w Watchdog) Validate() error {
	if !w.Enabled {
		return nil
	}
	if w.IntervalSeconds < 5 || w.IntervalSeconds > 3600 {
		return errors.New("看门狗 interval_seconds 必须在 5 至 3600 秒之间")
	}
	if w.StaleSeconds < 10 || w.StaleSeconds > 3600 {
		return errors.New("看门狗 stale_seconds 必须在 10 至 3600 秒之间")
	}
	if w.UnhealthyThreshold < 1 || w.UnhealthyThreshold > 10 {
		return errors.New("看门狗 unhealthy_threshold 必须在 1 至 10 之间")
	}
	if w.CooldownSeconds < 0 || w.CooldownSeconds > 86400 {
		return errors.New("看门狗 cooldown_seconds 必须在 0 至 86400 秒之间")
	}
	if w.HeartbeatPath == "" {
		return errors.New("看门狗必须配置 heartbeat_path")
	}
	if strings.ContainsAny(w.HeartbeatPath, "\r\n\x00") {
		return errors.New("看门狗 heartbeat_path 不能包含控制字符")
	}
	if w.HistoryPath != "" && strings.ContainsAny(w.HistoryPath, "\r\n\x00") {
		return errors.New("看门狗 history_path 不能包含控制字符")
	}
	if strings.TrimSpace(w.RestartCommand) == "" {
		return errors.New("看门狗启用需要 restart_command")
	}
	// 模式必须显式选择：service-command 超时会终止命令，managed-child 要求新心跳；
	// 选错模式会误杀直接拉起的常驻进程，因此不提供静默默认。
	if w.RestartMode != "service-command" && w.RestartMode != "managed-child" {
		return errors.New("看门狗启用需要 restart_mode（service-command 或 managed-child）")
	}
	return nil
}
