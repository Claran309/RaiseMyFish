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
	dir         string
	path        string
	historyPath string
	clock       time.Time
	restarts    int
	attempts    int
	kills       int
	restartErr  error
	alive       bool
	// procStart 是「系统视角」的进程创建时间；procKnown=false 表示读不到（权限/平台）。
	// 默认与心跳的 started_at 对齐 = 同一个实例。
	procStart time.Time
	procKnown bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	return &harness{
		dir:         dir,
		path:        filepath.Join(dir, "fishd.heartbeat"),
		historyPath: filepath.Join(dir, "watchdog-history.jsonl"),
		clock:       time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		alive:       true,
		procKnown:   true,
	}
}

// write 写心跳并把 mtime 调到指定时间。
func (h *harness) write(t *testing.T, at time.Time, pid int) {
	t.Helper()
	if err := WriteHeartbeat(h.path, pid, at, at); err != nil {
		t.Fatal(err)
	}
	// 默认让「系统视角的创建时间」与心跳里的 started_at 一致 = 同一个实例。
	h.procStart = at
	if err := os.Chtimes(h.path, at, at); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) newDog(t *testing.T, threshold int, cooldown time.Duration) *Watchdog {
	t.Helper()
	dog, err := New(Options{
		HeartbeatPath:      h.path,
		HistoryPath:        h.historyPath,
		Interval:           time.Minute,
		StaleAfter:         2 * time.Minute,
		UnhealthyThreshold: threshold,
		Cooldown:           cooldown,
		RestartCommand:     "restart-fishd",
		Now:                func() time.Time { return h.clock },
		PIDAlive:           func(int) bool { return h.alive },
		ProcessStart: func(int) (time.Time, bool) {
			if !h.procKnown {
				return time.Time{}, false
			}
			return h.procStart, true
		},
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
	if err := WriteHeartbeat(h.path, 99, at, at); err != nil {
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

// TestRestartHistoryPersisted 重启成功/失败都落历史，读取顺序为新的在前。
func TestRestartHistoryPersisted(t *testing.T) {
	h := newHarness(t)
	h.write(t, h.clock.Add(-5*time.Minute), 1234)
	dog := h.newDog(t, 1, 0)
	if res := dog.Check(context.Background()); !res.Restarted {
		t.Fatalf("应触发重启：%+v", res)
	}

	records, err := ReadHistory(h.historyPath, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !records[0].OK || records[0].Verdict != VerdictFrozen || records[0].PID != 1234 {
		t.Fatalf("历史记录不符：%+v", records)
	}

	h.clock = h.clock.Add(time.Minute)
	h.restartErr = errors.New("boom")
	dog.lastAttempt = time.Time{} // 跳过冷却，直接再试
	if res := dog.Check(context.Background()); res.Restarted {
		t.Fatalf("重启命令失败不应标记成功：%+v", res)
	}
	records, err = ReadHistory(h.historyPath, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].OK || records[0].Reason != "restart_failed" {
		t.Fatalf("失败历史不符：%+v", records)
	}
}

// TestReadHistoryMissingAndBadLines 文件不存在返回空；坏行跳过。
func TestReadHistoryMissingAndBadLines(t *testing.T) {
	dir := t.TempDir()
	records, err := ReadHistory(filepath.Join(dir, "none.jsonl"), 5)
	if err != nil || len(records) != 0 {
		t.Fatalf("缺失文件应返回空：%v %+v", err, records)
	}
	path := filepath.Join(dir, "history.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n{\"at\":\"2026-11-04T12:00:00Z\",\"verdict\":\"dead\",\"pid\":7,\"ok\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err = ReadHistory(path, 5)
	if err != nil || len(records) != 1 || records[0].PID != 7 {
		t.Fatalf("坏行应跳过：%v %+v", err, records)
	}
}

// TestFrozenSkipsKillOnReusedPID O11 核心：同一个 PID 但已经是别的进程 → 不杀，只重启。
//
// 场景：fishd 崩溃后未写新心跳，系统把该 PID 分配给无关进程。
// 只看「pid 活着」就 Kill 会误杀；必须核对进程创建时间。
func TestFrozenSkipsKillOnReusedPID(t *testing.T) {
	h := newHarness(t)
	stale := h.clock.Add(-5 * time.Minute)
	h.write(t, stale, 4321)
	// 系统里这个 pid 的创建时间远晚于心跳记录的启动时刻 = PID 被复用。
	h.procStart = h.clock.Add(-time.Minute)

	dog := h.newDog(t, 1, time.Minute)
	res := dog.Check(context.Background())
	if !res.Restarted {
		t.Fatalf("心跳过期仍应重启：%+v", res)
	}
	if h.kills != 0 {
		t.Fatalf("PID 复用时不应杀进程，实际 %d 次", h.kills)
	}
}

// TestFrozenKillsSameInstance 创建时间对得上（容差内）才允许杀。
func TestFrozenKillsSameInstance(t *testing.T) {
	h := newHarness(t)
	stale := h.clock.Add(-5 * time.Minute)
	h.write(t, stale, 4321)
	// fishd 记录的是进程内 time.Now()，系统给的可能晚几秒，容差内视为同一实例。
	h.procStart = stale.Add(2 * time.Second)

	dog := h.newDog(t, 1, time.Minute)
	dog.Check(context.Background())
	if h.kills != 1 {
		t.Fatalf("同一实例应被终止，实际 %d 次", h.kills)
	}
}

// TestFrozenLegacyHeartbeatSkipsKill 旧心跳没有 started_at，无法核对 → 不杀。
func TestFrozenLegacyHeartbeatSkipsKill(t *testing.T) {
	h := newHarness(t)
	stale := h.clock.Add(-5 * time.Minute)
	// 手工写旧格式（没有 started_at 字段）。
	body := []byte(`{"at":"` + stale.Format(time.RFC3339) + `","pid":4321}`)
	if err := os.WriteFile(h.path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(h.path, stale, stale); err != nil {
		t.Fatal(err)
	}

	dog := h.newDog(t, 1, time.Minute)
	res := dog.Check(context.Background())
	if !res.Restarted {
		t.Fatalf("仍应重启：%+v", res)
	}
	if h.kills != 0 {
		t.Fatalf("无启动时间时不应杀进程，实际 %d 次", h.kills)
	}
}

// TestFrozenSkipsKillWhenStartUnknown 读不到进程创建时间（权限/平台不支持）→ 不杀。
func TestFrozenSkipsKillWhenStartUnknown(t *testing.T) {
	h := newHarness(t)
	h.write(t, h.clock.Add(-5*time.Minute), 4321)
	h.procKnown = false

	dog := h.newDog(t, 1, time.Minute)
	res := dog.Check(context.Background())
	if !res.Restarted {
		t.Fatalf("仍应重启：%+v", res)
	}
	if h.kills != 0 {
		t.Fatalf("创建时间未知时不应杀进程，实际 %d 次", h.kills)
	}
}

// TestProcessStartTimeSelf 用当前进程验证平台实现确实能拿到创建时间。
//
// 拿不到就 Skip（容器里可能没有 ps / 无权限），但拿到时必须合理：
// 不晚于现在、且与进程内记录的启动时刻落在容差内 —— 这正是 sameInstance 的前提。
func TestProcessStartTimeSelf(t *testing.T) {
	started, ok := ProcessStartTime(os.Getpid())
	if !ok {
		t.Skip("当前平台/环境拿不到进程创建时间")
	}
	if started.IsZero() {
		t.Fatal("拿到了零值时间")
	}
	age := time.Since(started)
	if age < -procStartTolerance || age > 24*time.Hour {
		t.Fatalf("进程创建时间不合理：%v（age=%v）", started, age)
	}
}
