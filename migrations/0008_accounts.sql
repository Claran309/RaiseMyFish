-- B站账号管理（#44）：多账号存储 + 唯一活跃账号。
-- cookie/csrf 为本地明文（个人项目，文档已注明）；API 与日志只出现脱敏值。
-- status: active=验证可用 / invalid=验证失效 / disabled=人工停用。
-- is_active 由部分唯一索引保证全局只有一个 true。

CREATE TABLE IF NOT EXISTS accounts (
    id         TEXT PRIMARY KEY,
    mid        TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    cookie     TEXT NOT NULL,
    csrf       TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'invalid', 'disabled')),
    last_check TIMESTAMPTZ,
    note       TEXT NOT NULL DEFAULT '',
    is_active  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE UNIQUE INDEX IF NOT EXISTS accounts_one_active_idx ON accounts(is_active) WHERE is_active;
CREATE INDEX IF NOT EXISTS accounts_last_check_idx ON accounts(last_check DESC NULLS LAST);
