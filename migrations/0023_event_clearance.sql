-- 异常清理：只标记 cleared_at，不改事件状态机；面板查询过滤已清理行。
ALTER TABLE events ADD COLUMN cleared_at TIMESTAMPTZ;
CREATE INDEX idx_events_uncleared_errors ON events(at DESC)
    WHERE cleared_at IS NULL AND status IN ('dead', 'retry');
