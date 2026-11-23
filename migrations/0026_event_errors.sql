-- L1 死信止血：按通道×环节×error_kind 持久化失败原因。
--
-- 只存分类与安全摘要，不存 prompt/正文/凭据；message 由调用方先脱敏并截断。
CREATE TABLE IF NOT EXISTS event_errors (
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    stage TEXT NOT NULL,
    error_kind TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_event_errors_lookup ON event_errors(created_at DESC, stage, error_kind);
CREATE INDEX IF NOT EXISTS idx_event_errors_event ON event_errors(event_id, created_at DESC);
