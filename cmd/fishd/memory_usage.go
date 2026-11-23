package main

import (
	"context"
	"encoding/json"
	"time"

	"RaiseMyFish/internal/brain"
	"RaiseMyFish/internal/memory"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// memoryIDs 从召回快照里提取记录 ID（去重保序）。
func memoryIDs(records []memory.Record) []string {
	seen := map[string]bool{}
	var ids []string
	for _, record := range records {
		if record.ID == "" || seen[record.ID] {
			continue
		}
		seen[record.ID] = true
		ids = append(ids, record.ID)
	}
	return ids
}

// withMemoryMeta 把 memory_ids 写进回复 meta；只写 ID，不写正文。
func withMemoryMeta(reply brain.Reply, ids []string) brain.Reply {
	if len(ids) == 0 {
		return reply
	}
	fields := map[string]any{}
	if len(reply.Meta) > 0 {
		_ = json.Unmarshal(reply.Meta, &fields)
	}
	fields["memory_ids"] = ids
	if data, err := json.Marshal(fields); err == nil {
		reply.Meta = data
	}
	return reply
}

// recordMemoryUsage 写回复级归因；失败只记日志，不阻塞发送。
func recordMemoryUsage(ctx context.Context, repo store.MemoryUsageRepo, log *zap.Logger, replyEventID string, ids []string) {
	if repo == nil || len(ids) == 0 {
		return
	}
	rows := make([]store.MemoryUsage, 0, len(ids))
	for i, id := range ids {
		rows = append(rows, store.MemoryUsage{ReplyEventID: replyEventID, RecordID: id, Rank: i, Injected: true})
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := repo.RecordMemoryUsage(writeCtx, rows); err != nil {
		log.Warn("写入记忆使用记录失败", zap.String("action", "memory_usage"), zap.String("event_id", replyEventID), zap.Error(err))
	}
}
