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
	"io"
	"os"
	"strings"
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
	// StartedAt 是写入方进程的启动时刻（O11）。
	//
	// 看门狗用它核对「这个 pid 现在还是不是原来那个 fishd」：
	// 进程崩溃后 PID 会被系统复用，只判断存活就去 Kill 会误杀无关进程。
	// 旧版本写的心跳没有该字段 → 无法核对 → 不杀（只重启）。
	StartedAt time.Time `json:"started_at,omitempty"`
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
	// RestartCommand 是重启命令（如 systemctl restart fishd，或直接启动常驻 fishd）。
	RestartCommand string
	// HistoryPath 是重启历史 JSONL 文件；空表示不落盘。
	HistoryPath string
	// RestartTimeout 是重启动作的观察上限（默认 60s）。
	// 短命管理命令在此之内返回；常驻进程超过后按启动成功脱离，不再等待或终止。
	RestartTimeout time.Duration
	// Now 可注入时钟。
	Now func() time.Time
	// PIDAlive 可注入判活（默认走系统实现）。
	PIDAlive func(pid int) bool
	// Kill 可注入杀进程（默认真实发送终止信号）。
	Kill func(pid int) error
	// ProcessStart 可注入「读取进程创建时间」（默认走系统实现）。
	// 返回 false 表示拿不到 —— 此时一律不杀。
	ProcessStart func(pid int) (time.Time, bool)
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
	if o.ProcessStart == nil {
		o.ProcessStart = ProcessStartTime
	}
	if o.Restart == nil {
		o.Restart = func(ctx context.Context, command string) error {
			return runCommand(ctx, command, o.Log)
		}
	}
	return &Watchdog{opts: o}, nil
}

