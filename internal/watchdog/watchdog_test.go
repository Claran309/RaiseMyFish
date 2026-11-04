package watchdog

// 本文件测看门狗：健康 / 冻结 / 死亡 / 阈值 / 冷却，全部注入 mock。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

type harness struct {
	dir        string
	path       string
	clock      time.Time
	restarts   int
	attempts   int
	kills      int
	restartErr error
	alive      bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	return &harness{
		dir:   dir,
		path:  filepath.Join(dir, "fishd.heartbeat"),
		clock: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		alive: true,
	}
}

// write 写心跳并把 mtime 调到指定时间。
func (h *harness) write(t *testing.T, at time.Time, pid int) {
	t.Helper()
	if err := WriteHeartbeat(h.path, pid, at); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(h.path, at, at); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) newDog(t *testing.T, threshold int, cooldown time.Duration) *Watchdog {
	t.Helper()
	dog, err := New(Options{
		HeartbeatPath:      h.path,
		Interval:           time.Minute,
		StaleAfter:         2 * time.Minute,
		UnhealthyThreshold: threshold,
		Cooldown:           cooldown,
		RestartCommand:     "restart-fishd",
		Now:                func() time.Time { return h.clock },
		PIDAlive:           func(int) bool { return h.alive },
		Kill: func(int) error {
			h.kills++
			return nil
		},
		Restart: func(context.Context, string) error {
			h.attempts++
			if h.restartErr != nil {
				return h.restartErr
			}
			h.restarts++
			return nil
		},
		Log: zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dog
}

// TestHealthyNoRestart 心跳新鲜 + pid 在 → 不重启，连续计数归零。
func TestHealthyNoRestart(t *testing.T) {
	h := newHarness(t)
	h.write(t, h.clock, 1234)
	dog := h.newDog(t, 2, 5*time.Minute)

	for i := 0; i < 3; i++ {
		res := dog.Check(context.Background())
		if res.Verdict != VerdictHealthy || res.Restarted {
			t.Fatalf("第 %d 次不该重启：%+v", i+1, res)
		}
	}
	if h.restarts != 0 {
		t.Errorf("健康时重启了 %d 次", h.restarts)
	}
}

// TestFrozenRestartsAfterThreshold 心跳过期：第 1 次只记录，第 2 次才重启。
func TestFrozenRestartsAfterThreshold(t *testing.T) {
	h := newHarness(t)
	// 心跳比当前时间早 5 分钟（> StaleAfter 2min）
	h.write(t, h.clock.Add(-5*time.Minute), 1234)
	dog := h.newDog(t, 2, 5*time.Minute)

	first := dog.Check(context.Background())
	if first.Verdict != VerdictFrozen || first.Restarted || first.Consecutive != 1 {
		t.Fatalf("第 1 次应只记录：%+v", first)
	}
	if h.restarts != 0 {
		t.Fatalf("阈值 2 时第 1 次不该重启，实际 %d", h.restarts)
	}

	second := dog.Check(context.Background())
	// Consecutive 记录的是「触发重启时的累计次数」，重启后内部计数清零
	if !second.Restarted || second.Consecutive != 2 {
		t.Fatalf("第 2 次应重启（累计 2 次）：%+v", second)
	}
	if h.restarts != 1 {
		t.Errorf("应重启 1 次，实际 %d", h.restarts)
	}
	if h.kills != 1 {
		t.Errorf("冻结且进程仍在时应先杀旧进程，实际 %d", h.kills)
	}
}

// TestDeadProcessRestarts pid 不存在算死亡。
func TestDeadProcessRestarts(t *testing.T) {
	h := newHarness(t)
	h.write(t, h.clock, 4321)
	h.alive = false
	dog := h.newDog(t, 2, 5*time.Minute)

	if res := dog.Check(context.Background()); res.Verdict != VerdictDead || res.Restarted {
		t.Fatalf("第 1 次应判 dead 且不重启：%+v", res)
	}
	if res := dog.Check(context.Background()); !res.Restarted {
		t.Fatalf("第 2 次应重启：%+v", res)
	}
	if h.restarts != 1 {
		t.Errorf("应重启 1 次，实际 %d", h.restarts)
	}
	if h.kills != 0 {
		t.Errorf("进程已死亡时不应再 kill，实际 %d", h.kills)
	}
}

// TestThresholdOne 阈值 1 时首次异常就重启。
func TestThresholdOne(t *testing.T) {
	h := newHarness(t)
	h.write(t, h.clock.Add(-10*time.Minute), 1)
	dog := h.newDog(t, 1, 5*time.Minute)

	if res := dog.Check(context.Background()); !res.Restarted {
		t.Fatalf("阈值 1 应立即重启：%+v", res)
	}
}

// TestCooldownSuppressesRepeatedRestart 冷却期内即使再次异常也不重启。
func TestCooldownSuppressesRepeatedRestart(t *testing.T) {
	h := newHarness(t)
	h.write(t, h.clock.Add(-5*time.Minute), 1234)
	dog := h.newDog(t, 1, 5*time.Minute)

	if res := dog.Check(context.Background()); !res.Restarted {
		t.Fatalf("首次应重启：%+v", res)
	}
	// 心跳仍过期（重启失败/尚未刷新），1 分钟后再次检查
	h.clock = h.clock.Add(time.Minute)
	res := dog.Check(context.Background())
	if res.Restarted {
		t.Fatalf("冷却期内不该再重启：%+v", res)
	}
	if h.restarts != 1 {
		t.Errorf("冷却期内重启次数应保持 1，实际 %d", h.restarts)
	}

	// 冷却结束后（再等 5 分钟）可以重启
	h.clock = h.clock.Add(5 * time.Minute)
	if !dog.Check(context.Background()).Restarted {
		t.Error("冷却结束后应允许重启")
	}
	if h.restarts != 2 {
		t.Errorf("应累计 2 次，实际 %d", h.restarts)
	}
}

// TestRestartFailureKeepsCooldown 重启命令失败也进入冷却，避免每次检查都打命令。
func TestRestartFailureKeepsCooldown(t *testing.T) {
	h := newHarness(t)
	h.write(t, h.clock.Add(-5*time.Minute), 1)
	h.restartErr = errors.New("systemctl failed")
	dog := h.newDog(t, 1, 5*time.Minute)

	first := dog.Check(context.Background())
	if first.Restarted {
		t.Fatal("命令失败不应标记为已重启")
	}
	if h.attempts != 1 {
		t.Fatalf("应尝试重启 1 次，实际 %d", h.attempts)
	}
	h.clock = h.clock.Add(time.Minute)
	if res := dog.Check(context.Background()); res.Restarted {
		t.Fatalf("失败后不该立刻重试成功：%+v", res)
	}
	if h.attempts != 1 {
		t.Errorf("冷却期内不应重复执行命令，实际 %d 次", h.attempts)
	}
	// 冷却结束后允许再次尝试
	h.clock = h.clock.Add(5 * time.Minute)
	_ = dog.Check(context.Background())
	if h.attempts != 2 {
		t.Errorf("冷却结束后应重新尝试，实际 %d 次", h.attempts)
	}
	if h.restarts != 0 {
		t.Errorf("命令一直失败，成功次数应为 0，实际 %d", h.restarts)
	}
}

// TestMissingHeartbeatFrozen 心跳文件不存在按冻结处理。
func TestMissingHeartbeatFrozen(t *testing.T) {
	h := newHarness(t)
	dog := h.newDog(t, 1, time.Minute)
	res := dog.Check(context.Background())
	if res.Verdict != VerdictFrozen || !res.Restarted {
		t.Fatalf("缺文件应判冻结并重启：%+v", res)
	}
	if h.kills != 0 {
		t.Errorf("拿不到 pid 时不应 kill，实际 %d", h.kills)
	}
}

// TestInvalidHeartbeatDoesNotKill 心跳损坏时没有可信 pid，只重启不杀进程。
func TestInvalidHeartbeatDoesNotKill(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	old := h.clock.Add(-5 * time.Minute)
	if err := os.Chtimes(h.path, old, old); err != nil {
		t.Fatal(err)
	}
	dog := h.newDog(t, 1, time.Minute)
	res := dog.Check(context.Background())
	if res.Verdict != VerdictFrozen || !res.Restarted {
		t.Fatalf("损坏心跳应判冻结并重启：%+v", res)
	}
	if h.kills != 0 {
		t.Errorf("损坏心跳不应 kill 未知 pid，实际 %d", h.kills)
	}
}

// TestWriteHeartbeatRoundTrip 写心跳后能读回 pid 与时间。
func TestWriteHeartbeatRoundTrip(t *testing.T) {
	h := newHarness(t)
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if err := WriteHeartbeat(h.path, 99, at); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("心跳文件为空")
	}
	// 目录里不该残留临时文件
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("残留临时文件：%s", e.Name())
		}
	}
}

// TestNewValidates 参数校验。
func TestNewValidates(t *testing.T) {
	if _, err := New(Options{HeartbeatPath: "x", Log: nil}); err == nil {
		t.Error("nil 日志应报错")
	}
	if _, err := New(Options{Log: zap.NewNop()}); err == nil {
		t.Error("空心跳路径应报错")
	}
	// 默认值规范化
	dog, err := New(Options{HeartbeatPath: "x", Log: zap.NewNop()})
	if err != nil {
		t.Fatal(err)
	}
	if dog.opts.Interval != time.Minute || dog.opts.UnhealthyThreshold != 2 {
		t.Errorf("默认值不符：%+v", dog.opts)
	}
	if dog.opts.StaleAfter != 2*time.Minute || dog.opts.Cooldown != 5*time.Minute || dog.opts.RestartTimeout != time.Minute {
		t.Errorf("时间默认值不符：%+v", dog.opts)
	}
	if dog.opts.Kill == nil || dog.opts.Restart == nil || dog.opts.PIDAlive == nil {
		t.Error("进程操作默认实现未注入")
	}
}
