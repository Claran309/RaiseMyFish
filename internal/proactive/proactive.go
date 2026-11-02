// Package proactive 实现「观众视角主动发弹幕」：截一帧、看变化、生成一句、去重、限流、交给 sender。
//
// 它是旁路能力：任何一步失败都只让本轮跳过，不影响直播采集、调度与回复主链路。
package proactive

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"time"

	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

const (
	// DefaultPrompt 让视觉模型只产出一句观众弹幕，避免解释、@ 或回复某人。
	DefaultPrompt = "你在看直播。看图，用观众口吻写一句中文弹幕，只输出这一句：不超过15个字，不要@任何人，不要引号、序号或解释。"
	// MaxDanmakuRunes 与派发单一致：主动弹幕最多 15 个字。
	MaxDanmakuRunes         = 15
	DefaultMaxPerHour       = 6
	DefaultMinChangePercent = 5
	DefaultSendGap          = 30 * time.Second
	recentLimit             = 20
)

// 跳过原因，写入日志字段 reason。
const (
	ReasonNoChange = "no_change"
	ReasonSimilar  = "similar"
	ReasonLimited  = "limited"
	ReasonGap      = "gap"
)

// Snapshotter 截取当前直播画面，返回本次临时文件路径；调用方负责删除。
type Snapshotter interface {
	Snapshot(ctx context.Context) (string, error)
}

// Describer 与原看直播流程共用 OpenAI 兼容视觉接口。
type Describer interface {
	Describe(ctx context.Context, frames []string, prompt string) (string, error)
}

// Sender 发送主动弹幕；实现方负责 out 事件记账。
type Sender interface {
	Send(ctx context.Context, text string) (sender.Result, error)
}

// Options 是运行期参数；Interval 由 cmd 的定时循环控制，不在这里睡眠。
type Options struct {
	MaxPerHour       int
	MinChangePercent float64
	SendGap          time.Duration
	Prompt           string
	Logger           *zap.Logger
	Now              func() time.Time
}

// Result 描述本轮结果，便于日志与测试断言。
type Result struct {
	Sent          bool
	Reason        string
	ChangePercent float64
}

// Runner 一次只跑一轮；上一次画面只保存在内存，不落盘。
type Runner struct {
	snapshot  Snapshotter
	describer Describer
	sender    Sender
	log       *zap.Logger
	now       func() time.Time
	minChange float64
	prompt    string
	limiter   *limiter
	recent    *recent
	previous  image.Image
}

// New 校验依赖与参数；构造期不截帧、不调用模型、不发送。
func New(snapshot Snapshotter, describer Describer, snd Sender, opts Options) (*Runner, error) {
	if snapshot == nil || describer == nil || snd == nil {
		return nil, errors.New("主动弹幕缺少截帧/读帧/发送环节")
	}
	if opts.MaxPerHour < 1 || opts.MaxPerHour > 60 {
		return nil, errors.New("主动弹幕 max_per_hour 必须在1至60之间")
	}
	if math.IsNaN(opts.MinChangePercent) || math.IsInf(opts.MinChangePercent, 0) || opts.MinChangePercent < 0 || opts.MinChangePercent > 100 {
		return nil, errors.New("主动弹幕 min_change_percent 必须在0至100之间")
	}
	gap := opts.SendGap
	if gap == 0 {
		gap = DefaultSendGap
	}
	if gap < 0 || gap > time.Hour {
		return nil, errors.New("主动弹幕 send_gap 必须在0至3600秒之间")
	}
	prompt := opts.Prompt
	if prompt == "" {
		prompt = DefaultPrompt
	}
	log := opts.Logger
	if log == nil {
		log = zap.NewNop()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Runner{
		snapshot:  snapshot,
		describer: describer,
		sender:    snd,
		log:       log,
		now:       now,
		minChange: opts.MinChangePercent,
		prompt:    prompt,
		limiter:   newLimiter(opts.MaxPerHour, time.Hour, gap),
		recent:    newRecent(recentLimit),
	}, nil
}

// RunOnce 执行一轮：限流预检 → 截帧 → 变化检测 → 读帧生成 → 去重 → 限流占位 → 发送。
func (r *Runner) RunOnce(ctx context.Context) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("主动弹幕缺少上下文")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if reason := r.limiter.Check(r.now()); reason != "" {
		return Result{Reason: reason}, r.skip(reason, 0)
	}

	path, err := r.snapshot.Snapshot(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("截取直播画面失败：%w", err)
	}
	defer func() { _ = os.Remove(path) }()

	current, err := loadImage(path)
	if err != nil {
		return Result{}, fmt.Errorf("读取直播画面失败：%w", err)
	}
	change := 100.0
	if r.previous != nil {
		change = ChangePercent(r.previous, current)
	}
	r.previous = current
	if change < r.minChange {
		return Result{Reason: ReasonNoChange, ChangePercent: change}, r.skip(ReasonNoChange, change)
	}
	if err := ctx.Err(); err != nil {
		return Result{ChangePercent: change}, err
	}

	raw, err := r.describer.Describe(ctx, []string{path}, r.prompt)
	if err != nil {
		return Result{ChangePercent: change}, fmt.Errorf("生成主动弹幕失败：%w", err)
	}
	text := sanitizeDanmaku(raw)
	if text == "" {
		return Result{ChangePercent: change}, errors.New("生成主动弹幕为空")
	}
	if r.recent.similar(text) {
		return Result{Reason: ReasonSimilar, ChangePercent: change}, r.skip(ReasonSimilar, change)
	}
	// 限流按「发送尝试」计数：失败/不确定也占额度，宁可不发也不刷屏。
	if !r.limiter.Reserve(r.now()) {
		return Result{Reason: ReasonLimited, ChangePercent: change}, r.skip(ReasonLimited, change)
	}
	r.recent.add(text)

	res, err := r.sender.Send(ctx, text)
	if err != nil {
		// 发送成功但审计落库失败时 res.OK 仍为 true，保留真实结果供上层区分。
		return Result{Sent: res.OK, ChangePercent: change}, fmt.Errorf("发送主动弹幕失败：%w", err)
	}
	if !res.OK {
		r.log.Warn("主动弹幕未被接受",
			zap.String("action", "proactive"),
			zap.String("error_kind", res.Error),
		)
		return Result{Reason: "send_not_accepted", ChangePercent: change}, nil
	}
	r.log.Info("主动弹幕已发送",
		zap.String("action", "proactive"),
		zap.Bool("verified", res.Verified),
		zap.Int("runes", len([]rune(text))),
		zap.Float64("change_percent", change),
	)
	return Result{Sent: true, ChangePercent: change}, nil
}

func (r *Runner) skip(reason string, change float64) error {
	r.log.Info("主动弹幕跳过",
		zap.String("action", "proactive"),
		zap.String("reason", reason),
		zap.Float64("change_percent", change),
	)
	return nil
}
