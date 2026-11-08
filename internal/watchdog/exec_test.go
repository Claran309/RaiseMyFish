package watchdog

// 本文件用真实子进程测 O02：重启命令不能因宽限期到点杀死常驻进程，且必须正确回收。

import (
	"context"
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

// TestWatchdogSpawnHelper 作为 runCommand 拉起的假常驻进程，只在子进程模式执行。
func TestWatchdogSpawnHelper(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 {
		t.Skip("仅作为 runCommand 的子进程 helper")
	}
	pidFile, mode := args[0], args[1]
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(11)
	}
	switch mode {
	case "fail":
		time.Sleep(50 * time.Millisecond)
		os.Exit(3)
	case "short":
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	default:
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}

// helperCommand 拼出「测试二进制 + helper 模式」的命令串。
func helperCommand(t *testing.T, pidFile, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("取测试二进制路径：%v", err)
	}
	if strings.ContainsAny(exe, " \t") {
		t.Skip("测试二进制路径含空白，命令按空格切分无法覆盖")
	}
	return fmt.Sprintf("%s -test.run=^TestWatchdogSpawnHelper$ %s %s", exe, pidFile, mode)
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

// TestRunCommandKeepsResidentAlivePastTimeout 常驻进程必须跨过宽限期继续运行。
func TestRunCommandKeepsResidentAlivePastTimeout(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := runCommand(ctx, helperCommand(t, pidFile, "resident"), zap.NewNop()); err != nil {
		t.Fatalf("常驻进程启动失败：%v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("应等满宽限期再脱离，实际 %s", elapsed)
	}

	pid := waitPIDFile(t, pidFile)
	t.Cleanup(func() {
		_ = killPID(pid)
		waitGone(t, pid)
	})
	if !PidAlive(pid) {
		t.Fatalf("跨过 RestartTimeout 后常驻进程 %d 应仍存活", pid)
	}
}

// TestRunCommandReapsResidentAfterExit 宽限后脱离的子进程退出时必须被 Wait 回收。
func TestRunCommandReapsResidentAfterExit(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := runCommand(ctx, helperCommand(t, pidFile, "short"), zap.NewNop()); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	pid := waitPIDFile(t, pidFile)
	// helper 300ms 后自行退出；没有后台 Wait 时会留下僵尸。
	waitGone(t, pid)
}

// TestRunCommandReturnsEarlyExitError 宽限内退出的短命命令仍按退出码报错。
func TestRunCommandReturnsEarlyExitError(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := runCommand(ctx, helperCommand(t, filepath.Join(dir, "pid"), "fail"), zap.NewNop())
	if err == nil {
		t.Fatal("命令宽限内非零退出应返回错误")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("应在命令退出后立即返回，实际 %s", elapsed)
	}
}

// TestWatchdogRestartKeepsResidentAlivePastTimeout 走完整 Check → RestartTimeout → runCommand：
// 重启返回后新进程仍活着，证明超时没有杀死它。
func TestWatchdogRestartKeepsResidentAlivePastTimeout(t *testing.T) {
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
		RestartCommand:     helperCommand(t, pidFile, "resident"),
		RestartTimeout:     150 * time.Millisecond,
		PIDAlive:           func(int) bool { return false },
		Kill:               func(int) error { return nil },
		Log:                zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}

	res := dog.Check(context.Background())
	if !res.Restarted {
		t.Fatalf("应触发重启：%+v", res)
	}
	pid := waitPIDFile(t, pidFile)
	t.Cleanup(func() {
		_ = killPID(pid)
		waitGone(t, pid)
	})
	if !PidAlive(pid) {
		t.Fatalf("看门狗重启返回后，常驻进程 %d 应跨过 RestartTimeout 继续运行", pid)
	}
}
