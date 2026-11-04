package gift

// 本文件测跨进程每日额度账本：持久化、跨日重置、释放与并发。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func readLedger(t *testing.T, path string) quotaLedger {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ledger quotaLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestFileQuotaPersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "gift-ledger.json")
	first, err := NewFileQuota(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Reserve("2026-11-02", 1, 2); err != nil {
		t.Fatalf("首次预占失败：%v", err)
	}
	second, err := NewFileQuota(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Reserve("2026-11-02", 1, 2); err != nil {
		t.Fatalf("第二个实例应能继续使用额度：%v", err)
	}
	if ledger := readLedger(t, path); ledger.Day != "2026-11-02" || ledger.Used != 2 {
		t.Fatalf("账本不符：%+v", ledger)
	}

	third, _ := NewFileQuota(path)
	err = third.Reserve("2026-11-02", 1, 2)
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindFatal || typed.Message != "送礼超过每日限额" {
		t.Fatalf("跨实例限额未生效：%v", err)
	}
	if err := third.Reserve("2026-11-03", 1, 2); err != nil {
		t.Fatalf("跨日未重置：%v", err)
	}
	if ledger := readLedger(t, path); ledger.Day != "2026-11-03" || ledger.Used != 1 {
		t.Fatalf("跨日账本不符：%+v", ledger)
	}
}

func TestFileQuotaReleaseIsCrossInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gift-ledger.json")
	first, _ := NewFileQuota(path)
	if err := first.Reserve("2026-11-02", 2, 2); err != nil {
		t.Fatal(err)
	}
	second, _ := NewFileQuota(path)
	second.Release("2026-11-02", 1)
	if ledger := readLedger(t, path); ledger.Used != 1 {
		t.Fatalf("释放后 used = %d，期望 1", ledger.Used)
	}
	third, _ := NewFileQuota(path)
	if err := third.Reserve("2026-11-02", 1, 2); err != nil {
		t.Fatalf("释放出的额度应可再次使用：%v", err)
	}
	third.Release("2026-11-02", 5)
	if ledger := readLedger(t, path); ledger.Used != 0 {
		t.Fatalf("过度释放应夹到 0，实际 %d", ledger.Used)
	}
}

func TestFileQuotaCorruptLedgerRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gift-ledger.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	quota, _ := NewFileQuota(path)
	err := quota.Reserve("2026-11-02", 1, 10)
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindFatal || typed.Message != "送礼额度账本不可用" {
		t.Fatalf("损坏账本应拒绝发送：%v", err)
	}
}

func TestFileQuotaConcurrentReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gift-ledger.json")
	const limit = 3
	var success, limited int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		quota, err := NewFileQuota(path)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := quota.Reserve("2026-11-02", 1, limit)
			var typed *Error
			switch {
			case err == nil:
				atomic.AddInt32(&success, 1)
			case errors.As(err, &typed) && typed.Message == "送礼超过每日限额":
				atomic.AddInt32(&limited, 1)
			default:
				t.Errorf("意外错误：%v", err)
			}
		}()
	}
	wg.Wait()
	if success != limit || limited != 10-limit {
		t.Fatalf("并发额度失效：success=%d limited=%d", success, limited)
	}
	if ledger := readLedger(t, path); ledger.Used != limit {
		t.Fatalf("账本 used = %d，期望 %d", ledger.Used, limit)
	}
}

// TestFileQuotaConcurrentReservationsRepeated 多轮复跑 10 实例并发预占。
// Windows 上锁文件高频创建/删除会瞬时返回 Access Denied；旧实现把它当致命错误，
// 单轮测试约十分之一概率失败，多轮可稳定覆盖该竞争窗口。
func TestFileQuotaConcurrentReservationsRepeated(t *testing.T) {
	const (
		rounds = 20
		limit  = 5
	)
	for round := 0; round < rounds; round++ {
		path := filepath.Join(t.TempDir(), "gift-ledger.json")
		var success, limited int32
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			quota, err := NewFileQuota(path)
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func(round int) {
				defer wg.Done()
				err := quota.Reserve("2026-11-02", 1, limit)
				var typed *Error
				switch {
				case err == nil:
					atomic.AddInt32(&success, 1)
				case errors.As(err, &typed) && typed.Message == "送礼超过每日限额":
					atomic.AddInt32(&limited, 1)
				default:
					t.Errorf("第 %d 轮意外错误：%v", round, err)
				}
			}(round)
		}
		wg.Wait()
		if success != limit || limited != 10-limit {
			t.Fatalf("第 %d 轮并发额度失效：success=%d limited=%d", round, success, limited)
		}
		if ledger := readLedger(t, path); ledger.Used != limit {
			t.Fatalf("第 %d 轮账本 used = %d，期望 %d", round, ledger.Used, limit)
		}
	}
}

func TestNewFileQuotaRejectsBadPath(t *testing.T) {
	for _, path := range []string{"", "  ", "a\nb"} {
		if _, err := NewFileQuota(path); err == nil {
			t.Fatalf("非法路径未被拒绝：%q", path)
		}
	}
}

// TestSendUsesQuotaStoreAcrossSenders 一次性进程反复启动也不能突破额度。
func TestSendUsesQuotaStoreAcrossSenders(t *testing.T) {
	quota, err := NewFileQuota(filepath.Join(t.TempDir(), "gift-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	o := realOptions()
	o.DailyLimit = 1
	o.Quota = quota
	first, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	first.command = helperCommand("sent", nil)
	if result, err := first.Send(context.Background(), helperRoom, helperGift, 1); err != nil || !result.Sent {
		t.Fatalf("首次真实送礼失败：%+v %v", result, err)
	}

	second, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	var spawns int32
	second.command = helperCommand("sent", &spawns)
	result, err := second.Send(context.Background(), helperRoom, helperGift, 1)
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindFatal || typed.Message != "送礼超过每日限额" {
		t.Fatalf("第二个进程未被限额：%+v %v", result, err)
	}
	if atomic.LoadInt32(&spawns) != 0 {
		t.Fatal("限额拒绝后仍启动子进程")
	}
}

// TestQuotaStoreReleasesDefiniteFailures 确定未 POST 的失败要归还文件额度。
func TestQuotaStoreReleasesDefiniteFailures(t *testing.T) {
	quota, err := NewFileQuota(filepath.Join(t.TempDir(), "gift-ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	o := realOptions()
	o.DailyLimit = 1
	o.Quota = quota
	sender, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	sender.command = helperCommand("retryable", nil)
	if _, err := sender.Send(context.Background(), helperRoom, helperGift, 1); err == nil {
		t.Fatal("retryable 未返回错误")
	}
	sender.command = helperCommand("sent", nil)
	if result, err := sender.Send(context.Background(), helperRoom, helperGift, 1); err != nil || !result.Sent {
		t.Fatalf("释放额度后应可重试：%+v %v", result, err)
	}
}

// TestDryRunDoesNotTouchQuotaStore dry-run 不读写账本。
func TestDryRunDoesNotTouchQuotaStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gift-ledger.json")
	quota, err := NewFileQuota(path)
	if err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Quota = quota
	sender, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	sender.command = helperCommand("sent", nil)
	if result, err := sender.Send(context.Background(), helperRoom, helperGift, 1); err != nil || !result.Sent {
		t.Fatalf("dry-run 失败：%+v %v", result, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run 不应创建账本：%v", err)
	}
}
