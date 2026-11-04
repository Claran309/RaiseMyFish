package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFreezeStateLifecycle 冻结标记：不存在为未冻结，创建/删除均幂等。
func TestFreezeStateLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "memory.frozen")

	frozen, err := IsFrozen(path)
	if err != nil || frozen {
		t.Fatalf("初始状态 = %v, err=%v；期望未冻结且无错误", frozen, err)
	}

	if err := Freeze(path); err != nil {
		t.Fatalf("冻结失败：%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("冻结标记未创建：%v", err)
	}
	if frozen, err := IsFrozen(path); err != nil || !frozen {
		t.Fatalf("冻结后状态 = %v, err=%v；期望已冻结", frozen, err)
	}

	// 重复冻结保持幂等，不因已存在而报错
	if err := Freeze(path); err != nil {
		t.Fatalf("重复冻结失败：%v", err)
	}

	if err := Unfreeze(path); err != nil {
		t.Fatalf("解冻失败：%v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("解冻后标记仍存在：%v", err)
	}
	if frozen, err := IsFrozen(path); err != nil || frozen {
		t.Fatalf("解冻后状态 = %v, err=%v；期望未冻结", frozen, err)
	}

	// 重复解冻也要幂等，方便运维脚本无脑重跑
	if err := Unfreeze(path); err != nil {
		t.Fatalf("重复解冻失败：%v", err)
	}
}

// TestFreezeRejectsEmptyPath 空路径必须报错，避免误写当前目录。
func TestFreezeRejectsEmptyPath(t *testing.T) {
	if _, err := IsFrozen("  "); err == nil {
		t.Error("IsFrozen 空路径应报错")
	}
	if err := Freeze(""); err == nil {
		t.Error("Freeze 空路径应报错")
	}
	if err := Unfreeze(" "); err == nil {
		t.Error("Unfreeze 空路径应报错")
	}
}
