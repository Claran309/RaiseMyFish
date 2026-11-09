-- A2/N05：把「谁说的（asserted_by）」与「说的是谁（subject）」分开落库。
-- 旧行的 from_id/from_name 历史上存的是发言人；新行中它们表示已验证的陈述主体，
-- subject 为空表示旧数据，重建长期设定时按说话人推导并按 fail-closed 处理。
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS asserted_by_id   TEXT;
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS asserted_by_name TEXT;
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS subject          TEXT;

CREATE INDEX IF NOT EXISTS idx_mem_asserted_by ON memory_records(asserted_by_id, kind);
