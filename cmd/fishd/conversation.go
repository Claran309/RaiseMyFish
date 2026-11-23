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
	case event.ChLive:
		// 直播不建持久会话，只有 30 秒内存短窗口（C3）。
		return cfg.LiveWindowSeconds > 0
	default:
		return false
	}
}

// conversationChannels 返回允许摘要的通道集合，与注入开关保持一致。
//
// 摘要 worker 只处理「注入也开启」的通道：只开 DM 时评论摘要任务不会入队，
// 避免摘要了却从不使用的无谓模型花费。
func conversationChannels(cfg config.Conversation) []string {
	out := make([]string, 0, 2)
	if cfg.DMEnabled {
		out = append(out, string(event.ChDM))
	}
	if cfg.CommentEnabled {
		out = append(out, string(event.ChComment))
	}
	return out
}

// liveWindowFrom 按配置构造直播短窗口；live_window_seconds=0 时返回 nil（关闭）。
//
// 只在直播开关打开时创建：只开私信的项目不该为一个用不到的窗口占内存。
func liveWindowFrom(cfg config.Conversation) *conversation.Window {
	if !cfg.Enabled || cfg.LiveWindowSeconds <= 0 {
		return nil
	}
	return conversation.NewWindow(conversation.WindowOptions{
		TTL: time.Duration(cfg.LiveWindowSeconds) * time.Second,
	})
}

// inConversationRollout 按通道灰度判断该事件是否启用会话状态。
//
// 灰度按 conversation_id 确定性分桶：同一 thread/同一 peer 在整个灰度期内
// 稳定落在同一侧，不会出现「一半轮次有上下文、一半没有」。
func inConversationRollout(cfg config.Conversation, ev event.Event) bool {
	percent := 0
	switch ev.Channel {
	case event.ChDM:
		percent = cfg.DMRolloutPercent
	case event.ChComment:
		percent = cfg.CommentRolloutPercent
	default:
		return false
	}
	id := ev.ConversationID
	if id == "" {
		id = conversation.Key(ev)
	}
	return conversation.InRollout(id, percent)
}

// conversationInput 读取会话摘要与最近轮次；失败只降级为 nil，不阻塞回复。
func conversationInput(ctx context.Context, repo store.ConversationRepo, cfg config.Conversation, window *conversation.Window, ev event.Event, log *zap.Logger) *brain.ConversationContext {
	if !conversationEnabled(cfg, ev.Channel) {
		return nil
	}
	// C3：直播只走内存短窗口，不读持久会话状态。
	if ev.Channel == event.ChLive {
		return liveWindowInput(cfg, window, ev)
	}
	// C2：评论/私信按会话键确定性灰度；关闭时行为与未开启完全一致。
	if !inConversationRollout(cfg, ev) {
		return nil
	}
	if repo == nil {
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

// liveWindowInput 把短窗口里的最近消息转成会话投影；关闭或窗口为空返回 nil。
func liveWindowInput(cfg config.Conversation, window *conversation.Window, ev event.Event) *brain.ConversationContext {
	if window == nil || cfg.LiveWindowSeconds <= 0 {
		return nil
	}
	entries := window.Recent(ev)
	if len(entries) == 0 {
		return nil
	}
	out := &brain.ConversationContext{}
	for _, entry := range entries {
		out.Turns = append(out.Turns, brain.ConversationTurn{From: entry.From, Dir: entry.Dir, Text: clipRunes(entry.Text, 80)})
	}
	return out
}

// rememberLiveWindow 记录一条直播入向消息，供 30 秒内的追问接上下文。
func rememberLiveWindow(window *conversation.Window, ev event.Event) {
	if window == nil || ev.Channel != event.ChLive || ev.Dir != event.DirIn {
		return
	}
	window.Add(ev)
}

// touchConversation 记录本轮发言，推进轮数与过期时间；失败只记日志。
func touchConversation(ctx context.Context, repo store.ConversationRepo, cfg config.Conversation, ev event.Event, log *zap.Logger) {
	if repo == nil || !conversationEnabled(cfg, ev.Channel) {
		return
	}
	// 直播只有内存短窗口，绝不落 conversation_state。
	if ev.Channel == event.ChLive {
		return
	}
	if !inConversationRollout(cfg, ev) {
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
