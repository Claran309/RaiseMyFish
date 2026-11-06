package main

// 召回与状态片段：记忆/知识/当前观看状态。
// 所有召回失败都只降级并记日志，不阻塞回复。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/knowledge"
	"RaiseMyFish/internal/memory"
	"go.uber.org/zap"
)

// validBrainRoom 只接受数字房间号，避免把事件里的异常会话值原样写进 prompt。
func validBrainRoom(room string) bool {
	if len(room) == 0 || len(room) > 20 {
		return false
	}
	for _, r := range room {
		if r < '0' || r > '9' {
			return false
		}
	}
	return strings.Trim(room, "0") != ""
}

// brainState 只用「当前 live 采集房间」生成状态摘要。
//
// 为什么不看 Presence：watching 是带 TTL 的旁路状态，采集停止或切换房间后
// 旧值仍会存活一段时间；[watch] current_room 也可能与实际采集房间不一致。
// 未采集时不声称在看直播，宁缺毋滥。
func brainState(currentRoom string, watch config.Watch) string {
	room := strings.TrimSpace(currentRoom)
	if !validBrainRoom(room) {
		return ""
	}
	name := strings.TrimSpace(watch.StreamerName)
	configured := strings.TrimSpace(watch.CurrentRoom)
	// 主播名只在配置房间与当前采集房间一致（或未配置房间）时使用，避免串台。
	if name != "" && (configured == "" || configured == room) {
		return fmt.Sprintf("你正在看%s（房间 %s）的直播，你在她弹幕区发弹幕", name, room)
	}
	return fmt.Sprintf("你正在看房间 %s 的直播，你在其弹幕区发弹幕", room)
}

// brainStateSource 每次读取当前采集房间；读取器为空或未采集时返回空串。
func brainStateSource(currentRoom func() string, watch config.Watch) func(time.Time) string {
	return func(time.Time) string {
		if currentRoom == nil {
			return ""
		}
		return brainState(currentRoom(), watch)
	}
}

// recallMemory 召回记忆与共享事实。
//
//   - mem 为 nil（未装配）→ 返回只带 Scope 的空快照，不报错；
//   - 有查询文本且 Reader 支持 → 摘要优先匹配当前问题，否则退回普通召回；
//   - 召回失败 → 降级为只带 Scope 的空快照 + 记日志，**不阻塞回复**；
//   - 隐私：Scope 由事件派生，Reader 内部按 Scope 做隔离，这里不做任何越界拼装。
func recallMemory(ctx context.Context, mem memory.Reader, scope memory.Scope, query string, log *zap.Logger) (memory.Context, memory.SharedState) {
	empty := memory.Context{Scope: scope}
	emptyShared := memory.SharedState{Scope: scope}
	if mem == nil {
		return empty, emptyShared
	}
	var recalled memory.Context
	var err error
	if reader, ok := mem.(memory.QueryReader); ok && strings.TrimSpace(query) != "" {
		recalled, err = reader.RecallWithQuery(ctx, scope, query)
	} else {
		recalled, err = mem.Recall(ctx, scope)
	}
	if err != nil {
		log.Warn("记忆召回失败，降级为空快照",
			zap.String("action", "memory_recall"),
			zap.String("channel", string(scope.Channel)),
			zap.Error(err))
		return empty, emptyShared
	}
	shared, err := mem.Shared(ctx, scope)
	if err != nil {
		log.Warn("共享事实召回失败，降级为空",
			zap.String("action", "shared_recall"),
			zap.String("channel", string(scope.Channel)),
			zap.Error(err))
		return recalled, emptyShared
	}
	return recalled, shared
}

// recallKnowledge 召回知识库条目。
//
//   - know 为 nil（[knowledge] enabled=false 或未装配）→ 返回 nil，Brain 按无知识处理；
//   - 召回失败 → 降级为 nil + 记日志，不阻塞回复。
func recallKnowledge(ctx context.Context, know knowledge.Reader, ev event.Event, log *zap.Logger) *knowledge.Context {
	if know == nil {
		return nil
	}
	query := knowledge.Query{
		Scope: knowledge.Scope{Channel: ev.Channel, Session: ev.Session, Private: ev.Private},
		Text:  ev.Text,
	}
	got, err := know.Recall(ctx, query)
	if err != nil {
		log.Warn("知识召回失败，降级为无知识",
			zap.String("action", "knowledge_recall"),
			zap.String("channel", string(ev.Channel)),
			zap.Error(err))
		return nil
	}
	return &got
}
