package main

// Generate 阶段：召回 → Brain/ReAct → Sender → 结清。
//
// 结清纪律：handler 必须把事件落到 done/skipped/dead/retry 再返回；
// 真实 sender 已在 Send 事务里结清时，不能再拿旧 claim 写 Bus。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/presence"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/scheduler"
	"RaiseMyFish/internal/sender"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// maxLiveReplyRunes 是 B站弹幕的兜底上限；配置为 0 时使用。
const maxLiveReplyRunes = 40

// formatReplyText 给 live 回复加 @ 前缀并按 rune 截断；其他通道原样返回。
//
// 触发 @ 前缀的条件由 scheduler.MentionBackTarget 决定（live + 开关 + 结构化 @ 命中）。
// 前缀 "@昵称 " 也计入 max_reply_runes；昵称本身过长导致没有正文字数时跳过前缀，
// 避免发送一条只有 @ 的空弹幕。
func formatReplyText(ev event.Event, text string, policy scheduler.ChannelPolicy) string {
	if ev.Channel != event.ChLive {
		return text
	}
	maxRunes := policy.MaxReplyRunes
	if maxRunes <= 0 {
		maxRunes = maxLiveReplyRunes
	}
	if target, ok := scheduler.MentionBackTarget(ev, policy); ok && !hasMentionPrefix(text) {
		prefix := "@" + target + " "
		if budget := maxRunes - utf8.RuneCountInString(prefix); budget > 0 {
			return prefix + truncateRunes(text, budget)
		}
	}
	return truncateRunes(text, maxRunes)
}

// hasMentionPrefix 判断正文是否已以 @/＠ 开头，避免重复加前缀。
func hasMentionPrefix(text string) bool {
	for _, r := range strings.TrimLeftFunc(text, unicode.IsSpace) {
		return r == '@' || r == '＠'
	}
	return false
}

// truncateRunes 按 rune 截断，保证不会切坏 UTF-8。
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// formatViewerProfile 把档案压成 300-400 字的注入片段。
//
// 只带「说过多少次/回过多少次/最近几句」，不做价值判断，也不带 uid。
func formatViewerProfile(p store.ViewerProfile) string {
	texts := p.RecentTexts
	if len(texts) > 8 {
		texts = texts[:8]
	}
	replies := p.RecentReplies
	if len(replies) > 4 {
		replies = replies[:4]
	}
	var sb strings.Builder
	sb.WriteString("【对方档案 · 老熟人】\n")
	fmt.Fprintf(&sb, "- 说过 %d 次 / 鱼回过 %d 次 / 最后出现 %s\n", p.Hits, p.Replies, p.LastSeen.Format("01-02"))
	if len(texts) > 0 {
		sb.WriteString("## 他说过的话（最近几条）\n")
		for _, t := range texts {
			sb.WriteString("- " + truncateRunes(t, 40) + "\n")
		}
	}
	if len(replies) > 0 {
		sb.WriteString("## 本鱼回过的（最近几条）\n")
		for _, t := range replies {
			sb.WriteString("- " + truncateRunes(t, 30) + "\n")
		}
	}
	return truncateRunes(sb.String(), 400)
}

// generateDeps 是 Generate handler 的只读依赖投影。
type generateDeps struct {
	live       scheduler.ChannelPolicy
	pack       persona.Pack
	brains     map[event.Channel]brain.Brain
	sender     sender.Sender
	bus        event.Bus
	retryDelay time.Duration
	memory     memory.Reader
	knowledge  knowledge.Reader
	viewers    store.ViewerRepo
	soul       *soulCache
	channels   *channelSet
	presence   *presence.Table
	state      func(time.Time) string
	react      *reactRuntime
	log        *zap.Logger
}

