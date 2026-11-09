// Package extractworker 实现 0.9b 的记忆提炼 worker。
//
// 职责：定时扫描已完成的（status=done）事件 → 调 memory.Extract 提炼候选 →
// 经既有 store.MemoryRepo.InsertRecord 落 memory_records → 推进水位。
//
// 边界：
//   - 不调用模型、不做语义理解，规则全在 memory.Extract。
//   - 不碰 SQL：读写都走注入的接口（DoneSource / store.MemoryRepo）。
//   - 幂等靠记录主键：非事实类由 event_id + kind + title 派生，事实类由
//     「可见域 + 主体 + 标题」派生；写入层用 ON CONFLICT 更新，且只允许
//     同范围、更晚事件覆盖，公共域与私密域不会互相改写。
//   - 隐私边界 fail-closed：未授权的私密事件在拼上下文、调提炼器之前直接跳过；
//     是否授权由调用方的 AllowPrivate 决定，默认拒绝。
package extractworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
)

// 默认参数。
const (
	// defaultBatchSize 是单次拉取的事件条数。
	defaultBatchSize = 50
	// maxBatchSize 防止调用方传过大的 limit 把库里一次捞爆。
	maxBatchSize = 500
	// maxJobClaimSize 是任务队列单次领取上限（C1/N06）。
	//
	// 任务领取后串行处理，一批太大时队尾会在轮到之前就租约到期，
	// 被第二个 worker 抢走后重做；按实际容量小批领取，让租约只覆盖
	// 真正在手里的工作。
	maxJobClaimSize = 10
	// jobRenewDivisor 决定续租频率：每 TTL/3 续一次，
	// 单次网络抖动或调度延迟不会直接让租约过期。
	jobRenewDivisor = 3
	// defaultScanInterval 是定时扫描间隔；phase-0 计划里写的是「先每 5 分钟扫一次」。
	defaultScanInterval = 5 * time.Minute
)

// Cursor 是水位：已处理到的「事件时间 + 事件 ID」位置。
//
// 排序键取 (At, ID)：At 相同时用 ID 兜底，保证扫描顺序稳定、不漏不重。
// 零值表示「从头开始」。
type Cursor struct {
	// At 事件时间。
	At time.Time
	// ID 事件幂等键。
	ID string
}

// IsZero 判断是否为零水位（还没处理过任何事件）。
func (c Cursor) IsZero() bool { return c.At.IsZero() && c.ID == "" }

// equal 判断两个游标是否指向同一位置。
func (c Cursor) equal(other Cursor) bool { return c.At.Equal(other.At) && c.ID == other.ID }

// DoneSource 拉取「已完成且排在游标之后」的事件，按 (At, ID) 升序返回。
//
// 实现方负责过滤 status=done 与排序；worker 不写 SQL、不猜平台字段。
// 返回空切片表示没有积压。
type DoneSource interface {
	ListDoneAfter(ctx context.Context, after Cursor, limit int) ([]event.Event, error)
}

// Watermark 保存与读取提炼水位。
type Watermark interface {
	Load(ctx context.Context) (Cursor, error)
	Save(ctx context.Context, c Cursor) error
}

