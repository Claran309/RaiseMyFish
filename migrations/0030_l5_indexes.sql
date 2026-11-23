-- L5 索引收尾：只补查询路径缺失的组合索引，不新增字段。
-- 现有单列索引保留；组合索引用于「通道 + 方向 + 状态 + 时间」与发送审计。

CREATE INDEX IF NOT EXISTS idx_events_channel_dir_status_at ON events(channel, dir, status, at DESC);
CREATE INDEX IF NOT EXISTS idx_send_log_event_ok_at ON send_log(event_id, ok, at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_usage_record_created ON memory_usage(record_id, created_at DESC);