// generateHandler 返回 Generate 阶段的调度 handler。
func generateHandler(deps generateDeps) scheduler.Handler {
	return func(ctx context.Context, w scheduler.Work) (scheduler.Result, error) {
		// 0.4 契约：schedule 只做首次过滤与准入，Task 是否存在都不能进入生成；
		// 交回调度器重新准入，未结清事件会让 complete() 终止消费循环。
		if w.Claim.Stage == event.StageSchedule || w.Task == nil {
			if err := deps.bus.Defer(ctx, w.Claim, time.Now().Add(deps.retryDelay)); err != nil {
				return scheduler.Result{}, fmt.Errorf("交回调度重试失败：%w", err)
			}
			return scheduler.Result{}, nil
		}
		ev := w.Claim.Event
		engine, ok := deps.brains[ev.Channel]
		if !ok {
			return scheduler.Result{}, fmt.Errorf("通道 %s 未配置 Brain 路由", ev.Channel)
		}

		// 1. 先进 replying：契约只有 queued→replying 这条边，不能一步跳到 sent
		if err := deps.bus.Mark(ctx, w.Claim, []event.Status{event.StatusQueued}, event.StatusReplying); err != nil {
			return scheduler.Result{}, fmt.Errorf("进入执行中失败：%w", err)
		}
		// presence 是旁路观测：空表跳过，失败也不影响主链路
		if deps.presence != nil {
			deps.presence.Set(presence.State{Kind: presence.KindReplying, Channel: string(ev.Channel)})
		}
		defer func() {
			if deps.presence != nil {
				deps.presence.Clear(presence.KindReplying, string(ev.Channel))
			}
		}()

		scope := memory.Scope{Channel: ev.Channel, Session: ev.Session, Private: ev.Private}
		// 召回判断仍保留：短句单步与 ReAct 失败回落时，Brain 需要 memory/knowledge 注入。
		decision := reasoning.Decide(ev.Text)
		deps.log.Info("召回判断完成",
			zap.String("action", "reasoning"),
			zap.Bool("need_memory", decision.NeedMemory),
			zap.Bool("need_knowledge", decision.NeedKnowledge),
			zap.String("reason", decision.Reason),
		)

		// 不需要召回时仍传带 Scope 的空快照；召回失败按现有降级，不阻塞回复。
		memCtx := memory.Context{Scope: scope}
		shared := memory.SharedState{Scope: scope}
		if decision.NeedMemory {
			memCtx, shared = recallMemory(ctx, deps.memory, scope, deps.log)
		}
		var knowCtx *knowledge.Context
		if decision.NeedKnowledge {
			knowCtx = recallKnowledge(ctx, deps.knowledge, ev, deps.log)
		}

		stateText := ""
		if deps.state != nil {
			stateText = deps.state(time.Now())
		}
		// 认人：按 from_id 查档案；陌生人（无档案）不注入
		viewerText := ""
		if deps.viewers != nil && strings.TrimSpace(ev.FromID) != "" {
			if profile, err := deps.viewers.GetProfile(ctx, ev.FromID); err == nil {
				viewerText = formatViewerProfile(profile)
			}
		}
		// 长期设定：带 mtime 缓存，文件没变不重复解析
		soulLines := deps.soul.load(ctx)
		input := brain.ContextInput{
			Event:     ev,
			Persona:   deps.pack,
			Memory:    memCtx,
			Shared:    shared,
			Knowledge: knowCtx,
			State:     stateText,
			Viewer:    viewerText,
			Soul:      soulLines,
		}
		task := brain.Task{ID: w.Task.ID, EventID: w.Task.EventID, Channel: w.Task.Channel, Session: w.Task.Session}

		// ReAct：默认除短句外全部走多步（default_react=true），短句保持单步以维持 1 秒级延迟。
		// 工具要按**当前事件**的 Scope 查（并发 worker 共享一个可变 Scope 会串会话），
		// 所以 Agent 由工厂按事件现造；相同问题 5 分钟内直接复用结果。
		var reply brain.Reply
		var err error
		reactText := ""
		if deps.react != nil && deps.react.factory != nil && shouldUseReAct(ev.Text, deps.react.defaultReact) {
			reactText, err = runReact(ctx, deps.react, scope, deps.pack, ev, deps.log)
			if err != nil {
				// ReAct 是增强：失败就回落单步，不能把弹幕卡死
				deps.log.Warn("ReAct 执行失败，回落单步生成",
					zap.String("action", "react_fallback"),
					zap.String("event_id", ev.ID),
					zap.Error(err),
				)
				reactText = ""
			}
		}
		if strings.TrimSpace(reactText) != "" {
			reply = brain.Reply{Text: reactText}
		} else {
			reply, err = engine.Reply(ctx, task, input)
			if budgetFailure(err) && input.Knowledge != nil {
				// 预算错误常常是知识段撑爆提示：去掉知识再试一次，仍失败才进 dead。
				deps.log.Warn("知识召回超出提示预算，去掉知识重试",
					zap.String("action", "knowledge_budget_fallback"),
					zap.String("event_id", ev.ID),
					zap.String("ch", string(ev.Channel)),
					zap.String("session", ev.Session),
				)
				input.Knowledge = nil
				reply, err = engine.Reply(ctx, task, input)
			}
		}
		if err != nil {
			// 生成失败也要结清，否则事件卡在 replying
			if markErr := deps.bus.Mark(ctx, w.Claim, []event.Status{event.StatusReplying}, event.StatusDead); markErr != nil {
				return scheduler.Result{}, errors.Join(err, markErr)
			}
			// 事件已结清：可预期失败只留日志，返回 nil 让消费循环继续。
			logWorkFailure(deps.log, "generate_failed", w, err)
			return scheduler.Result{}, nil
		}

		// Scope 必须显式带上：sender.Reply.Validate 要求与事件通道/会话一致
		// 发送走通道：注册表优先，取不到回落直连 sender（行为与改造前一致）
		disp, ok := deps.channels.resolve(ev.Channel, deps.sender)
		if !ok {
			deps.log.Error("找不到可用的发送通道", zap.String("action", "channel_missing"), zap.String("channel", string(ev.Channel)))
			return scheduler.Result{}, errors.New("找不到可用的发送通道")
		}
		res, err := disp.Send(ctx, w.Claim, sender.Reply{Text: formatReplyText(ev, reply.Text, deps.live), Scope: scope})
		if err != nil {
			// sender 在自身事务里已把输入事件结清并释放租约（Settled=true）；
			// 再拿旧 claim 走 Bus 必然报「事件租约已失效」，这不是需要重试的失败。
			if sender.Handled(err) {
				return scheduler.Result{Replied: res.OK}, nil
			}
			if settleErr := settleFailure(ctx, deps.bus, w.Claim, err, []event.Status{event.StatusReplying}, deps.retryDelay); settleErr != nil {
				return scheduler.Result{}, errors.Join(err, settleErr)
			}
			return scheduler.Result{}, err
		}

		// 投递被接受才算发送成功；OK=false 不记 sending
		if deps.presence != nil && res.OK {
			deps.presence.Set(presence.State{Kind: presence.KindSending, Channel: string(ev.Channel), TTL: time.Minute})
		}

		// 2. 投递完成 → sent（Mark 会把 stage 推到 verify 并保留租约）
		if err := deps.bus.Mark(ctx, w.Claim, []event.Status{event.StatusReplying}, event.StatusSent); err != nil {
			// 真实 sender 在 Send 返回前已原子结清输入事件并释放租约，这次 Mark 只会
			// 拿到 ErrLeaseLost；事件已落到 done/retry，交给 scheduler 按快照核验即可。
			if errors.Is(err, event.ErrLeaseLost) {
				return scheduler.Result{Replied: res.OK}, nil
			}
			return scheduler.Result{}, fmt.Errorf("标记投递完成失败：%w", err)
		}
		// 3. 核验通过 → done；未核验则交回 retry，由 verify 阶段接手
		if err := settleDelivered(ctx, deps.bus, w.Claim, res.Verified, deps.retryDelay); err != nil {
			return scheduler.Result{}, err
		}
		return scheduler.Result{Replied: res.OK}, nil
	}
}