// Options 是 worker 的行为参数。
type Options struct {
	// OwnerUID 是主人的可信平台 UID：只有发言人 UID 与之相等才算主人自述，
	// 昵称可被改名冒充，不参与身份判定。
	OwnerUID string
	// OwnerName 主人昵称，仅用于 LLM 提炼的语义提示。
	OwnerName string
	// AllowPrivate 为 true 时才提炼私密事件；默认 false（fail-closed）。
	AllowPrivate bool
	// Frozen 为 true 时整轮跳过：不扫描、不写库、不推进水位（配置级冻结）。
	Frozen bool
	// FreezePath 是运行时冻结状态文件；非空时每轮检查，文件存在即冻结。
	// 配置级 Frozen 优先，删除状态文件不会覆盖它。
	FreezePath string
	// Extractor 是提炼器；nil 时用规则版（memory.NewRuleExtractor）。
	// 传 LLMExtractor 时按触发策略走模型，失败内部回落规则版。
	Extractor memory.Extractor
	// BatchSize 单次拉取条数，<=0 用默认值，超过上限会被夹到上限。
	// 任务队列路径单次领取还会再夹到 maxJobClaimSize（执行中续租，队尾不排队过期）。
	BatchSize int
	// ScanInterval 定时扫描间隔，<=0 用 5 分钟。
	ScanInterval time.Duration
	// Jobs 是任务队列（M05）。非 nil 时走「入队→领取→完成」路径；
	// 为 nil 时保留旧的水位扫描路径（兼容尚未迁移任务表的部署与测试）。
	Jobs JobSource
	// JobOwner 是本进程的租约标识（建议 hostname+pid），<=0 时用默认值。
	JobOwner string
	// JobTTL 是单条任务的租约时长，<=0 用 2 分钟。
	JobTTL time.Duration
	// MaxAttempts 是任务最大尝试次数（含首次），<=0 用 5。
	MaxAttempts int
	// Source 覆盖 memory_sources 的来源 id；为空时按候选类型推导。
	Source string
	// Now 注入时钟，仅测试需要；为空时用 time.Now。
	Now func() time.Time
	// Profiles 是观众档案重建依赖；nil 表示不跑这个子任务。
	Profiles ProfileSource
	// ProfileInterval 是档案重建节流间隔，<=0 用 1 小时。
	ProfileInterval time.Duration
	// ProfileMinHits 建档门槛，<=0 用 2。
	ProfileMinHits int
	// ProfileRecentTexts / ProfileRecentReplies 是档案保留条数。
	ProfileRecentTexts   int
	ProfileRecentReplies int
	// ProfileActiveWindow 是「直播活跃」判定窗口，<=0 用 5 分钟。
	ProfileActiveWindow time.Duration
	// Recent 提供同 session 最近事件，用于给 LLM 提炼喂上下文；nil 则不喂。
	Recent RecentEventsSource
	// ContextLimit 是喂给提炼器的上下文条数，<=0 用 5。
	ContextLimit int
	// Soul 是灵魂记忆重建依赖；nil 表示不跑。
	Soul SoulSource
	// SoulOpts 是灵魂记忆参数（路径/条数/门槛/间隔）。
	SoulOpts SoulOptions
}

// RecentEventsSource 提供某会话最近的事件（database.EventRepo 满足）。
//
// Scope 显式传入：公开事件只读公开上下文，私密事件只读本会话私密上下文。
type RecentEventsSource interface {
	RecentEvents(ctx context.Context, scope memory.Scope, limit int) ([]event.Event, error)
}

// RecentBeforeSource 是「带时间上界」的最近上下文查询（database.EventRepo 满足）。
//
// M07：补提炼历史事件时，上下文必须以**被处理事件**为上界。
// 只实现 RecentEvents 的来源仍可用，worker 会在客户端剔除不早于当前事件的行，
// 但那样会白白占掉配额（取 6 条里有 4 条是未来的，实际只剩 2 条），
// 所以生产装配必须提供本方法。
type RecentBeforeSource interface {
	RecentEventsBefore(ctx context.Context, scope memory.Scope, at time.Time, id string, limit int) ([]event.Event, error)
}

// ProfileSource 是观众档案重建所需的最小能力（由 database.ViewerRepo 实现）。
type ProfileSource interface {
	// ActiveSince 判断 since 之后是否有入向事件。
	ActiveSince(ctx context.Context, since time.Time) (bool, error)
	// RebuildProfiles 从 events 全量重算档案。
	RebuildProfiles(ctx context.Context, opts store.ViewerRebuildOptions) (store.ViewerRebuildStats, error)
}

