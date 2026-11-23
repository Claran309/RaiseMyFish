-- M5 记忆使用归因：记录每条回复注入了哪些记忆、是否被使用/纠正。
-- 只存 ID/排名/分数/布尔，不复制记忆正文。

CREATE TABLE IF NOT EXISTS memory_usage (
    id BIGSERIAL PRIMARY KEY,
    reply_event_id TEXT NOT NULL,
    record_id TEXT NOT NULL,
    rank INT NOT NULL DEFAULT 0,
    score REAL,
    injected BOOLEAN NOT NULL DEFAULT TRUE,
    used BOOLEAN,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_memory_usage_reply ON memory_usage(reply_event_id, rank);
CREATE INDEX IF NOT EXISTS idx_memory_usage_record ON memory_usage(record_id, created_at DESC);
-- 同一回复的同一条记忆只记一次；重放/重试不重复计数。
CREATE UNIQUE INDEX IF NOT EXISTS uq_memory_usage_reply_record ON memory_usage(reply_event_id, record_id);