// withErrorLog 在 handler 返回错误前记录完整错误链。
//
// scheduler.complete 只把错误包成固定文案的 safeError，日志里看不到具体类别与原因；
// 这里补上 Kind 和逐层展示信息。字段只含事件标识，不打印正文、prompt、凭据或 cookie。
func withErrorLog(log *zap.Logger, action string, handler scheduler.Handler) scheduler.Handler {
	return func(ctx context.Context, w scheduler.Work) (res scheduler.Result, err error) {
		defer func() {
			if err == nil {
				return
			}
			logWorkFailure(log, action, w, err)
		}()
		return handler(ctx, w)
	}
}

// logWorkFailure 记录一次调度工作失败的完整错误链。
// 已结清的可预期错误（handler 返回 nil）也走这里留痕，避免“不崩循环”变成“没有日志”。
// 字段只含事件标识，不打印正文、prompt、凭据或 cookie。
func logWorkFailure(log *zap.Logger, action string, w scheduler.Work, err error) {
	ev := w.Claim.Event
	log.Error("调度工作失败",
		zap.String("action", action),
		zap.String("event_id", ev.ID),
		zap.String("ch", string(ev.Channel)),
		zap.String("session", ev.Session),
		zap.String("stage", string(w.Claim.Stage)),
		zap.String("error_kind", safeErrorKind(err)),
		zap.String("error_chain", safeErrorChain(err)),
		zap.Error(err),
	)
}