// Stats 是一轮扫描的统计，交给上层打日志/日报。
type Stats struct {
	// Scanned 扫描到的事件条数。
	Scanned int
	// Extracted 产出候选的条数（一条事件可能产出多条）。
	Extracted int
	// Inserted 真正写库成功的条数。
	Inserted int
	// Duplicated 命中主键冲突（幂等去重）的条数。
	Duplicated int
	// Skipped 没提炼出候选（含私密未授权、空文本、无规则命中、时间缺失）的事件条数。
	Skipped int
	// Failed 处理失败、已回队等待重试（或已置 failed）的任务条数。
	Failed int
	// LeaseLost 本轮失租的任务条数：提炼期间租约被抢占/过期，
	// 结果已放弃写入，由当前持有者重做。失租不是失败，不计入 Failed。
	LeaseLost int
	// Enqueued 本轮新入队的任务条数（任务队列模式）。
	Enqueued int
	// ProfilesRebuilt 本轮重建的档案数（-1 表示本轮跳过）。
	ProfilesRebuilt int
	// ProfileSkipped 是跳过重建的原因（throttled / idle / none）。
	ProfileSkipped string
	// SoulEntries 本轮写入的灵魂条目数（-1 表示跳过）。
	SoulEntries int
	// SoulSkipped 是跳过原因（throttled / failed / none）。
	SoulSkipped string
	// Frozen 表示本轮因冻结被跳过。
	Frozen bool
	// FrozenReason 是冻结来源（config / state_file）。
	FrozenReason string
}

// Worker 是提炼 worker 本体。
type Worker struct {
	source DoneSource
	repo   store.MemoryRepo
	wm     Watermark
	opts   Options
	// jobs 非 nil 时走任务队列模式（M05），source/wm 仅作兼容保留
	jobs JobSource
	// 档案重建节流状态（进程内，重启后立刻允许跑一次）
	lastProfile time.Time
	// 灵魂记忆重建节流状态
	lastSoul time.Time
}

// New 构造 worker，并规范化参数。
func New(source DoneSource, repo store.MemoryRepo, wm Watermark, opts Options) (*Worker, error) {
	if source == nil {
		return nil, errors.New("事件来源不能为 nil")
	}
	if repo == nil {
		return nil, errors.New("记忆仓库不能为 nil")
	}
	if wm == nil {
		return nil, errors.New("水位存储不能为 nil")
	}

	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultBatchSize
	}
	if opts.BatchSize > maxBatchSize {
		opts.BatchSize = maxBatchSize
	}
	if opts.ScanInterval <= 0 {
		opts.ScanInterval = defaultScanInterval
	}
	if opts.ProfileInterval <= 0 {
		opts.ProfileInterval = time.Hour
	}
	if opts.ProfileMinHits <= 0 {
		opts.ProfileMinHits = 2
	}
	if opts.ProfileRecentTexts <= 0 {
		opts.ProfileRecentTexts = 15
	}
	if opts.ProfileRecentReplies <= 0 {
		opts.ProfileRecentReplies = 10
	}
	if opts.ProfileActiveWindow <= 0 {
		opts.ProfileActiveWindow = 5 * time.Minute
	}
	if opts.Extractor == nil {
		opts.Extractor = memory.NewRuleExtractor()
	}
	if opts.ContextLimit <= 0 {
		opts.ContextLimit = 5
	}
	if opts.SoulOpts.Interval <= 0 {
		opts.SoulOpts.Interval = time.Hour
	}
	if strings.TrimSpace(opts.JobOwner) == "" {
		opts.JobOwner = "extract-worker"
	}
	if opts.JobTTL <= 0 {
		opts.JobTTL = 2 * time.Minute
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	opts.Source = strings.TrimSpace(opts.Source)

	return &Worker{source: source, repo: repo, wm: wm, jobs: opts.Jobs, opts: opts}, nil
}

