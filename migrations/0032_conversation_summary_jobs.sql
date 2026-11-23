-- C5：会话滚动摘要任务表。
-- 与 conversation_state 分开：任务表只放租约/重试，摘要正文仍写回 conversation_state。

ALTER TABLE conversation_state ADD COLUMN IF NOT EXISTS summary_event_id TEXT;
ALTER TABLE conversation_state ADD COLUMN IF NOT EXISTS summary_turn_count INT NOT NULL DEFAULT 0;
ALTER TABLE conversation_state ADD COLUMN IF NOT EXISTS summary_updated_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS conversation_summary_jobs (
    conversation_id    TEXT PRIMARY KEY,
    summarized_event_id TEXT,
    turn_count          INT NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'pending',
    lease_owner         TEXT,
    lease_until         TIMESTAMPTZ,
    attempts            INT NOT NULL DEFAULT 0,
    last_error          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_conversation_summary_jobs_status
    ON conversation_summary_jobs(status, created_at);
