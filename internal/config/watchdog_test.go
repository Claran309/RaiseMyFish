package config

// 本文件测 [watchdog] 的默认值、启用校验与安全边界。

import (
	"strings"
	"testing"
)

func TestWatchdogDefaultsOff(t *testing.T) {
	cfg, err := loadWithScheduler(t, "[log]\nlevel='info'\n")
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	w := cfg.Watchdog
	if w.Enabled {
		t.Error("看门狗默认应关闭")
	}
	if w.IntervalSeconds != 60 || w.StaleSeconds != 120 || w.UnhealthyThreshold != 2 || w.CooldownSeconds != 300 {
		t.Errorf("默认值不符：%+v", w)
	}
	if w.HeartbeatPath != "state/fishd.heartbeat" {
		t.Errorf("默认心跳路径 = %q", w.HeartbeatPath)
	}
}

func TestWatchdogValidEnabled(t *testing.T) {
	body := `
[watchdog]
enabled = true
interval_seconds = 30
stale_seconds = 90
unhealthy_threshold = 3
cooldown_seconds = 600
restart_command = "systemctl restart fishd"
restart_mode = "service-command"
heartbeat_path = "state/custom.heartbeat"
`
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	w := cfg.Watchdog
	if !w.Enabled || w.IntervalSeconds != 30 || w.StaleSeconds != 90 || w.UnhealthyThreshold != 3 ||
		w.CooldownSeconds != 600 || w.RestartCommand != "systemctl restart fishd" || w.HeartbeatPath != "state/custom.heartbeat" {
		t.Errorf("看门狗配置不符：%+v", w)
	}
	if w.RestartMode != "service-command" {
		t.Errorf("重启模式未保留：%q", w.RestartMode)
	}
}

// TestWatchdogAcceptsManagedChildMode 直接拉起常驻进程的配置必须能通过校验。
func TestWatchdogAcceptsManagedChildMode(t *testing.T) {
	body := "[watchdog]\nenabled=true\nrestart_command='bin/fishd'\nrestart_mode='managed-child'\n"
	cfg, err := loadWithScheduler(t, body)
	if err != nil {
		t.Fatalf("managed-child 配置应通过校验：%v", err)
	}
	if cfg.Watchdog.RestartMode != "managed-child" {
		t.Errorf("重启模式 = %q，期望 managed-child", cfg.Watchdog.RestartMode)
	}
}

func TestWatchdogRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"间隔过小", "[watchdog]\nenabled=true\nrestart_command='x'\ninterval_seconds=4\n", "interval_seconds"},
		{"间隔过大", "[watchdog]\nenabled=true\nrestart_command='x'\ninterval_seconds=3601\n", "interval_seconds"},
		{"过期阈值过小", "[watchdog]\nenabled=true\nrestart_command='x'\nstale_seconds=9\n", "stale_seconds"},
		{"过期阈值过大", "[watchdog]\nenabled=true\nrestart_command='x'\nstale_seconds=3601\n", "stale_seconds"},
		{"阈值过小", "[watchdog]\nenabled=true\nrestart_command='x'\nunhealthy_threshold=0\n", "unhealthy_threshold"},
		{"阈值过大", "[watchdog]\nenabled=true\nrestart_command='x'\nunhealthy_threshold=11\n", "unhealthy_threshold"},
		{"冷却为负", "[watchdog]\nenabled=true\nrestart_command='x'\ncooldown_seconds=-1\n", "cooldown_seconds"},
		{"冷却过大", "[watchdog]\nenabled=true\nrestart_command='x'\ncooldown_seconds=86401\n", "cooldown_seconds"},
		{"心跳路径为空", "[watchdog]\nenabled=true\nrestart_command='x'\nheartbeat_path=''\n", "heartbeat_path"},
		{"心跳路径控制字符", "[watchdog]\nenabled=true\nrestart_command='x'\nheartbeat_path=\"a\\nb\"\n", "控制字符"},
		{"缺重启命令", "[watchdog]\nenabled=true\n", "restart_command"},
		{"缺重启模式", "[watchdog]\nenabled=true\nrestart_command='x'\n", "restart_mode"},
		{"非法重启模式", "[watchdog]\nenabled=true\nrestart_command='x'\nrestart_mode='bogus'\n", "restart_mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadWithScheduler(t, tc.body); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

func TestWatchdogDisabledSkipsValidation(t *testing.T) {
	// 关闭时不要求 restart_command，也不校验区间。
	body := "[watchdog]\nenabled=false\ninterval_seconds=1\n"
	if _, err := loadWithScheduler(t, body); err != nil {
		t.Fatalf("关闭时不应做启用校验：%v", err)
	}
}