// Run 按 ScanInterval 定时扫描，直到 ctx 被取消。
//
// 取消属于正常停止，返回 nil；单轮出错则立刻返回错误，交给上层决定是否重启 worker。
func (w *Worker) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("ctx 不能为 nil")
	}
	ticker := time.NewTicker(w.opts.ScanInterval)
	defer ticker.Stop()

	for {
		if _, err := w.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce 跑一轮：从水位开始按批扫描直到没有积压，然后落一次水位。
//
// 出错时的水位策略：只保存「已成功处理到」的位置，绝不越过失败事件，
// 这样重启后能从那一条补上，而不是丢事件。
func (w *Worker) RunOnce(ctx context.Context) (Stats, error) {
	var stats Stats

	if ctx == nil {
		return stats, errors.New("ctx 不能为 nil")
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}

	// 冻结检查必须早于读水位：本轮不消费任何事件，水位保持原样，
	// 解冻后从冻结点继续，既不丢事件也不会把积压当成已处理。
	frozen, reason, err := w.frozen()
	if err != nil {
		return stats, err
	}
	if frozen {
		stats.Frozen = true
		stats.FrozenReason = reason
		return stats, nil
	}

	// 任务队列模式（M05）：入队 → 领取 → 完成/失败。
	// 它不看事件是否已完成，也不使用事件时间水位。
	if w.jobs != nil {
		return w.runJobs(ctx, &stats)
	}

	start, err := w.wm.Load(ctx)
	if err != nil {
		return stats, fmt.Errorf("读取提炼水位：%w", err)
	}

	cursor := start
	for {
		batch, err := w.source.ListDoneAfter(ctx, cursor, w.opts.BatchSize)
		if err != nil {
			// 已经处理过的部分照样落水位，避免整轮白干
			if saveErr := w.saveIfAdvanced(ctx, start, cursor); saveErr != nil {
				return stats, errors.Join(fmt.Errorf("扫描待提炼事件：%w", err), saveErr)
			}
			return stats, fmt.Errorf("扫描待提炼事件：%w", err)
		}
		if len(batch) == 0 {
			break
		}

		for _, ev := range batch {
			stats.Scanned++
			next, err := w.handleEvent(ctx, ev, &stats)
			if err != nil {
				// 不推进到失败事件，保留重试机会
				if saveErr := w.saveIfAdvanced(ctx, start, cursor); saveErr != nil {
					return stats, errors.Join(err, saveErr)
				}
				return stats, err
			}
			cursor = next
		}
	}

	if err := w.saveIfAdvanced(ctx, start, cursor); err != nil {
		return stats, err
	}
	w.rebuildProfiles(ctx, &stats)
	w.rebuildSoul(ctx, &stats)
	return stats, nil
}

// maxJobBatches 限制单轮领取批数：积压特别多时留一部分给下一轮，
// 避免一轮扫描长时间占住循环、把心跳和其他子任务饿死。
const maxJobBatches = 10

// runJobs 是任务队列模式（M05）的一轮：入队 → 领取 → 处理 → 完成/失败。
//
// 与水位路径的关键差异：
//   - 入队只看「入向且正文非空」，不看 status，未回复的弹幕也能被提炼；
//   - 没有事件时间游标，晚完成事件不会因为时间早于水位而被跳过；
//   - 单条失败只记账并回队，本轮立即结束，下一轮（5 分钟后）再试。
func (w *Worker) runJobs(ctx context.Context, stats *Stats) (Stats, error) {
	// 入队批次比领取批次大：首次上线时历史事件可能上万条，
	// 入队太慢会让队列长期喂不饱（每轮只入队 200、却能处理 2000）。
	enqueueLimit := w.opts.BatchSize * 10
	if enqueueLimit > 500 {
		enqueueLimit = 500
	}
	// 领取批次按实际容量收窄：串行处理 + 执行中续租，一批只拿
	// 在租约内做得到的量，队尾才不会在排队时过期。
	claimLimit := w.opts.BatchSize
	if claimLimit > maxJobClaimSize {
		claimLimit = maxJobClaimSize
	}

	for batch := 0; batch < maxJobBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return *stats, err
		}
		// 每批前补一次入队：新事件能在本轮就被处理，不必等下一轮。
		enqueued, err := w.jobs.Enqueue(ctx, enqueueLimit)
		if err != nil {
			return *stats, fmt.Errorf("入队提炼任务：%w", err)
		}
		stats.Enqueued += enqueued

		jobs, err := w.jobs.Claim(ctx, w.opts.JobOwner, w.opts.JobTTL, claimLimit)
		if err != nil {
			return *stats, fmt.Errorf("领取提炼任务：%w", err)
		}
		if len(jobs) == 0 {
			break
		}
		for _, job := range jobs {
			if err := w.handleJob(ctx, job, stats); err != nil {
				// 单条失败已记账并回队，本轮到此为止（不做紧密重试）。
				w.rebuildProfiles(ctx, stats)
				w.rebuildSoul(ctx, stats)
				return *stats, nil
			}
		}
	}
	w.rebuildProfiles(ctx, stats)
	w.rebuildSoul(ctx, stats)
	return *stats, nil
}

