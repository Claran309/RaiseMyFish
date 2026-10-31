-- Phase 0初始业务表；字段和索引对应docs/09-data-model.md。
CREATE TABLE events (
    id TEXT PRIMARY KEY,
    channel TEXT NOT NULL,
    dir TEXT NOT NULL,
    session TEXT NOT NULL,
    from_id TEXT NOT NULL DEFAULT '',
    from_name TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL,
    private BOOLEAN NOT NULL DEFAULT FALSE,
    reply_to TEXT,
    at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    retry_at TIMESTAMPTZ,
    meta JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_events_status ON events(status, at);
CREATE INDEX idx_events_session ON events(channel, session, at);
CREATE INDEX idx_events_private ON events(private, at);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES events(id),
    channel TEXT NOT NULL,
    session TEXT NOT NULL,
    brain TEXT NOT NULL,
    priority SMALLINT NOT NULL DEFAULT 2,
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INT NOT NULL DEFAULT 0,
    deadline TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_tasks_status ON tasks(status, priority, created_at);
CREATE UNIQUE INDEX uq_tasks_event ON tasks(event_id);

CREATE TABLE memory_sources (
    id TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    weight INT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE TABLE memory_records (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL REFERENCES memory_sources(id),
    kind TEXT NOT NULL,
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    importance REAL NOT NULL DEFAULT 0.5,
    confidence REAL NOT NULL DEFAULT 0.8,
    private BOOLEAN NOT NULL DEFAULT FALSE,
    channel TEXT,
    session TEXT,
    valid_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to TIMESTAMPTZ,
    superseded_by TEXT REFERENCES memory_records(id),
    archived BOOLEAN NOT NULL DEFAULT FALSE,
    event_ids TEXT[],
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_mem_valid ON memory_records(valid_to, archived, importance DESC);
CREATE INDEX idx_mem_channel ON memory_records(channel, private);

CREATE TABLE llm_usage (
    id BIGSERIAL PRIMARY KEY,
    at TIMESTAMPTZ NOT NULL DEFAULT now(),
    channel TEXT,
    model TEXT,
    in_tokens INT,
    out_tokens INT,
    cost_usd NUMERIC(10,6),
    ms INT,
    ok BOOLEAN
);
CREATE TABLE send_log (
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT REFERENCES events(id),
    channel TEXT,
    ok BOOLEAN,
    verified BOOLEAN,
    msg_id TEXT,
    error TEXT,
    at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE dlq (
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT,
    stage TEXT,
    error TEXT,
    payload JSONB,
    at TIMESTAMPTZ NOT NULL DEFAULT now(),
    replayed BOOLEAN NOT NULL DEFAULT FALSE
);
