-- 知识库语义检索（K2）：智谱 embedding-3 生成 1024 维向量，存 pgvector。
-- 维度与 internal/knowledge.EmbeddingDimension、[embedding].dimension 对齐；
-- 改维度必须新迁移，不能原地修改已有的 vector(1024)。

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS knowledge_embeddings (
    id         TEXT PRIMARY KEY,
    source     TEXT NOT NULL,
    title      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    embedding  vector(1024)
);

CREATE INDEX IF NOT EXISTS knowledge_embeddings_vec_idx
    ON knowledge_embeddings USING hnsw (embedding vector_cosine_ops);
