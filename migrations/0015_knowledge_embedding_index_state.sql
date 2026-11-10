-- F06：知识向量纳入索引一致性协议（内容 hash / 源 hash / 向量空间指纹）。
--
-- 与 0014 同理，刻意不回填旧行：既有向量的 source_hash/fingerprint 为空，
-- 查询按 fingerprint 过滤后立即排除，由下一次增量任务按新契约重算；
-- 源 hash 为空的旧行允许被新计算结果覆盖，但不会被「补签」成可信状态。

ALTER TABLE knowledge_embeddings ADD COLUMN IF NOT EXISTS text_hash TEXT;
ALTER TABLE knowledge_embeddings ADD COLUMN IF NOT EXISTS source_hash TEXT;
ALTER TABLE knowledge_embeddings ADD COLUMN IF NOT EXISTS fingerprint TEXT;
