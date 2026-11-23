-- M1 事实版本化：同一 fact_key 只保留一条当前有效事实，改口时旧行失效但可追溯。
--
-- 只做加法：历史行 fact_key 为空，继续按旧行为参与召回；
-- status 默认 active，superseded/corrected/archived 由后续写入显式设置。

ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS fact_key TEXT;
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
CREATE INDEX IF NOT EXISTS idx_mem_fact_key ON memory_records(fact_key, valid_to);

-- 冲突审计：记录一次改口的新旧摘要与来源事件；不存正文之外的额外隐私。
CREATE TABLE IF NOT EXISTS memory_conflicts (
    id BIGSERIAL PRIMARY KEY,
    fact_key TEXT NOT NULL,
    old_record_id TEXT,
    new_record_id TEXT,
    old_summary TEXT NOT NULL DEFAULT '',
    new_summary TEXT NOT NULL DEFAULT '',
    source_event_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_memory_conflicts_key ON memory_conflicts(fact_key, created_at DESC);
