//go:build windows

package gift

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"
)

// TestLockContendedWindowsAccessDenied Windows 锁文件竞争会返回 ERROR_ACCESS_DENIED，
// 必须按可重试竞争处理，而不是立即报「账本被占用」。
func TestLockContendedWindowsAccessDenied(t *testing.T) {
	accessDenied := &fs.PathError{Op: "open", Path: "gift-ledger.json.lock", Err: syscall.ERROR_ACCESS_DENIED}
	if !errors.Is(accessDenied, fs.ErrPermission) {
		t.Fatalf("前提不成立：ERROR_ACCESS_DENIED 应映射为 fs.ErrPermission：%v", accessDenied)
	}
	if !lockContended(accessDenied) {
		t.Fatal("Windows Access Denied 应视为可重试的锁竞争")
	}

	exists := &fs.PathError{Op: "open", Path: "gift-ledger.json.lock", Err: syscall.ERROR_FILE_EXISTS}
	if !lockContended(exists) {
		t.Fatal("ERROR_FILE_EXISTS 应视为锁竞争")
	}
	if lockContended(errors.New("无关错误")) {
		t.Fatal("无关错误不应视为锁竞争")
	}
}
