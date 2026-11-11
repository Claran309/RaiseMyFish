package reasoning

// 本文件实现 send_danmaku：让 ReAct 在一条事件里直接发弹幕，
// 一轮最多 maxDanmakuPerRun 条，每条 ≤40 rune，去重并按 actor+动作限频。
//
// 边界：
//   - 执行器由装配方注入（房间/账号固定），工具层不碰凭据；
//   - 发送失败如实返回错误、不重试；已成功的条目由 Agent 记进 Result.Danmaku；
//   - 日志只记条数与结果，不记正文。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

// 弹幕工具名与护栏参数。
const (
	ToolSendDanmaku        = "send_danmaku"
	maxDanmakuRunes        = 40
	maxDanmakuPerRun       = 3
	defaultDanmakuInterval = 800 * time.Millisecond
)

// DanmakuSender 是一条弹幕的投递能力（由直播发送器适配）。
type DanmakuSender interface {
	SendDanmaku(ctx context.Context, text string) error
}

// DanmakuDeps 是 send_danmaku 的执行依赖与护栏。
type DanmakuDeps struct {
	Sender DanmakuSender
	// Limiter 是跨事件共享的限频表；nil 时本轮独立（仅测试或无共享装配时）。
	Limiter *SideEffectLimiter
	// Actor 是限频键（通常是本鱼 UID）。
	Actor string
	// Interval 是两次发送的最小间隔；<=0 用默认 800ms。
	Interval time.Duration
	// MaxPerRun 是单轮上限；<=0 用默认 3。
	MaxPerRun int
	Now       func() time.Time
	Log       *zap.Logger
}

// danmakuTool 的计数与去重状态只作用于本轮（工具箱按事件现造）。
type danmakuTool struct {
	deps DanmakuDeps
	mu   sync.Mutex
	sent int
	seen map[string]bool
}

// BuildDanmakuTool 装配 send_danmaku；Sender 为 nil 时报错，避免注册一个永远失败的工具。
func BuildDanmakuTool(d DanmakuDeps) (*Toolbox, error) {
	if d.Sender == nil {
		return nil, errors.New("send_danmaku 缺少发送执行器")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = zap.NewNop()
	}
	if d.Interval <= 0 {
		d.Interval = defaultDanmakuInterval
	}
	if d.MaxPerRun <= 0 {
		d.MaxPerRun = maxDanmakuPerRun
	}
	if d.Limiter == nil {
		d.Limiter = NewSideEffectLimiter()
	}
	t := &danmakuTool{deps: d, seen: make(map[string]bool)}
	return NewToolbox(Tool{
		Name: ToolSendDanmaku,
		Desc: `直接发一条弹幕（≤40字，单轮最多3条）；args 传 {"text":"..."}；回复观众时正文自己以 @昵称 开头；想补一句可再调用一次，不必用 final`,
		Run:  t.run,
	})
}

func (t *danmakuTool) run(ctx context.Context, args string) (string, error) {
	text, err := parseDanmakuText(args)
	if err != nil {
		t.log("rejected", 0)
		return "", err
	}
	t.mu.Lock()
	if t.seen[text] {
		t.mu.Unlock()
		t.log("duplicate", t.sent)
		return "", fmt.Errorf("这条弹幕本轮已经发过了，不要重复")
	}
	if t.sent >= t.deps.MaxPerRun {
		t.mu.Unlock()
		t.log("limit", t.sent)
		return "", fmt.Errorf("本轮弹幕已达上限 %d 条，直接 final 收尾", t.deps.MaxPerRun)
	}
	t.mu.Unlock()
	if !t.deps.Limiter.allow(t.deps.Actor, ToolSendDanmaku, t.deps.Now(), t.deps.Interval) {
		t.log("rate_limited", t.sent)
		return "", fmt.Errorf("发弹幕太频繁，稍后再试或直接 final")
	}
	if err := t.deps.Sender.SendDanmaku(ctx, text); err != nil {
		t.log("failed", t.sent)
		return "", err
	}
	t.mu.Lock()
	t.sent++
	t.seen[text] = true
	sent := t.sent
	t.mu.Unlock()
	t.log("ok", sent)
	return fmt.Sprintf("已发送第 %d/%d 条弹幕", sent, t.deps.MaxPerRun), nil
}

// log 只记结果与条数；正文与参数不进日志。
func (t *danmakuTool) log(result string, sent int) {
	t.deps.Log.Info("弹幕工具调用",
		zap.String("action", "tool_danmaku"),
		zap.String("tool", ToolSendDanmaku),
		zap.String("result", result),
		zap.Int("sent", sent),
	)
}

// parseDanmakuText 解析并校验一条弹幕正文。
func parseDanmakuText(args string) (string, error) {
	text := danmakuText(args)
	if text == "" {
		return "", fmt.Errorf(`缺少 text（args 传 {"text":"..."}）`)
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("正文不是合法 UTF-8")
	}
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("弹幕必须是单行文本，不能含换行或控制字符")
		}
	}
	if n := utf8.RuneCountInString(text); n > maxDanmakuRunes {
		return "", fmt.Errorf("弹幕最多 %d 字（当前 %d）", maxDanmakuRunes, n)
	}
	return text, nil
}

// danmakuText 从参数里取正文：优先 JSON 的 text 字段，其次把参数本身当正文。
func danmakuText(args string) string {
	fields := splitArgs(args)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "text" && i+1 < len(fields) {
			return strings.TrimSpace(fields[i+1])
		}
	}
	raw := strings.TrimSpace(args)
	if raw == "" || strings.HasPrefix(raw, "{") {
		return ""
	}
	return raw
}
