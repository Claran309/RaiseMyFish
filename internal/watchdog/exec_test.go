package watchdog

// 本文件用真实子进程测重启命令的两种模式：
//   - service-command：短命命令必须在观察窗口内退出，超时失败并回收；
//   - managed-child：常驻子进程必须写出带自身 pid 的新心跳才算就绪，
//     没有证据就终止回收，不能把「仍在运行」当成功；
//   - 父 ctx 已取消时不启动新进程。

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestWatchdogSpawnHelper 作为 runCommand 拉起的假进程，只在子进程模式执行。
func TestWatchdogSpawnHelper(t *testing.T) {
	args := flag.Args()
	if len(args) < 3 {
		t.Skip("仅作为 runCommand 的子进程 helper")
	}
	pidFile, mode, heartbeatPath := args[0], args[1], args[2]
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(11)
	}
	writeHeartbeat := func(pid int) bool {
		if heartbeatPath == "-" || heartbeatPath == "" {
			return false
		}
		return WriteHeartbeat(heartbeatPath, pid, time.Now(), time.Now()) == nil
	}
	switch mode {
	case "fail":
		time.Sleep(50 * time.Millisecond)
		os.Exit(3)
	case "short":
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	case "ready":
		// 模拟 fishd 启动即写心跳（pid 必须是自己）。
		if !writeHeartbeat(os.Getpid()) {
			os.Exit(12)
		}
		time.Sleep(30 * time.Second)
	case "slow-ready":
		// 延迟写，用来证明看门狗不会拿启动前的旧心跳当就绪证据。
		time.Sleep(300 * time.Millisecond)
		if !writeHeartbeat(os.Getpid()) {
			os.Exit(12)
		}
		time.Sleep(30 * time.Second)
	case "wrong-pid":
		// 心跳新鲜但 pid 不是子进程：应被拒绝。
		if !writeHeartbeat(os.Getpid() + 100000) {
			os.Exit(12)
		}
		time.Sleep(30 * time.Second)
	default: // resident：常驻但不写心跳（挂死的启动脚本/进程）
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

// helperCommand 拼出「测试二进制 + helper 模式」的命令串。
func helperCommand(t *testing.T, pidFile, mode, heartbeatPath string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("取测试二进制路径：%v", err)
	}
	for _, path := range []string{exe, pidFile, heartbeatPath} {
		if path == "-" || path == "" {
			continue
		}
		if strings.ContainsAny(path, " \t") {
			t.Skip("路径含空白，命令按空格切分无法覆盖")
		}
	}
	return fmt.Sprintf("%s -test.run=^TestWatchdogSpawnHelper$ %s %s %s", exe, pidFile, mode, heartbeatPath)
}

// waitPIDFile 等子进程写出自己的 pid。
func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待子进程写 pid 文件超时：%s", path)
	return 0
}

// waitGone 等进程退出并被回收；Unix 上未 Wait 的僵尸仍会被 PidAlive 判为存活。
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !PidAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("进程 %d 退出后仍未被回收", pid)
}

// TestParseRestartMode 配置值只接受两种模式；空值按更安全的 service-command。
func TestParseRestartMode(t *testing.T) {
	cases := []struct {
		raw  string
		want RestartMode
		ok   bool
	}{
		{"", RestartModeServiceCommand, true},
		{" service-command ", RestartModeServiceCommand, true},
		{"managed-child", RestartModeManagedChild, true},
		{"managed_child", "", false},
		{"SERVICE-COMMAND", "", false},
	}
	for _, tc := range cases {
		got, err := ParseRestartMode(tc.raw)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("ParseRestartMode(%q) = %q, %v；期望 %q", tc.raw, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParseRestartMode(%q) 应报错，实际 %q", tc.raw, got)
		}
	}
}

// TestRunCommandServiceCommandReturnsExitError 短命命令非零退出按失败返回。
func TestRunCommandServiceCommandReturnsExitError(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := runCommand(ctx, RestartModeServiceCommand, helperCommand(t, filepath.Join(dir, "pid"), "fail", "-"), "", zap.NewNop())
	if err == nil {
		t.Fatal("命令非零退出应返回错误")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("应在命令退出后立即返回，实际 %s", elapsed)
	}
}

// TestRunCommandServiceCommandSuccess 短命命令正常退出并回收。
func TestRunCommandServiceCommandSuccess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := runCommand(ctx, RestartModeServiceCommand, helperCommand(t, pidFile, "short", "-"), "", zap.NewNop()); err != nil {
		t.Fatalf("短命命令正常退出不应报错：%v", err)
	}
	waitGone(t, waitPIDFile(t, pidFile))
}

