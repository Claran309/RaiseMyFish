package watchdog

// Package watchdog 是 fishd 的看门狗核心：读心跳、判活、按阈值与冷却重启。
//
// 设计：
//   - 判定只看两件事——心跳文件是否过期（冻结）、心跳里的 pid 是否还活着（死亡）；
//   - 连续 unhealthy_threshold 次异常才重启，避免抖动；
//   - 重启后进入 cooldown，冷却期内不再重启（防风暴）；
//   - 所有外部动作（时钟、判活、重启）都可注入，测试不碰真实进程。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
)

// Verdict 是一次检查的结论。
type Verdict string

const (
	// VerdictHealthy 心跳新鲜且进程在。
	VerdictHealthy Verdict = "healthy"
	// VerdictFrozen 心跳过期（进程可能卡死）。
	VerdictFrozen Verdict = "frozen"
	// VerdictDead 心跳里的 pid 不存在。
	VerdictDead Verdict = "dead"
)

// Heartbeat 是心跳文件内容。
type Heartbeat struct {
	At  time.Time `json:"at"`
	PID int       `json:"pid"`
}

// Options 是看门狗参数。
type Options struct {
	// HeartbeatPath 是 fishd 写的心跳文件。
	HeartbeatPath string
	// Interval 是检查间隔（默认 60s）。
	Interval time.Duration
	// StaleAfter 是心跳过期阈值（默认 120s）。
	StaleAfter time.Duration
	// UnhealthyThreshold 是连续异常多少次才重启（默认 2）。
	UnhealthyThreshold int
	// Cooldown 是重启后的冷却时间（默认 300s）。
	Cooldown time.Duration
	// RestartCommand 是重启命令（如 systemctl restart fishd）。
	RestartCommand string
	// RestartTimeout 是重启命令的执行上限（默认 60s），防止命令挂死拖住看门狗。
	RestartTimeout time.Duration
	// Now 可注入时钟。
	Now func() time.Time
	// PIDAlive 可注入判活（默认走系统实现）。
	PIDAlive func(pid int) bool
	// Kill 可注入杀进程（默认真实发送终止信号）。
	Kill func(pid int) error
	// Restart 可注入重启动作（默认执行 RestartCommand）。
	Restart func(ctx context.Context, command string) error
	// Log 必填。
	Log *zap.Logger
}

// CheckResult 是一次检查的结果，便于日志与测试断言。
type CheckResult struct {
	Verdict Verdict
	// Consecutive 是当前连续异常次数。
	Consecutive int
	// Restarted 表示本次触发了重启。
	Restarted bool
	// Reason 是给人看的原因。
	Reason string
}

// Watchdog 是看门狗本体。
type Watchdog struct {
	opts Options

	consecutive int
	lastAttempt time.Time
	restarts    int
}

