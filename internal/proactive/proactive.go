// Package proactive 实现「观众视角主动发弹幕」。
//
// 0.19 重构后的链路（复用直播回复管线，只多注入一段画面描述）：
//
//	gap 检查 → 截帧 → 详细识图 → 构造 ContextInput（Visual + 合成事件）
//	→ Brain.Reply（人设/记忆/知识/预算全部复用）→ 清洗/15字/去重 → 发送
//
// 它是旁路能力：任何一步失败都只让本轮跳过，不影响直播采集、调度与回复主链路。
package proactive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/sender"
	"go.uber.org/zap"
)

const (
	// MaxDanmakuRunes 与派发单一致：主动弹幕最多 15 个字。
	MaxDanmakuRunes = 15
	// DefaultSendGap 是两次主动发送之间的默认最小间隔。
	DefaultSendGap = 30 * time.Second
	// SyntheticEventText 是合成事件的占位正文：Brain 校验要求事件文本非空。
	SyntheticEventText = "[画面] 正在看直播"
	recentLimit        = 20
)

// 跳过原因，写入日志字段 reason。
const (
	ReasonSimilar = "similar"
	ReasonGap     = "gap"
)

// Snapshotter 截取当前直播画面，返回本次临时文件路径；调用方负责删除。
type Snapshotter interface {
	Snapshot(ctx context.Context) (string, error)
}

// Sender 发送主动弹幕；实现方负责 out 事件记账。
type Sender interface {
	Send(ctx context.Context, text string) (sender.Result, error)
}

// Options 是运行期参数；Interval 由 cmd 的定时循环控制，不在这里睡眠。
type Options struct {
	// SendGap 是两次发送的最小间隔；零值取 DefaultSendGap。
	SendGap time.Duration
	// RoomID 是合成事件与记忆范围的会话标识（直播间号）。
	RoomID string
	Logger *zap.Logger
	Now    func() time.Time
}

// Result 描述本轮结果，便于日志与测试断言。
type Result struct {
	Sent   bool
	Reason string
	// Text 是真正发出去的弹幕（未发送时为空）。
	Text string
	// Visual 是本轮注入给 Brain 的画面描述（便于排查「模型凭什么这么说」）。
	Visual string
}

// Runner 一次只跑一轮；不保存上一帧（像素变化检测已删除）。
type Runner struct {
	snapshot Snapshotter
	vision   VisionDescriber
	brain    brain.Brain
	pack     persona.Pack
	mem      memory.Reader
	sender   Sender
	room     string
	limiter  *gapLimiter
	recent   *recent
	log      *zap.Logger
	now      func() time.Time
}

// New 校验依赖与参数；构造期不截帧、不调用模型、不发送。
func New(snapshot Snapshotter, vision VisionDescriber, engine brain.Brain, pack persona.Pack, mem memory.Reader, snd Sender, opts Options) (*Runner, error) {
	if snapshot == nil || vision == nil || snd == nil {
		return nil, errors.New("主动弹幕缺少截帧/识图/发送环节")
	}
	if engine == nil {
		return nil, errors.New("主动弹幕缺少 Brain（需要复用回复管线）")
	}
	if strings.TrimSpace(pack.Meta.Name) == "" || strings.TrimSpace(pack.Soul) == "" {
		return nil, errors.New("主动弹幕缺少可用人格包")
	}
	if strings.TrimSpace(opts.RoomID) == "" {
		return nil, errors.New("主动弹幕缺少房间号")
	}
	gap := opts.SendGap
	if gap == 0 {
		gap = DefaultSendGap
	}
	if gap < 0 || gap > time.Hour {
		return nil, errors.New("主动弹幕 send_gap 必须在0至3600秒之间")
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
		snapshot: snapshot,
		vision:   vision,
		brain:    engine,
		pack:     pack,
		mem:      mem,
		sender:   snd,
		room:     opts.RoomID,
		limiter:  newGapLimiter(gap),
		recent:   newRecent(recentLimit),
		log:      log,
		now:      now,
	}, nil
}

