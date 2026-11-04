//go:build !windows

package watchdog

// Unix：signal 0 只检查进程是否存在，不真的发信号。

import "syscall"

// PidAlive 判断进程是否存在（EPERM 也算存在）。
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}