// New 校验参数并规范化默认值。
func New(o Options) (*Watchdog, error) {
	if o.Log == nil {
		return nil, errors.New("日志器不能为 nil")
	}
	if o.HeartbeatPath == "" {
		return nil, errors.New("心跳文件路径不能为空")
	}
	if o.Interval <= 0 {
		o.Interval = time.Minute
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 2 * time.Minute
	}
	if o.UnhealthyThreshold <= 0 {
		o.UnhealthyThreshold = 2
	}
	if o.Cooldown <= 0 {
		o.Cooldown = 5 * time.Minute
	}
	if o.RestartTimeout <= 0 {
		o.RestartTimeout = time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PIDAlive == nil {
		o.PIDAlive = PidAlive
	}
	if o.Kill == nil {
		o.Kill = killPID
	}
	if o.Restart == nil {
		o.Restart = runCommand
	}
	return &Watchdog{opts: o}, nil
}

// Check 做一次检查；必要时先杀旧进程再重启。
func (w *Watchdog) Check(ctx context.Context) CheckResult {
	verdict, pid, reason := w.inspect()
	if verdict == VerdictHealthy {
		w.consecutive = 0
		w.opts.Log.Info("看门狗检查健康",
			zap.String("action", "watchdog_healthy"),
			zap.Int("pid", pid),
		)
		return CheckResult{Verdict: verdict, Consecutive: 0, Reason: reason}
	}

	w.consecutive++
	res := CheckResult{Verdict: verdict, Consecutive: w.consecutive, Reason: reason}
	if w.consecutive < w.opts.UnhealthyThreshold {
		w.opts.Log.Warn("看门狗发现异常，等待下一次确认",
			zap.String("action", "watchdog_unhealthy"),
			zap.String("verdict", string(verdict)),
			zap.Int("pid", pid),
			zap.Int("consecutive", w.consecutive),
			zap.String("reason", reason),
		)
		return res
	}

	now := w.opts.Now()
	if !w.lastAttempt.IsZero() && now.Sub(w.lastAttempt) < w.opts.Cooldown {
		res.Reason = "冷却期内，暂不重启"
		w.opts.Log.Warn("看门狗处于冷却期",
			zap.String("action", "watchdog_cooldown"),
			zap.String("verdict", string(verdict)),
			zap.Int("pid", pid),
			zap.Duration("remaining", w.opts.Cooldown-now.Sub(w.lastAttempt)),
		)
		return res
	}

	if w.opts.RestartCommand == "" {
		res.Reason = "未配置 restart_command"
		w.opts.Log.Error("看门狗需要重启但未配置命令",
			zap.String("action", "watchdog_restart_missing"),
			zap.String("verdict", string(verdict)),
			zap.Int("pid", pid),
		)
		return res
	}

	// 冻结但进程还在：先终止旧进程，避免重启命令与旧实例抢端口/文件锁。
	if verdict == VerdictFrozen && pid > 0 && w.opts.PIDAlive(pid) {
		if err := w.opts.Kill(pid); err != nil {
			w.opts.Log.Warn("看门狗终止旧进程失败，继续尝试重启",
				zap.String("action", "watchdog_kill_failed"),
				zap.Int("pid", pid),
				zap.Error(err),
			)
		} else {
			w.opts.Log.Warn("看门狗已终止冻结的旧进程",
				zap.String("action", "watchdog_kill"),
				zap.Int("pid", pid),
			)
		}
	}

	restartCtx, cancel := context.WithTimeout(ctx, w.opts.RestartTimeout)
	err := w.opts.Restart(restartCtx, w.opts.RestartCommand)
	cancel()
	if err != nil {
		// 命令失败也进入冷却：否则每个检查周期都会反复执行失败的命令。
		w.lastAttempt = now
		res.Reason = "重启命令执行失败"
		w.opts.Log.Error("看门狗重启失败",
			zap.String("action", "watchdog_restart_failed"),
			zap.String("verdict", string(verdict)),
			zap.Int("pid", pid),
			zap.Error(err),
		)
		return res
	}

	w.lastAttempt = now
	w.consecutive = 0
	w.restarts++
	res.Restarted = true
	res.Reason = "已重启"
	w.opts.Log.Error("看门狗已重启 fishd",
		zap.String("action", "watchdog_restart"),
		zap.String("verdict", string(verdict)),
		zap.Int("pid", pid),
		zap.Int("restarts", w.restarts),
		zap.String("command", w.opts.RestartCommand),
	)
	return res
}

// inspect 读心跳并给出结论；pid 用于日志与杀旧进程，拿不到时为 0。
func (w *Watchdog) inspect() (Verdict, int, string) {
	info, err := os.Stat(w.opts.HeartbeatPath)
	if err != nil {
		return VerdictFrozen, 0, "心跳文件不存在"
	}
	age := w.opts.Now().Sub(info.ModTime())

	// 先读内容拿 pid：即使心跳过期，冻结的进程也可能还在，需要终止。
	var hb Heartbeat
	data, readErr := os.ReadFile(w.opts.HeartbeatPath)
	parseErr := error(nil)
	if readErr == nil {
		parseErr = json.Unmarshal(data, &hb)
	}
	pid := hb.PID
	if parseErr != nil || pid < 0 {
		pid = 0
	}

	if age > w.opts.StaleAfter {
		return VerdictFrozen, pid, fmt.Sprintf("心跳已过期 %s", age.Truncate(time.Second))
	}
	if readErr != nil {
		return VerdictFrozen, 0, "心跳文件不可读"
	}
	if parseErr != nil {
		return VerdictFrozen, 0, "心跳文件不是合法JSON"
	}
	if hb.PID <= 0 {
		return VerdictFrozen, 0, "心跳缺少 pid"
	}
	if !w.opts.PIDAlive(hb.PID) {
		return VerdictDead, hb.PID, fmt.Sprintf("pid %d 不存在", hb.PID)
	}
	return VerdictHealthy, hb.PID, "心跳正常"
}

// Run 按间隔循环，直到 ctx 取消。
func (w *Watchdog) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("上下文不能为 nil")
	}
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	w.opts.Log.Info("看门狗已启动",
		zap.String("action", "watchdog_start"),
		zap.Duration("interval", w.opts.Interval),
		zap.Duration("stale_after", w.opts.StaleAfter),
		zap.Int("threshold", w.opts.UnhealthyThreshold),
	)
	// 启动先查一次，不必等满一个周期
	w.Check(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.Check(ctx)
		}
	}
}

// WriteHeartbeat 原子写心跳（临时文件 + rename）。
func WriteHeartbeat(path string, pid int, now time.Time) error {
	dir := dirOf(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建心跳目录：%w", err)
	}
	payload, err := json.Marshal(Heartbeat{At: now.UTC(), PID: pid})
	if err != nil {
		return errors.New("编码心跳失败")
	}
	tmp, err := os.CreateTemp(dir, ".heartbeat-*.tmp")
	if err != nil {
		return fmt.Errorf("创建心跳临时文件：%w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入心跳：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭心跳文件：%w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("替换心跳文件：%w", err)
	}
	return nil
}
