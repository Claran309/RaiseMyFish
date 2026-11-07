package watchdog

// 本文件放进程相关的平台无关部分：目录推导与命令执行。

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// dirOf 取父目录；空路径返回 "."。
func dirOf(path string) string {
	dir := filepath.Dir(path)
	if dir == "" {
		return "."
	}
	return dir
}

// runCommand 执行重启命令；按空格切分（足够应付 systemctl / 脚本）。
//
// 不经过 shell，避免命令注入；需要 shell 特性时请写脚本。
//
// 不能用 CommandContext 等待：restart_command 常常是直接启动常驻 fishd，
// 超时取消会杀死刚拉起的服务（自愈变自杀）。这里 Start 后最多等待一个宽限期
// （由调用方通过 ctx 截止时间给出）：
//   - 宽限内退出：短命服务管理命令，按退出码返回成功/失败；
//   - 宽限后仍在运行：按常驻进程启动成功处理，立即脱离；
//   - 后台 Wait 持续等待该进程退出并回收资源，避免僵尸/句柄泄漏。
func runCommand(ctx context.Context, command string, log *zap.Logger) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
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

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// 恰好已退出的短命令仍按退出码处理，避免与宽限到点竞争。
		select {
		case err := <-done:
			return err
		default:
		}
		// 宽限期到点仍在运行：常驻服务已经拉起，不再等待、绝不杀它。
		if log != nil {
			log.Info("看门狗拉起的命令仍在运行，按常驻进程脱离",
				zap.String("action", "watchdog_spawn"),
				zap.Int("pid", pid),
			)
		}
		go func() {
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
		}()
		return nil
	}
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
