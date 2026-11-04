package watchdog

// 本文件放进程相关的平台无关部分：目录推导与命令执行。

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func runCommand(ctx context.Context, command string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	return cmd.Run()
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