// TestRunCommandServiceCommandTimeoutIsFailureAndReclaims 假卡死命令：
// 到观察超时仍未退出必须返回失败，并终止、Wait 回收，不能当成常驻启动成功。
func TestRunCommandServiceCommandTimeoutIsFailureAndReclaims(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := runCommand(ctx, RestartModeServiceCommand, helperCommand(t, pidFile, "resident", "-"), "", zap.NewNop())
	if err == nil {
		t.Fatal("命令超时未退出应返回失败，不能当作启动成功")
	}
	if !strings.Contains(err.Error(), "未在观察窗口内退出") {
		t.Fatalf("错误应说明超时未退出，实际 %v", err)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("应等满观察窗口再失败，实际 %s", elapsed)
	}
	pid := waitPIDFile(t, pidFile)
	if PidAlive(pid) {
		t.Fatalf("超时的服务管理命令 %d 应被终止", pid)
	}
	waitGone(t, pid)
}

// TestRunCommandCanceledBeforeStartDoesNotSpawn 父 ctx 已取消时不新启动进程。
func TestRunCommandCanceledBeforeStartDoesNotSpawn(t *testing.T) {
	modes := []RestartMode{RestartModeServiceCommand, RestartModeManagedChild}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			heartbeatPath := filepath.Join(dir, "fishd.heartbeat")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := runCommand(ctx, mode, helperCommand(t, pidFile, "resident", heartbeatPath), heartbeatPath, zap.NewNop())
			if err == nil || !strings.Contains(err.Error(), "未启动") {
				t.Fatalf("已取消的上下文应拒绝启动，实际 %v", err)
			}
			time.Sleep(150 * time.Millisecond)
			if _, statErr := os.Stat(pidFile); !errors.Is(statErr, os.ErrNotExist) {
				// 真启动了就把残留进程清掉，避免测试挂住。
				if pid := readPIDFile(pidFile); pid > 0 {
					_ = killPID(pid)
				}
				t.Fatalf("父 ctx 已取消却启动了进程（pid 文件存在）")
			}
		})
	}
}

// readPIDFile 读 pid 文件，失败返回 0（仅用于失败清理）。
func readPIDFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// TestRunCommandManagedChildReadyViaHeartbeat 常驻子进程写出带自身 pid 的新心跳后返回成功，
// 返回后进程必须继续存活（不能被 CommandContext 杀掉），退出时由后台 Wait 回收。
func TestRunCommandManagedChildReadyViaHeartbeat(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	heartbeatPath := filepath.Join(dir, "fishd.heartbeat")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := runCommand(ctx, RestartModeManagedChild, helperCommand(t, pidFile, "ready", heartbeatPath), heartbeatPath, zap.NewNop()); err != nil {
		t.Fatalf("写出新心跳后应确认就绪：%v", err)
	}
	pid := waitPIDFile(t, pidFile)
	t.Cleanup(func() {
		_ = killPID(pid)
		waitGone(t, pid)
	})
	if !PidAlive(pid) {
		t.Fatalf("确认就绪后子进程 %d 应继续运行", pid)
	}
	if got := readHeartbeatPID(t, heartbeatPath); got != pid {
		t.Fatalf("心跳 pid = %d，期望子进程 %d", got, pid)
	}
}

// TestRunCommandManagedChildRequiresNewHeartbeat 假常驻进程：
// 没有新心跳、心跳 pid 不对、或只有启动前的旧心跳，都不能算就绪，且必须终止回收。
func TestRunCommandManagedChildRequiresNewHeartbeat(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		prewrite bool
	}{
		{"无心跳", "resident", false},
		{"错误pid", "wrong-pid", false},
		{"旧心跳未更新", "resident", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			heartbeatPath := filepath.Join(dir, "fishd.heartbeat")
			if tc.prewrite {
				old := time.Now().Add(-5 * time.Minute)
				if err := WriteHeartbeat(heartbeatPath, 1, old, old); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(heartbeatPath, old, old); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := runCommand(ctx, RestartModeManagedChild, helperCommand(t, pidFile, tc.mode, heartbeatPath), heartbeatPath, zap.NewNop())
			if err == nil {
				t.Fatal("没有新心跳证据时不能返回成功")
			}
			if !strings.Contains(err.Error(), "未在观察窗口内写出新心跳") {
				t.Fatalf("错误应说明缺少就绪证据，实际 %v", err)
			}
			if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
				t.Fatalf("应等满观察窗口再失败，实际 %s", elapsed)
			}
			pid := waitPIDFile(t, pidFile)
			if PidAlive(pid) {
				t.Fatalf("未确认就绪的子进程 %d 应被终止", pid)
			}
			waitGone(t, pid)
		})
	}
}

// TestRunCommandManagedChildExitsBeforeReady 子进程在写出心跳前退出 → 失败并回收。
func TestRunCommandManagedChildExitsBeforeReady(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	heartbeatPath := filepath.Join(dir, "fishd.heartbeat")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := runCommand(ctx, RestartModeManagedChild, helperCommand(t, pidFile, "fail", heartbeatPath), heartbeatPath, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "确认就绪前退出") {
		t.Fatalf("就绪前退出应报错，实际 %v", err)
	}
	waitGone(t, waitPIDFile(t, pidFile))
}