// RunOnce 执行一轮：
//
//	gap 检查 → 截帧 → 详细识图 → 构造 ContextInput → Brain.Reply
//	→ 清洗/15字 → 去重 → 发送
func (r *Runner) RunOnce(ctx context.Context) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("主动弹幕缺少上下文")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// 1. gap：间隔内直接跳过，不截帧不调模型（省钱也省 CPU）
	if reason := r.limiter.Check(r.now()); reason != "" {
		r.skip(reason)
		return Result{Reason: reason}, nil
	}

	// 2. 截帧（临时文件用完即删）
	path, err := r.snapshot.Snapshot(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("截取直播画面失败：%w", err)
	}
	defer func() { _ = os.Remove(path) }()

	// 3. 详细识图：产出的是「上下文」，不是弹幕，所以不限 15 字
	visual, err := r.vision.Describe(ctx, path)
	if err != nil {
		return Result{}, fmt.Errorf("识别直播画面失败：%w", err)
	}
	visual = strings.TrimSpace(visual)
	if visual == "" {
		return Result{}, errors.New("画面描述为空")
	}
	if err := ctx.Err(); err != nil {
		return Result{Visual: visual}, err
	}

	// 4. 构造 ContextInput：合成事件 + 画面描述，其余（人设/记忆/知识/预算）全走 Brain 复用管线
	ev := r.syntheticEvent()
	scope := memory.Scope{Channel: ev.Channel, Session: ev.Session}
	memCtx, shared := r.recall(ctx, scope)
	input := brain.ContextInput{
		Event:   ev,
		Persona: r.pack,
		Memory:  memCtx,
		Shared:  shared,
		Visual:  visual,
	}
	task := brain.Task{ID: ev.ID, EventID: ev.ID, Channel: ev.Channel, Session: ev.Session}

	// 5. 复用 Brain：人设、记忆、知识、预算截断与弹幕回复完全相同
	reply, err := r.brain.Reply(ctx, task, input)
	if err != nil {
		return Result{Visual: visual}, fmt.Errorf("生成主动弹幕失败：%w", err)
	}

	// 6. 清洗 + 15 字截断
	text := sanitizeDanmaku(reply.Text)
	if text == "" {
		return Result{Visual: visual}, errors.New("生成主动弹幕为空")
	}
	// 7. 去重：最近 20 条里相似度 ≥0.75 视为重复
	if r.recent.similar(text) {
		r.skip(ReasonSimilar)
		return Result{Reason: ReasonSimilar, Visual: visual}, nil
	}
	if !r.limiter.Reserve(r.now()) {
		r.skip(ReasonGap)
		return Result{Reason: ReasonGap, Visual: visual}, nil
	}
	r.recent.add(text)

	// 8. 发送
	res, err := r.sender.Send(ctx, text)
	if err != nil {
		// 发送成功但审计落库失败时 res.OK 仍为 true，保留真实结果供上层区分。
		return Result{Sent: res.OK, Text: text, Visual: visual}, fmt.Errorf("发送主动弹幕失败：%w", err)
	}
	if !res.OK {
		r.log.Warn("主动弹幕未被接受",
			zap.String("action", "proactive"),
			zap.String("error_kind", res.Error),
		)
		return Result{Reason: "send_not_accepted", Text: text, Visual: visual}, nil
	}
	r.log.Info("主动弹幕已发送",
		zap.String("action", "proactive"),
		zap.Bool("verified", res.Verified),
		zap.Int("runes", len([]rune(text))),
		zap.Int("visual_runes", len([]rune(visual))),
	)
	return Result{Sent: true, Text: text, Visual: visual}, nil
}

// syntheticEvent 造一条「没有弹幕的弹幕」。
//
// Brain 要求事件 ID 稳定非空、Text 非空、Dir=in；这里用固定占位正文，
// 真正的画面信息走 ContextInput.Visual。
func (r *Runner) syntheticEvent() event.Event {
	now := r.now()
	return event.Event{
		ID:      "proactive:" + r.room + ":" + strconv.FormatInt(now.UnixNano(), 10),
		Channel: event.ChLive,
		Dir:     event.DirIn,
		Session: r.room,
		Text:    SyntheticEventText,
		At:      now,
		Status:  event.StatusQueued,
	}
}

// recall 尽力召回记忆；失败只降级为空快照，不影响本轮（记忆是增强）。
func (r *Runner) recall(ctx context.Context, scope memory.Scope) (memory.Context, memory.SharedState) {
	empty := memory.Context{Scope: scope}
	emptyShared := memory.SharedState{Scope: scope}
	if r.mem == nil {
		return empty, emptyShared
	}
	recalled, err := r.mem.Recall(ctx, scope)
	if err != nil {
		r.log.Warn("主动弹幕记忆召回失败，降级为空快照",
			zap.String("action", "proactive"), zap.Error(err))
		return empty, emptyShared
	}
	shared, err := r.mem.Shared(ctx, scope)
	if err != nil {
		return recalled, emptyShared
	}
	return recalled, shared
}

func (r *Runner) skip(reason string) {
	r.log.Info("主动弹幕跳过",
		zap.String("action", "proactive"),
		zap.String("reason", reason),
	)
}
