-- 记忆记录（memory_records）补 subject 字段：事实要能按 uid 查。
-- 只加列与索引，不动既有数据语义；老记录 from_id 为 NULL（未知主体）。

ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS from_id   TEXT;
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS from_name TEXT;

CREATE INDEX IF NOT EXISTS idx_mem_from_id ON memory_records(from_id, kind);