// handleJob 处理一条任务：提炼 → 租约围栏内提交 → 标记完成；失败则记录原因并回队。
//
// 执行期间启动续租心跳；续租发现失租时取消提炼。真正的写保护仍在
// CompleteWithRecords 的事务里：即使取消晚了一步，栅栏也会让结果整体回滚。
func (w *Worker) handleJob(ctx context.Context, job Job, stats *Stats) error {
	stats.Scanned++
	// 事件已被删除（隐私撤回 / 清理）：任务直接完成，不无限重试。
	if strings.TrimSpace(job.Event.ID) == "" {
		stats.Skipped++
		if err := w.jobs.Complete(ctx, job); err != nil {
			if errors.Is(err, store.ErrConflict) {
				stats.LeaseLost++
				return nil
			}
			return fmt.Errorf("完成空任务：%w", err)
		}
		return nil
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	lost, stopHeartbeat := w.startLeaseHeartbeat(ctx, job, cancelWork)
	records, skipped, err := w.extractEvent(workCtx, job.Event)
	stopHeartbeat()
	if leaseLost(lost) {
		// 续租已确认失租：提炼结果作废，新持有者会重做。
		stats.LeaseLost++
		return nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lostLease, failErr := w.failJob(ctx, job, err.Error())
		if lostLease {
			stats.LeaseLost++
			return nil
		}
		if failErr != nil {
			return errors.Join(err, failErr)
		}
		stats.Failed++
		return err
	}
	if skipped {
		stats.Skipped++
		if err := w.jobs.Complete(ctx, job); err != nil {
			if errors.Is(err, store.ErrConflict) {
				stats.LeaseLost++
				return nil
			}
			return fmt.Errorf("完成提炼任务：%w", err)
		}
		return nil
	}

	stats.Extracted += len(records)
	outcomes, err := w.jobs.CompleteWithRecords(ctx, job, records)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			stats.LeaseLost++
			return nil
		}
		lostLease, failErr := w.failJob(ctx, job, err.Error())
		if lostLease {
			stats.LeaseLost++
			return nil
		}
		if failErr != nil {
			return errors.Join(fmt.Errorf("提交提炼结果：%w", err), failErr)
		}
		stats.Failed++
		return fmt.Errorf("提交提炼结果：%w", err)
	}
	for _, outcome := range outcomes {
		if outcome.Inserted {
			stats.Inserted++
		} else {
			stats.Duplicated++
		}
	}
	return nil
}

// failJob 把失败写回任务表；返回 true 表示租约已被抢走（无需再记失败）。
func (w *Worker) failJob(ctx context.Context, job Job, reason string) (bool, error) {
	if err := w.jobs.Fail(ctx, job, reason, true, w.opts.MaxAttempts); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return true, nil
		}
		return false, fmt.Errorf("记录提炼任务失败：%w", err)
	}
	return false, nil
}

// startLeaseHeartbeat 在提炼执行期间定期续租，返回「失租」信号与停止函数。
//
// 启动时先立刻续一次：任务在队列里等待过，租约可能已经接近到期。
// 只有 Renew 明确返回 ErrConflict 才算失租；网络/DB 抖动留到下一拍重试，
// 租约是否仍然有效最终由提交事务的条件更新裁决。
func (w *Worker) startLeaseHeartbeat(ctx context.Context, job Job, cancelWork context.CancelFunc) (<-chan struct{}, func()) {
	lost := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	interval := w.opts.JobTTL / jobRenewDivisor
	if interval <= 0 {
		interval = w.opts.JobTTL
	}

	go func() {
		defer close(done)
		renew := func() bool {
			if _, err := w.jobs.Renew(ctx, job, w.opts.JobTTL); err != nil {
				if errors.Is(err, store.ErrConflict) {
					close(lost)
					cancelWork()
					return false
				}
				// 非冲突错误：下一拍再试；父 ctx 已停则直接退出。
				return ctx.Err() == nil
			}
			return true
		}
		if !renew() {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if !renew() {
					return
				}
			}
		}
	}()

	var once sync.Once
	return lost, func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

// leaseLost 判断续租是否已经明确失租。
func leaseLost(lost <-chan struct{}) bool {
	select {
	case <-lost:
		return true
	default:
		return false
	}
}

