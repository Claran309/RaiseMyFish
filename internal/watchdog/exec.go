package watchdog

// 本文件放进程相关的平台无关部分：目录推导与命令执行。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// RestartMode 指定 restart_command 的语义。
type RestartMode string

const (
	// RestartModeServiceCommand 是短命管理命令（systemctl/启动脚本）：
	// 必须在观察窗口内退出；超时视为失败，终止并回收，不能当作常驻启动成功。
	RestartModeServiceCommand RestartMode = "service-command"
	// RestartModeManagedChild 是直接拉起的常驻子进程（如 fishd 本体）：
	// 只有观察到该子进程写出的新心跳才算启动成功。
	RestartModeManagedChild RestartMode = "managed-child"
)

const (
	// readyPollInterval 是 managed-child 检查心跳证据的间隔。
	readyPollInterval = 100 * time.Millisecond
	// reclaimWait 是终止子进程后等待 Wait 回收的上限。
	reclaimWait = 5 * time.Second
)

// ParseRestartMode 解析配置值；空值按 service-command 处理（不会把「仍在运行」当成功）。
func ParseRestartMode(raw string) (RestartMode, error) {
	switch mode := RestartMode(strings.TrimSpace(raw)); mode {
	case "":
		return RestartModeServiceCommand, nil
	case RestartModeServiceCommand, RestartModeManagedChild:
		return mode, nil
	default:
		return "", fmt.Errorf("重启命令模式 %q 无效（可选 %s / %s）", raw, RestartModeServiceCommand, RestartModeManagedChild)
	}
}

// dirOf 取父目录；空路径返回 "."。
func dirOf(path string) string {
	dir := filepath.Dir(path)
	if dir == "" {
		return "."
	}
	return dir
}

// runCommand 执行重启命令，按模式区分短命管理命令与常驻子进程。
//
// 不用 exec.CommandContext 等待：managed-child 模式下超时取消会杀死刚拉起的服务。
// 改为 Start 后自己观察：
//   - service-command：命令必须在 ctx 截止前退出；超时视为失败，终止并 Wait 回收；
//   - managed-child：命令是常驻子进程，必须观察到它写出的新心跳才返回成功；
//     超时仍无证据则终止并回收，避免每轮冷却都留下一个挂死进程与 Wait goroutine。
//
// 父 ctx 在启动前已取消时直接返回，不新启动任何进程。
func runCommand(ctx context.Context, mode RestartMode, command, heartbeatPath string, log *zap.Logger) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("上下文已结束，未启动重启命令：%w", err)
	}
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return errors.New("重启命令为空")
	}
	if mode == "" {
		mode = RestartModeServiceCommand
	}
	// managed-child 的就绪证据以启动前的心跳为基线。
	var baseline heartbeatState
	if mode == RestartModeManagedChild {
		if heartbeatPath == "" {
			return errors.New("managed-child 模式需要心跳文件路径")
		}
		baseline = readHeartbeatState(heartbeatPath)
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	// 常驻子进程不继承看门狗的 stdio，避免占住父进程的日志管道。
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if mode == RestartModeManagedChild {
		return waitManagedChild(ctx, cmd, done, pid, heartbeatPath, baseline, log)
	}
	return waitServiceCommand(ctx, cmd, done, pid, log)
}

// waitServiceCommand 等待短命命令退出；观察窗口结束仍未退出按失败处理并回收。
func waitServiceCommand(ctx context.Context, cmd *exec.Cmd, done <-chan error, pid int, log *zap.Logger) error {
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// 恰好已退出的短命令仍按退出码处理，避免与窗口结束竞争。
		select {
		case err := <-done:
			return err
		default:
		}
	}

	killErr := cmd.Process.Kill()
	exitErr, reclaimed := waitResult(done, reclaimWait)
	if reclaimed && killErr != nil {
		// 进程在窗口边缘已自行退出（Kill 报“进程已结束”）：按真实退出码返回。
		return exitErr
	}
	if log != nil {
		fields := []zap.Field{
			zap.String("action", "watchdog_command_timeout"),
			zap.Int("pid", pid),
			zap.Error(ctx.Err()),
		}
		if killErr != nil {
			fields = append(fields, zap.NamedError("kill_error", killErr))
		}
		log.Warn("看门狗的服务管理命令未在观察窗口内退出，已终止", fields...)
	}
	if killErr != nil {
		return fmt.Errorf("服务管理命令未在观察窗口内退出，终止失败：%w", errors.Join(ctx.Err(), killErr))
	}
	if !reclaimed {
		return fmt.Errorf("服务管理命令未在观察窗口内退出，终止后 %s 内未回收：%w", reclaimWait, ctx.Err())
	}
	return fmt.Errorf("服务管理命令未在观察窗口内退出，已终止并回收：%w", ctx.Err())
}

