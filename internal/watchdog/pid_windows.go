//go:build windows

package watchdog

// Windows：没有 signal 0，用 tasklist 过滤 pid 判断。
//
// 只在本机看门狗里调用，频率 60s 一次，开销可接受。

import (
	"os/exec"
	"strconv"
	"strings"
)

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
