package main

// 本文件实现统一上下文快照：普通 Brain、ReAct、主动弹幕共用同一份资料。
//
// 边界：
//   - 权限与私密过滤在 repository 的查询层执行，这里不写 SQL、不自行越界拼装；
//   - 召回失败只降级为空快照并记日志，不阻塞回复；
//   - 「档案读取失败」与「陌生人」必须分开：前者注入明确状态，不能伪装成没有档案。

import (
	"context"
	"errors"
	"strings"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/persona"
	"RaiseMyFish/internal/reasoning"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// viewerUnavailableText 是档案读取失败的显式标记。
//
// 陌生人（ErrNotFound）不注入任何片段；真实故障如果也返回空，模型会把
// 「查不到」当成「不认识」，所以这里给一条状态，让模型知道本轮无法确认身份。
const viewerUnavailableText = "【对方档案读取失败，本轮无法确认其身份】"

// contextBuilder 组装一次生成所需的全部资料。
type contextBuilder struct {
	pack      persona.Pack
	memory    memory.Reader
	knowledge knowledge.Reader
	viewers   store.ViewerRepo
	soul      *soulCache
	state     func(time.Time) string
	log       *zap.Logger
}

// newContextBuilder 构造快照装配器；可选依赖为 nil 时对应区块留空。
func newContextBuilder(pack persona.Pack, mem memory.Reader, know knowledge.Reader, viewers store.ViewerRepo, soul *soulCache, state func(time.Time) string, log *zap.Logger) *contextBuilder {
	if log == nil {
		log = zap.NewNop()
	}
	return &contextBuilder{
		pack:      pack,
		memory:    mem,
		knowledge: know,
		viewers:   viewers,
		soul:      soul,
		state:     state,
		log:       log,
	}
}

// BuildContext 组装统一快照。
//
// 记忆（同会话最近消息 + 跨通道公开消息 + 摘要 + 共享事实）默认注入；
// visual 由调用方提供（主动弹幕识图，被动为空）；
// withKnowledge 由调用方按意图决定，避免每条短句都触发语义检索。
func (b *contextBuilder) BuildContext(ctx context.Context, ev event.Event, visual string, withKnowledge bool) brain.ContextInput {
	scope := memory.Scope{Channel: ev.Channel, Session: ev.Session, Private: ev.Private}
	memCtx, shared := recallMemory(ctx, b.memory, scope, ev.Text, ev.FromID, b.log)

	var knowCtx *knowledge.Context
	if withKnowledge {
		knowCtx = recallKnowledge(ctx, b.knowledge, ev, b.log)
	}

	stateText := ""
	if b.state != nil {
		stateText = b.state(time.Now())
	}

	return brain.ContextInput{
		Event:     ev,
		Persona:   b.pack,
		Memory:    memCtx,
		Shared:    shared,
		Knowledge: knowCtx,
		State:     stateText,
		Visual:    strings.TrimSpace(visual),
		Viewer:    b.viewerText(ctx, ev),
		Soul:      b.soul.load(ctx),
	}
}

// viewerText 认人：陌生人（无档案）不注入；读取失败注入显式状态。
func (b *contextBuilder) viewerText(ctx context.Context, ev event.Event) string {
	if b.viewers == nil || strings.TrimSpace(ev.FromID) == "" {
		return ""
	}
	profile, err := b.viewers.GetProfile(ctx, ev.FromID)
	if err == nil {
		return formatViewerProfile(profile)
	}
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	b.log.Warn("观众档案读取失败，本轮不注入档案",
		zap.String("action", "viewer_profile"),
		zap.String("event_id", ev.ID),
		zap.String("ch", string(ev.Channel)),
		zap.Error(err),
	)
	return viewerUnavailableText
}

// buildPassiveInput 是被动回复的统一快照入口。
//
// 近期记忆无条件注入（C04）：连续对话不能靠「刚才/记得」这类关键词才召回；
// 知识仍按意图补充，避免短句也触发语义检索。
func buildPassiveInput(ctx context.Context, builder *contextBuilder, ev event.Event) (brain.ContextInput, reasoning.Decision) {
	decision := reasoning.Decide(ev.Text)
	return builder.BuildContext(ctx, ev, "", decision.NeedKnowledge), decision
}