// budgetFailure 判断是否为提示预算超限；只有这类错误值得去掉知识重试。
func budgetFailure(err error) bool {
	var typed *brain.Error
	return errors.As(err, &typed) && typed.Kind == brain.KindBudget
}

// safeErrorKind 提取项目内已知错误类型的类别；未知错误返回空串。
func safeErrorKind(err error) string {
	var brainErr *brain.Error
	if errors.As(err, &brainErr) {
		return string(brainErr.Kind)
	}
	var senderErr *sender.Error
	if errors.As(err, &senderErr) {
		return string(senderErr.Kind)
	}
	return ""
}

// safeErrorChain 展开错误链的逐层展示文本（含 errors.Join 的多分支）。
// 项目内错误类型的 Error() 都承诺不含凭据与正文；未知错误仍只展示其 Error()，不打印结构化字段。
func safeErrorChain(err error) string {
	const maxParts = 8
	parts := make([]string, 0, maxParts)
	var walk func(error)
	walk = func(e error) {
		if e == nil || len(parts) >= maxParts {
			return
		}
		parts = append(parts, e.Error())
		switch wrapped := e.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				walk(child)
			}
		case interface{ Unwrap() error }:
			walk(wrapped.Unwrap())
		}
	}
	walk(err)
	return strings.Join(parts, "：")
}

// settleDelivered 把「已投递」的事件收尾：核验通过就 done，否则交回 retry 让 verify 阶段接手。
func settleDelivered(ctx context.Context, bus event.Bus, c event.Claim, verified bool, retryDelay time.Duration) error {
	from := []event.Status{event.StatusQueued, event.StatusSent}
	if verified {
		if err := bus.Mark(ctx, c, from, event.StatusDone); err != nil {
			return fmt.Errorf("标记完成失败：%w", err)
		}
		return nil
	}
	// Defer 会置 retry 并释放租约，是「未结清 → 可再领取」的正规路径
	if err := bus.Defer(ctx, c, time.Now().UTC().Add(retryDelay)); err != nil {
		return fmt.Errorf("交回重试失败：%w", err)
	}
	return nil
}

// settleFailure 按发送错误类别结清事件。
//
// 只用于 sender 尚未结清的错误（!sender.Handled）：sender 已结清时输入租约已被其事务释放，
// 再拿旧 claim Defer/Mark 只会报「事件租约已失效」，调用方必须先过滤。
//
// 只有 KindRetry（可重试的临时故障）交回 retry；其余（dead/auth/input/skipped/uncertain）
// 一律置 dead——尤其是 uncertain（可能已经发出去了），重投会重复发弹幕，宁可停下来让人看。
func settleFailure(ctx context.Context, bus event.Bus, c event.Claim, cause error, from []event.Status, retryDelay time.Duration) error {
	var sendErr *sender.Error
	if errors.As(cause, &sendErr) && sendErr.Kind == sender.KindRetry {
		if err := bus.Defer(ctx, c, time.Now().UTC().Add(retryDelay)); err != nil {
			return fmt.Errorf("交回重试失败：%w", err)
		}
		return nil
	}
	if err := bus.Mark(ctx, c, from, event.StatusDead); err != nil {
		return fmt.Errorf("标记失败事件失败：%w", err)
	}
	return nil
}
