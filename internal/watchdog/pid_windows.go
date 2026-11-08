//go:build windows

package watchdog

// Windows：没有 signal 0，用 tasklist 过滤 pid 判断。
//
// 进程创建时间走 kernel32!GetProcessTimes：tasklist 只给内存/CPU，
// 拿不到创建时间就无法识别 PID 复用，那看门狗就只能「不确定不杀」。

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// processQueryLimitedInformation 是 OpenProcess 的最小权限（Vista+）。
const processQueryLimitedInformation = 0x1000

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess     = kernel32.NewProc("OpenProcess")
	procGetProcessTimes = kernel32.NewProc("GetProcessTimes")
	procCloseHandle     = kernel32.NewProc("CloseHandle")
)

// filetime 是 Windows FILETIME：自 1601-01-01 起的 100 纳秒数。
type filetime struct {
	Low  uint32
	High uint32
}

// unixNano 换算成 Unix 纳秒。
func (f filetime) unixNano() int64 {
	ticks := int64(f.High)<<32 | int64(f.Low)
	if ticks <= 0 {
		return 0
	}
	// 1601-01-01 → 1970-01-01 相差 11644473600 秒。
	const epochDiff = 116444736000000000
	return (ticks - epochDiff) * 100
}

// PidAlive 判断进程是否存在。
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		// 拿不到就当作活着，避免误杀正在跑的服务
		return true
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

// ProcessStartTime 返回进程创建时间；进程不存在或无权限时 ok=false。
func ProcessStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	handle, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return time.Time{}, false
	}
	defer procCloseHandle.Call(handle)
	var creation, exit, kernel, user filetime
	ret, _, _ := procGetProcessTimes.Call(
		handle,
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ret == 0 {
		return time.Time{}, false
	}
	nanos := creation.unixNano()
	if nanos <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, nanos), true
}
