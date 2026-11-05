-- 长期记忆与观众档案语义召回（#38）：复用 K2 的 1024 维 embedding 与 pgvector。
-- 维度与 [embedding].dimension 对齐；改维度必须新迁移，不能原地修改 vector(1024)。
-- 向量列允许为 NULL：未跑 fishctl 入库任务时退回关键词召回，不影响主链路。

CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding vector(1024);
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding_text_hash TEXT;
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding_updated_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS memory_records_embedding_idx
    ON memory_records USING hnsw (embedding vector_cosine_ops);

ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_embedding vector(1024);
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_embedding_hash TEXT;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_embedding_updated_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS viewer_profiles_embedding_idx
    ON viewer_profiles USING hnsw (profile_embedding vector_cosine_ops);
