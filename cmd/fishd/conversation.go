package main

import (
	"context"
	"strings"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/conversation"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// conversationEnabled 按通道判断会话状态是否启用。
func conversationEnabled(cfg config.Conversation, channel event.Channel) bool {
	if !cfg.Enabled {
		return false
	}
	switch channel {
	case event.ChDM:
		return cfg.DMEnabled
	case event.ChComment:
		return cfg.CommentEnabled
	default:
		return false
	}
}

// conversationInput 读取会话摘要与最近轮次；失败只降级为 nil，不阻塞回复。
func conversationInput(ctx context.Context, repo store.ConversationRepo, cfg config.Conversation, ev event.Event, log *zap.Logger) *brain.ConversationContext {
	if repo == nil || !conversationEnabled(cfg, ev.Channel) {
		return nil
	}
	id := ev.ConversationID
	if id == "" {
		id = conversation.Key(ev)
	}
	if id == "" {
		return nil
	}
	now := time.Now().UTC()
	state, ok, err := repo.LoadState(ctx, id, now)
	if err != nil {
		log.Warn("读取会话状态失败，本轮不注入会话段", zap.String("action", "conversation_state"), zap.String("conversation", conversation.Short(id)), zap.Error(err))
		return nil
	}
	turns, err := repo.RecentTurns(ctx, id, cfg.RecentTurns)
	if err != nil {
		log.Warn("读取会话轮次失败，本轮不注入会话段", zap.String("action", "conversation_turns"), zap.String("conversation", conversation.Short(id)), zap.Error(err))
		return nil
	}
	if !ok && len(turns) == 0 {
		return nil
	}
	out := &brain.ConversationContext{}
	if ok {
		out.Summary = clipRunes(strings.TrimSpace(state.Summary), cfg.SummaryMaxRunes)
	}
	for _, turn := range turns {
		out.Turns = append(out.Turns, brain.ConversationTurn{From: turn.FromName, Dir: turn.Dir, Text: clipRunes(turn.Text, 120)})
	}
	if out.Summary == "" && len(out.Turns) == 0 {
		return nil
	}
	return out
}

// touchConversation 记录本轮发言，推进轮数与过期时间；失败只记日志。
func touchConversation(ctx context.Context, repo store.ConversationRepo, cfg config.Conversation, ev event.Event, log *zap.Logger) {
	if repo == nil || !conversationEnabled(cfg, ev.Channel) {
		return
	}
	id := ev.ConversationID
	if id == "" {
		id = conversation.Key(ev)
	}
	if id == "" {
		return
	}
	now := time.Now().UTC()
	state := store.ConversationState{
		ConversationID: id, Channel: string(ev.Channel), UserID: ev.FromID,
		LastEventID: ev.ID, LastTurnAt: now, ExpiresAt: now.Add(time.Duration(cfg.TTLHours) * time.Hour),
	}
	if account, _, ok := event.SplitDMSession(ev.Session); ok {
		state.AccountID = account
	}
	if ev.Channel == event.ChComment {
		state.TopicID = ev.Session
	}
	// 轮数按本会话累计；仓储用 GREATEST 防止回退。
	if current, ok, err := repo.LoadState(ctx, id, now); err == nil && ok {
		state.TurnCount = current.TurnCount + 1
	} else {
		state.TurnCount = 1
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := repo.Touch(writeCtx, state); err != nil {
		log.Warn("更新会话状态失败", zap.String("action", "conversation_touch"), zap.String("conversation", conversation.Short(id)), zap.Error(err))
	}
}

func clipRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
