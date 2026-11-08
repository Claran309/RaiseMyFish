//go:build !windows

package watchdog

// Unix：signal 0 只检查进程是否存在，不真的发信号。
//
// 进程创建时间用 `ps -o lstart=` 取：Linux 与 macOS 都支持，
// 且不像 /proc 那样只限 Linux。强制 C locale，避免月份名被本地化成中文。

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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

// psStartLayout 是 `ps -o lstart=` 的输出格式（例：Mon Jan  2 15:04:05 2006）。
const psStartLayout = "Mon Jan _2 15:04:05 2006"

// ProcessStartTime 返回进程创建时间；拿不到（进程不存在/ps 不可用）时 ok=false。
func ProcessStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, false
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation(psStartLayout, text, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
