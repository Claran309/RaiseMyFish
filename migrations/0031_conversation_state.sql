-- C0/C1：会话状态。
-- events.conversation_id 由采集器按通道语义写入；为空表示旧逻辑。
-- conversation_state 只存摘要/最近轮次元数据，不复制消息正文。

ALTER TABLE events ADD COLUMN IF NOT EXISTS conversation_id TEXT;
CREATE INDEX IF NOT EXISTS idx_events_conversation_at
    ON events(conversation_id, at DESC, id DESC);

CREATE TABLE IF NOT EXISTS conversation_state (
    conversation_id TEXT PRIMARY KEY,
    channel         TEXT NOT NULL,
    account_id      TEXT NOT NULL DEFAULT '',
    user_id         TEXT NOT NULL DEFAULT '',
    topic_id        TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    unanswered      TEXT NOT NULL DEFAULT '',
    turn_count      INT  NOT NULL DEFAULT 0,
    last_event_id   TEXT,
    last_turn_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_conversation_state_expires
    ON conversation_state(expires_at);
CREATE INDEX IF NOT EXISTS idx_conversation_state_channel
    ON conversation_state(channel, last_turn_at DESC);