// readHeartbeatPID 读心跳里的 pid（测试断言用）。
func readHeartbeatPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读心跳失败：%v", err)
	}
	var hb Heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		t.Fatalf("解析心跳失败：%v", err)
	}
	return hb.PID
}

// TestWatchdogManagedChildRestartConfirmsHeartbeat 走完整 Check → runCommand：
// 启动前的旧心跳不能算就绪，必须等新子进程写出带自身 pid 的心跳。
func TestWatchdogManagedChildRestartConfirmsHeartbeat(t *testing.T) {
	dir := t.TempDir()
	heartbeatPath := filepath.Join(dir, "fishd.heartbeat")
	old := time.Now().Add(-5 * time.Minute)
	if err := WriteHeartbeat(heartbeatPath, 1, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(heartbeatPath, old, old); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "pid")

	dog, err := New(Options{
		HeartbeatPath:      heartbeatPath,
		StaleAfter:         time.Minute,
		UnhealthyThreshold: 1,
		Cooldown:           time.Hour,
		RestartCommand:     helperCommand(t, pidFile, "slow-ready", heartbeatPath),
		RestartMode:        RestartModeManagedChild,
		RestartTimeout:     3 * time.Second,
		PIDAlive:           func(int) bool { return false },
		Kill:               func(int) error { return nil },
		Log:                zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res := dog.Check(context.Background())
	if !res.Restarted {
		t.Fatalf("子进程写出新心跳后应确认重启成功：%+v", res)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("旧心跳不能当成就绪证据，应等到新心跳（实际 %s）", elapsed)
	}

	pid := waitPIDFile(t, pidFile)
	t.Cleanup(func() {
		_ = killPID(pid)
		waitGone(t, pid)
	})
	if !PidAlive(pid) {
		t.Fatalf("看门狗重启返回后，常驻子进程 %d 应继续运行", pid)
	}
	if got := readHeartbeatPID(t, heartbeatPath); got != pid {
		t.Fatalf("心跳 pid = %d，期望新子进程 %d", got, pid)
	}
}

// TestWatchdogManagedChildFailureReclaimsAndCoolsDown 常驻子进程始终不写心跳：
// Check 必须按失败处理、终止回收，并进入冷却，不能在冷却后累积挂死进程。
func TestWatchdogManagedChildFailureReclaimsAndCoolsDown(t *testing.T) {
	dir := t.TempDir()
	heartbeatPath := filepath.Join(dir, "fishd.heartbeat")
	historyPath := filepath.Join(dir, "watchdog-history.jsonl")
	pidFile := filepath.Join(dir, "pid")
	clock := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	old := clock.Add(-5 * time.Minute)
	if err := WriteHeartbeat(heartbeatPath, 1, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(heartbeatPath, old, old); err != nil {
		t.Fatal(err)
	}

	dog, err := New(Options{
		HeartbeatPath:      heartbeatPath,
		HistoryPath:        historyPath,
		StaleAfter:         time.Minute,
		UnhealthyThreshold: 1,
		Cooldown:           10 * time.Minute,
		RestartCommand:     helperCommand(t, pidFile, "resident", heartbeatPath),
		RestartMode:        RestartModeManagedChild,
		RestartTimeout:     700 * time.Millisecond,
		Now:                func() time.Time { return clock },
		PIDAlive:           func(int) bool { return false },
		Kill:               func(int) error { return nil },
		Log:                zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	res := dog.Check(context.Background())
	if res.Restarted || res.Reason != "重启命令执行失败" {
		t.Fatalf("没有新心跳应判重启失败：%+v", res)
	}
	pid := waitPIDFile(t, pidFile)
	if PidAlive(pid) {
		t.Fatalf("启动失败的子进程 %d 应被终止", pid)
	}
	waitGone(t, pid)

	records, err := ReadHistory(historyPath, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].OK || records[0].Reason != "restart_failed" {
		t.Fatalf("应记录一次失败：%+v", records)
	}

	// 冷却期内不再执行命令，避免每轮都留下一个挂死进程。
	clock = clock.Add(time.Minute)
	res = dog.Check(context.Background())
	if res.Restarted || res.Reason != "冷却期内，暂不重启" {
		t.Fatalf("冷却期内应暂不重启：%+v", res)
	}
	records, err = ReadHistory(historyPath, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("冷却期内不应再次执行重启命令，历史条数 = %d", len(records))
	}

	// 冷却结束后再试一次：仍然失败并回收，不累积进程。
	clock = clock.Add(10 * time.Minute)
	res = dog.Check(context.Background())
	if res.Restarted || res.Reason != "重启命令执行失败" {
		t.Fatalf("冷却结束后应再次尝试并失败：%+v", res)
	}
	records, err = ReadHistory(historyPath, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].OK || records[0].Reason != "restart_failed" {
		t.Fatalf("应累计两次失败：%+v", records)
	}
	second := waitPIDFile(t, pidFile)
	if PidAlive(second) {
		t.Fatalf("第二次失败的子进程 %d 也应被终止", second)
	}
	waitGone(t, second)
}