// Check 做一次检查；必要时先杀旧进程再重启。
func (w *Watchdog) Check(ctx context.Context) CheckResult {
	verdict, pid, startedAt, reason := w.inspect()
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
	//
	// O11：只判断「pid 活着」就杀会误杀——崩溃后 PID 会被系统复用。
	// 必须先确认它还是心跳里那个实例（启动时间对得上）；对不上或无法核对一律不杀。
	if verdict == VerdictFrozen && pid > 0 && w.opts.PIDAlive(pid) {
		if reason, ok := w.sameInstance(pid, startedAt); !ok {
			w.opts.Log.Warn("看门狗跳过终止：进程身份无法确认（可能是 PID 复用）",
				zap.String("action", "watchdog_kill_skipped"),
				zap.Int("pid", pid),
				zap.String("reason", reason),
			)
		} else if err := w.opts.Kill(pid); err != nil {
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
		w.record(RestartRecord{At: now.UTC(), Verdict: verdict, PID: pid, OK: false, Restarts: w.restarts, Reason: "restart_failed"})
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
	w.record(RestartRecord{At: now.UTC(), Verdict: verdict, PID: pid, OK: true, Restarts: w.restarts})
	w.opts.Log.Error("看门狗已重启 fishd",
		zap.String("action", "watchdog_restart"),
		zap.String("verdict", string(verdict)),
		zap.Int("pid", pid),
		zap.Int("restarts", w.restarts),
		zap.String("command", w.opts.RestartCommand),
	)
	return res
}

// procStartTolerance 是「同一进程」的启动时间容差。
//
// fishd 记录的启动时刻来自进程内 time.Now()，操作系统记的创建时间可能差几十毫秒；
// 5 秒足够吸收这点误差，又远小于 PID 被复用所需的完整进程生命周期。
const procStartTolerance = 5 * time.Second

// sameInstance 判断 pid 现在还是不是心跳里那个进程。
//
// 三个条件全满足才算「同一个」；任何一条不满足都返回 false —— **不确定就不杀**：
//   - 心跳里带 started_at（旧心跳没有，没法核对）；
//   - 能拿到系统视角的进程创建时间（权限/平台不支持时拿不到）；
//   - 两者相差不超过容差。
func (w *Watchdog) sameInstance(pid int, startedAt time.Time) (string, bool) {
	if startedAt.IsZero() {
		return "心跳缺少 started_at，无法核对进程身份", false
	}
	actual, ok := w.opts.ProcessStart(pid)
	if !ok || actual.IsZero() {
		return "读取进程创建时间失败", false
	}
	diff := actual.Sub(startedAt)
	if diff < 0 {
		diff = -diff
	}
	if diff > procStartTolerance {
		return fmt.Sprintf("进程创建时间相差 %s（疑为 PID 复用）", diff.Truncate(time.Second)), false
	}
	return "", true
}

// inspect 读心跳并给出结论；pid 用于日志与杀旧进程，拿不到时为 0。
func (w *Watchdog) inspect() (Verdict, int, time.Time, string) {
	info, err := os.Stat(w.opts.HeartbeatPath)
	if err != nil {
		return VerdictFrozen, 0, time.Time{}, "心跳文件不存在"
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
		return VerdictFrozen, pid, hb.StartedAt, fmt.Sprintf("心跳已过期 %s", age.Truncate(time.Second))
	}
	if readErr != nil {
		return VerdictFrozen, 0, time.Time{}, "心跳文件不可读"
	}
	if parseErr != nil {
		return VerdictFrozen, 0, time.Time{}, "心跳文件不是合法JSON"
	}
	if hb.PID <= 0 {
		return VerdictFrozen, 0, time.Time{}, "心跳缺少 pid"
	}
	if !w.opts.PIDAlive(hb.PID) {
		return VerdictDead, hb.PID, hb.StartedAt, fmt.Sprintf("pid %d 不存在", hb.PID)
	}
	return VerdictHealthy, hb.PID, hb.StartedAt, "心跳正常"
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

// RestartRecord 是一次重启尝试的历史记录。
type RestartRecord struct {
	At       time.Time `json:"at"`
	Verdict  Verdict   `json:"verdict"`
	PID      int       `json:"pid"`
	OK       bool      `json:"ok"`
	Restarts int       `json:"restarts"`
	Reason   string    `json:"reason,omitempty"`
}

// record 追加一条重启历史；写失败只告警，不影响重启流程。
func (w *Watchdog) record(rec RestartRecord) {
	if w.opts.HistoryPath == "" {
		return
	}
	if err := AppendHistory(w.opts.HistoryPath, rec); err != nil {
		w.opts.Log.Warn("看门狗重启历史写入失败",
			zap.String("action", "watchdog_history"),
			zap.Error(err),
		)
	}
}

// AppendHistory 追加一条 JSONL 历史记录（目录自动创建，文件权限 0600）。
func AppendHistory(path string, rec RestartRecord) error {
	if dir := dirOf(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("创建看门狗历史目录：%w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开看门狗历史：%w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		_ = file.Close()
		return errors.New("编码看门狗历史失败")
	}
	data = append(data, '\n')
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("写看门狗历史：%w", err)
	}
	return nil
}

// ReadHistory 读取最近 limit 条重启记录（新的在前）。
//
// 只读文件尾部 256KB，避免历史文件无限增长时把内存吃满；
// 文件不存在返回空列表，不视为错误。
func ReadHistory(path string, limit int) ([]RestartRecord, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("打开看门狗历史：%w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取看门狗历史大小：%w", err)
	}
	const maxTail = 256 << 10
	offset := int64(0)
	if info.Size() > maxTail {
		offset = info.Size() - maxTail
	}
	if _, err := file.Seek(offset, 0); err != nil {
		return nil, fmt.Errorf("定位看门狗历史：%w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTail))
	if err != nil {
		return nil, fmt.Errorf("读取看门狗历史：%w", err)
	}
	lines := strings.Split(string(data), "\n")
	out := make([]RestartRecord, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var rec RestartRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // 截断的半行或坏行直接跳过
		}
		out = append(out, rec)
	}
	return out, nil
}

// WriteHeartbeat 原子写心跳（临时文件 + rename）。
//
// startedAt 是写入方进程的启动时刻，看门狗用它识别 PID 复用；
// 零值表示「不提供」，此时看门狗不会杀任何进程（只重启）。
func WriteHeartbeat(path string, pid int, startedAt, now time.Time) error {
	dir := dirOf(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建心跳目录：%w", err)
	}
	payload, err := json.Marshal(Heartbeat{At: now.UTC(), PID: pid, StartedAt: startedAt.UTC()})
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