// 冻结来源，用于日志区分「配置写死」与「运行时开关」。
const (
	freezeReasonConfig    = "config"
	freezeReasonStateFile = "state_file"
)

// frozen 判断本轮是否应跳过；配置冻结优先，其次看运行时状态文件。
func (w *Worker) frozen() (bool, string, error) {
	if w.opts.Frozen {
		return true, freezeReasonConfig, nil
	}
	if strings.TrimSpace(w.opts.FreezePath) == "" {
		return false, "", nil
	}
	frozen, err := memory.IsFrozen(w.opts.FreezePath)
	if err != nil {
		return false, "", err
	}
	if frozen {
		return true, freezeReasonStateFile, nil
	}
	return false, "", nil
}

// rebuildProfiles 是档案重建子任务：每小时节流 + 只在直播活跃（最近 5 分钟有 in 事件）时跑。
//
// 失败只记在 stats 里，不影响提炼主流程（档案是增强，不是前置）。
func (w *Worker) rebuildProfiles(ctx context.Context, stats *Stats) {
	stats.ProfilesRebuilt = -1
	if w.opts.Profiles == nil {
		stats.ProfileSkipped = "none"
		return
	}
	now := w.now()
	if !w.lastProfile.IsZero() && now.Sub(w.lastProfile) < w.opts.ProfileInterval {
		stats.ProfileSkipped = "throttled"
		return
	}
	active, err := w.opts.Profiles.ActiveSince(ctx, now.Add(-w.opts.ProfileActiveWindow))
	if err != nil || !active {
		stats.ProfileSkipped = "idle"
		return
	}
	res, err := w.opts.Profiles.RebuildProfiles(ctx, store.ViewerRebuildOptions{
		MinHits:       w.opts.ProfileMinHits,
		RecentTexts:   w.opts.ProfileRecentTexts,
		RecentReplies: w.opts.ProfileRecentReplies,
	})
	// 无论成功失败都记节流时间：坏库时不要每 5 分钟重试一次
	w.lastProfile = now
	if err != nil {
		stats.ProfileSkipped = "failed"
		return
	}
	stats.ProfilesRebuilt = res.Profiles
	stats.ProfileSkipped = ""
}

// handleEvent 处理单条事件，返回本条事件对应的新水位。
func (w *Worker) handleEvent(ctx context.Context, ev event.Event, stats *Stats) (Cursor, error) {
	next := Cursor{At: ev.At, ID: ev.ID}
	if err := w.processEvent(ctx, ev, stats); err != nil {
		return next, err
	}
	return next, nil
}

// processEvent 提炼并逐条落库一条事件（水位路径）。
//
// 「有意跳过」（时间缺失 / 私密未授权 / 没有候选）返回 nil：
// 这些是正常结论，不该让任务反复重试。
func (w *Worker) processEvent(ctx context.Context, ev event.Event, stats *Stats) error {
	records, skipped, err := w.extractEvent(ctx, ev)
	if err != nil {
		return err
	}
	if skipped {
		stats.Skipped++
		return nil
	}
	for _, record := range records {
		stats.Extracted++
		inserted, err := w.repo.InsertRecord(ctx, record)
		if err != nil {
			return fmt.Errorf("写入记忆记录：%w", err)
		}
		if inserted {
			stats.Inserted++
		} else {
			stats.Duplicated++
		}
	}
	return nil
}

