-- 看板注入：操作人写入的临时设定，可带过期时间。
-- ContextBuilder 只读取未过期行；删除即立即失效。
CREATE TABLE context_injections (
    id BIGSERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ
);
CREATE INDEX idx_context_injections_active ON context_injections(expires_at);