// waitManagedChild 等待常驻子进程写出新心跳；只有证据成立才返回成功。
func waitManagedChild(ctx context.Context, cmd *exec.Cmd, done <-chan error, pid int, heartbeatPath string, baseline heartbeatState, log *zap.Logger) error {
	ticker := time.NewTicker(readyPollInterval)
	defer ticker.Stop()
	ready := func() bool { return heartbeatReady(heartbeatPath, baseline, pid) }

	for {
		if ready() {
			confirmManagedChild(done, pid, log)
			return nil
		}
		select {
		case err := <-done:
			return exitBeforeReady(pid, err)
		case <-ctx.Done():
			// 窗口结束时再查一次，避免与最后一次心跳写入竞争。
			if ready() {
				confirmManagedChild(done, pid, log)
				return nil
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return stopUnreadyChild(cmd, done, pid, log, ctx.Err())
			}
			// 父上下文取消（看门狗退出）：不杀可能已经健康的服务，后台继续等待回收。
			go reapManagedChild(done, pid, log)
			return fmt.Errorf("父上下文取消，未确认常驻子进程就绪：%w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// confirmManagedChild 记录就绪并脱离：后台 Wait 持续等待退出，避免僵尸/句柄泄漏。
func confirmManagedChild(done <-chan error, pid int, log *zap.Logger) {
	if log != nil {
		log.Info("看门狗拉起的常驻子进程已确认就绪",
			zap.String("action", "watchdog_spawn_ready"),
			zap.Int("pid", pid),
		)
	}
	go reapManagedChild(done, pid, log)
}

// stopUnreadyChild 终止观察窗口内没有就绪证据的子进程并回收。
func stopUnreadyChild(cmd *exec.Cmd, done <-chan error, pid int, log *zap.Logger, cause error) error {
	killErr := cmd.Process.Kill()
	exitErr, reclaimed := waitResult(done, reclaimWait)
	if reclaimed && killErr != nil {
		// 进程在窗口边缘已自行退出：按“就绪前退出”处理。
		return exitBeforeReady(pid, exitErr)
	}
	if log != nil {
		fields := []zap.Field{
			zap.String("action", "watchdog_ready_timeout"),
			zap.Int("pid", pid),
			zap.Error(cause),
		}
		if killErr != nil {
			fields = append(fields, zap.NamedError("kill_error", killErr))
		}
		log.Warn("看门狗拉起的常驻子进程未确认就绪，已终止", fields...)
	}
	if killErr != nil {
		return fmt.Errorf("常驻子进程未在观察窗口内写出新心跳，终止失败：%w", errors.Join(cause, killErr))
	}
	if !reclaimed {
		return fmt.Errorf("常驻子进程未在观察窗口内写出新心跳，终止后 %s 内未回收：%w", reclaimWait, cause)
	}
	return fmt.Errorf("常驻子进程未在观察窗口内写出新心跳，已终止并回收：%w", cause)
}

// reapManagedChild 等待常驻子进程退出并记录；调用方已经不再关心它。
func reapManagedChild(done <-chan error, pid int, log *zap.Logger) {
	err := <-done
	if log == nil {
		return
	}
	fields := []zap.Field{
		zap.String("action", "watchdog_spawn_exit"),
		zap.Int("pid", pid),
	}
	if err != nil {
		log.Warn("看门狗拉起的常驻进程异常退出", append(fields, zap.Error(err))...)
		return
	}
	log.Info("看门狗拉起的常驻进程已退出", fields...)
}

// exitBeforeReady 描述常驻子进程在确认就绪前退出的错误。
func exitBeforeReady(pid int, err error) error {
	if err == nil {
		return fmt.Errorf("常驻子进程在确认就绪前退出（pid %d）", pid)
	}
	return fmt.Errorf("常驻子进程在确认就绪前退出（pid %d）：%w", pid, err)
}

// waitResult 在 wait 内等待 Wait 结果；返回退出错误与是否等到。
func waitResult(done <-chan error, wait time.Duration) (error, bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case err := <-done:
		return err, true
	case <-timer.C:
		return nil, false
	}
}

// heartbeatState 是启动前的心跳基线。
type heartbeatState struct {
	exists bool
	mod    time.Time
	at     time.Time
}

// readHeartbeatState 读取当前心跳；文件缺失或损坏时 exists=false 或只保留 mtime。
func readHeartbeatState(path string) heartbeatState {
	info, err := os.Stat(path)
	if err != nil {
		return heartbeatState{}
	}
	state := heartbeatState{exists: true, mod: info.ModTime()}
	data, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	var hb Heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return state
	}
	state.at = hb.At
	return state
}

// heartbeatReady 判断心跳是否是 childPID 在启动之后新写出的。
//
// 只认 pid 匹配的子进程心跳：旧进程（含冻结未杀掉的实例）继续写心跳不会误判成功。
// 与启动前基线比较 mtime 或内容时间，避免「文件存在」被当成新证据。
func heartbeatReady(path string, baseline heartbeatState, childPID int) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var hb Heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return false
	}
	if hb.PID != childPID {
		return false
	}
	if !baseline.exists {
		return true
	}
	if info.ModTime().After(baseline.mod) {
		return true
	}
	return !hb.At.IsZero() && hb.At.After(baseline.at)
}

// killPID 终止指定进程；调用方已确认它存在。
func killPID(pid int) error {
	if pid <= 0 {
		return errors.New("pid 无效")
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