// extractEvent 只提炼、不落库，供水位路径与任务路径共用。
//
// 任务路径不能在这里写库：必须先过租约栅栏，再在同一个事务里
// 写入记录并完成任务（C1/N06），否则旧 worker 失租返回会重复写。
//
// 「有意跳过」（时间缺失 / 私密未授权 / 没有候选）返回 skipped=true 且 err=nil。
func (w *Worker) extractEvent(ctx context.Context, ev event.Event) ([]store.MemoryRecord, bool, error) {
	// 事件时间缺失：算不出有效期，也过不了持久化校验（valid_from 不能为零）
	if ev.At.IsZero() {
		return nil, true, nil
	}

	// 私密边界在任何上下文拼装、提炼器调用和模型调用之前：未授权直接跳过。
	// 跳过也算处理完毕，否则每轮都会重复扫描同一批私信。
	if ev.Private && !w.opts.AllowPrivate {
		return nil, true, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	extractOpts := memory.ExtractOptions{
		OwnerUID:  w.opts.OwnerUID,
		OwnerName: w.opts.OwnerName,
		// 首次出现需要「该 session 已见昵称集合」，属持久化能力，
		// 既有 repo 没有对应接口，本批不臆造存储，固定传 false。
		IsFirstNick:  false,
		AllowPrivate: w.opts.AllowPrivate,
	}
	// 上下文：取同 session、且严格早于当前事件的历史（M07），失败降级为无上下文
	if w.opts.Recent != nil {
		scope := memory.Scope{Channel: ev.Channel, Session: ev.Session, Private: ev.Private}
		if recent, err := w.recentBefore(ctx, scope, ev, w.opts.ContextLimit+1); err == nil {
			extractOpts.Context = withoutCurrent(recent, ev.ID)
		}
	}
	extractor := w.opts.Extractor
	if extractor == nil {
		extractor = memory.NewRuleExtractor()
	}
	candidates, err := extractor.Extract(ctx, ev, extractOpts)
	if err != nil {
		// 失租/停机取消不要回落规则版：结果已经作废，继续算只是浪费。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		// 提炼器异常兜底规则版；LLMExtractor 内部通常已降级，这里防未来实现。
		candidates = memory.Extract(ev, extractOpts)
	}
	if len(candidates) == 0 {
		return nil, true, nil
	}

	records := make([]store.MemoryRecord, 0, len(candidates))
	for _, candidate := range candidates {
		records = append(records, w.buildRecord(ev, candidate))
	}
	return records, false, nil
}

// recentBefore 取「严格早于当前事件」的上下文（M07）。
//
// 优先用带 SQL 上界的来源；旧来源退回「最近 N 条 + 客户端过滤」，
// 语义仍正确，只是可能因配额被未来事件占用而变浅。
func (w *Worker) recentBefore(ctx context.Context, scope memory.Scope, ev event.Event, limit int) ([]event.Event, error) {
	if src, ok := w.opts.Recent.(RecentBeforeSource); ok {
		return src.RecentEventsBefore(ctx, scope, ev.At, ev.ID, limit)
	}
	rows, err := w.opts.Recent.RecentEvents(ctx, scope, limit)
	if err != nil {
		return nil, err
	}
	return keepBefore(rows, ev.At, ev.ID), nil
}

// keepBefore 只保留严格早于 (at, id) 的行，与 SQL 的 `(at,id) < (?,?)` 语义一致。
func keepBefore(rows []event.Event, at time.Time, id string) []event.Event {
	out := make([]event.Event, 0, len(rows))
	for _, row := range rows {
		if row.At.After(at) {
			continue
		}
		// 同一时刻：按 id 排序，等于或更大的都算「未来/当前」。
		if row.At.Equal(at) && row.ID >= id {
			continue
		}
		out = append(out, row)
	}
	return out
}

// withoutCurrent 去掉当前这条事件，保留旧→新顺序。
func withoutCurrent(rows []event.Event, currentID string) []event.Event {
	out := make([]event.Event, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- { // 查询是倒序，翻转成旧→新
		if rows[i].ID == currentID {
			continue
		}
		out = append(out, rows[i])
	}
	return out
}

// buildRecord 把候选装配成持久化记录。
func (w *Worker) buildRecord(ev event.Event, c memory.Candidate) store.MemoryRecord {
	source := w.opts.Source
	if source == "" {
		source = defaultSource(c.Kind)
	}
	return store.MemoryRecord{
		Record: memory.Record{
			ID:         recordIDFor(ev, c),
			Channel:    ev.Channel,
			Session:    ev.Session,
			Summary:    c.Summary,
			Importance: c.Importance,
			Confidence: c.Confidence,
			Private:    ev.Private,
			EventIDs:   []string{ev.ID},
			ValidFrom:  w.validFrom(ev.At),
		},
		Source:   source,
		Kind:     storeKind(c.Kind),
		Title:    c.Title,
		FromID:   strings.TrimSpace(c.FromID),
		FromName: strings.TrimSpace(c.FromName),
		// 谁说的与说的是谁分开落库：Soul 重建只按可信证据决定是否提升。
		AssertedByID:   strings.TrimSpace(c.AssertedByID),
		AssertedByName: strings.TrimSpace(c.AssertedByName),
		Subject:        strings.TrimSpace(c.Subject),
		// Body 留空：摘要已经够召回用，不把完整正文写进记忆库
		Body: "",
	}
}

// validFrom 返回记录的有效期起点：取事件时间，但**绝不晚于当前时间**。
//
// 为什么要兜底：memory_records 的有效性查询是 `valid_from <= now()`，
// 一旦事件时间在未来（时钟漂移、预置数据、跨机同步），写进去的记录会立刻不可见，
// 表现成「提炼明明成功、召回却永远为空」这种很难查的静默故障。
func (w *Worker) validFrom(at time.Time) time.Time {
	now := w.now()
	if at.After(now) {
		return now
	}
	return at
}

// now 返回当前时间；测试可注入 clock 让断言稳定。
func (w *Worker) now() time.Time {
	if w.opts.Now != nil {
		return w.opts.Now()
	}
	return time.Now()
}

// saveIfAdvanced 只在水位真的前进时才写盘，避免空转时反复覆盖文件。
func (w *Worker) saveIfAdvanced(ctx context.Context, from, to Cursor) error {
	if to.equal(from) {
		return nil
	}
	if err := w.wm.Save(ctx, to); err != nil {
		return fmt.Errorf("保存提炼水位：%w", err)
	}
	return nil
}

// recordIDFor 生成记录主键。
//
// 事实类按「同一个人 + 同一句事实 + 同一可见域」去重（而不是按事件）：
// 同一个人在不同时间重复说「我是学生」，同一域内只该留一条。
// 公开域跨房间/跨平台共用主键；私密域必须带 channel+session，
// 否则公共行、DM-A、DM-B 会共享主键，后续摘要可能写进错误范围。
func recordIDFor(ev event.Event, c memory.Candidate) string {
	if c.Kind == memory.KindFact && strings.TrimSpace(ev.FromID) != "" {
		return "fact:" + factDomain(ev) + ":" + strings.TrimSpace(ev.FromID) + ":" + shortHash(c.Title)
	}
	return recordID(ev.ID, c.Kind, c.Title)
}

// factDomain 是事实去重主键里的可见域段。
//
// 公开事实在所有公开通道间共享（同一个人跨平台说同一句事实仍算一条）；
// 私密事实按 channel+session 分域，DM-A 与 DM-B 不能互相覆盖。
func factDomain(ev event.Event) string {
	if ev.Private {
		return "private:" + string(ev.Channel) + ":" + ev.Session
	}
	return "public"
}

// shortHash 取摘要前 12 个十六进制字符，足够在同一人下区分不同事实。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// recordID 由 event_id + kind + title 派生稳定的主键，实现幂等。
//
// 用 sha256 前 12 字节（96 位）足够避免碰撞，且长度可控；
// 分隔符用 \x1f 防止「ab+c」与「a+bc」拼接后撞车。
func recordID(eventID, kind, title string) string {
	sum := sha256.Sum256([]byte(eventID + "\x1f" + kind + "\x1f" + title))
	return "mem_" + hex.EncodeToString(sum[:12])
}

// storeKind 把提炼候选的类型映射到 store 允许的记忆类型。
//
// store.MemoryRecord.Validate 只接受 talk/preference/promise/fact/summary/note，
// 其中 first_seen 没有对应项，落到最贴近的 note（观察记录）。
// 这是映射而不是改契约：要新增 kind 必须先改 store 校验。
func storeKind(kind string) string {
	switch kind {
	case memory.KindPromise:
		return "promise"
	case memory.KindPreference:
		return "preference"
	case memory.KindFact:
		return "fact"
	case memory.KindFirstSeen:
		return "note"
	default:
		return "note"
	}
}

// defaultSource 按候选类型挑 memory_sources 里的来源字典项。
//
// 现有种子只有 owner_direct / owner_interaction / persona / expression / content / reading，
// 首次出现最贴近 owner_interaction（互动信号）。
func defaultSource(kind string) string {
	switch kind {
	case memory.KindPromise, memory.KindPreference:
		return "owner_direct"
	case memory.KindFirstSeen:
		return "owner_interaction"
	default:
		return "content"
	}
}
