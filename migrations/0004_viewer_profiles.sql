-- 观众档案：按 uid 聚合「谁说过什么 / 鱼怎么回」，供 prompt 注入与认人。
-- uid 是主键（昵称会变，uid 不变）；recent_* 存最近若干条原文。

CREATE TABLE IF NOT EXISTS viewer_profiles (
    uid            TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    hits           INT NOT NULL DEFAULT 0,
    replies        INT NOT NULL DEFAULT 0,
    last_seen      TIMESTAMPTZ NOT NULL,
    recent_texts   JSONB NOT NULL DEFAULT '[]',
    recent_replies JSONB NOT NULL DEFAULT '[]',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS idx_viewer_profiles_last_seen ON viewer_profiles(last_seen DESC);
