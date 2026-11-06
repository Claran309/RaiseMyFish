-- P0-M10：观众档案向量拆三路，避免「鱼说的话」被当成「观众特点」。
--
-- 旧 profile_embedding 是「昵称 + 本人发言 + 鱼的回复」拼成的单个向量：
-- 鱼经常回「白饭」，于是每个被回复过的人都像「爱吃白饭的人」——
-- 用这个向量认人 / 找「谁说过白饭」都会被污染。
--
-- 新口径：
--   identity_embedding    = 昵称（认人用：同音、改名、模糊昵称）
--   facts_embedding       = 本人发言（查「他说过什么 / 谁爱要白饭」）
--   interaction_embedding = 鱼的回复（查互动关系，不参与认人）
--
-- 为什么重命名而不是新建：profile_embedding 的语义已被污染（混了三类内容），
-- 无法安全映射到任何一路，所以改名成 identity 后**立即置空**，
-- 由下一次增量任务按新口径重算。宁可短暂缺失，也不把污染向量留在库里。

DROP INDEX IF EXISTS viewer_profiles_embedding_idx;

ALTER TABLE viewer_profiles RENAME COLUMN profile_embedding TO identity_embedding;
ALTER TABLE viewer_profiles RENAME COLUMN profile_embedding_hash TO identity_embedding_hash;
ALTER TABLE viewer_profiles RENAME COLUMN profile_embedding_updated_at TO identity_embedding_updated_at;

-- 旧向量不符合任何一路口径：全部清空，强制上一轮增量重算。
UPDATE viewer_profiles
SET identity_embedding = NULL,
    identity_embedding_hash = NULL,
    identity_embedding_updated_at = NULL
WHERE identity_embedding IS NOT NULL OR identity_embedding_hash IS NOT NULL;

ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS facts_embedding vector(1024);
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS facts_embedding_hash TEXT;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS facts_embedding_updated_at TIMESTAMPTZ;

ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS interaction_embedding vector(1024);
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS interaction_embedding_hash TEXT;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS interaction_embedding_updated_at TIMESTAMPTZ;

-- 三路各建一个 HNSW 索引：按用途分别检索，互不干扰。
CREATE INDEX IF NOT EXISTS viewer_profiles_identity_embedding_idx
    ON viewer_profiles USING hnsw (identity_embedding vector_cosine_ops);
CREATE INDEX IF NOT EXISTS viewer_profiles_facts_embedding_idx
    ON viewer_profiles USING hnsw (facts_embedding vector_cosine_ops);
CREATE INDEX IF NOT EXISTS viewer_profiles_interaction_embedding_idx
    ON viewer_profiles USING hnsw (interaction_embedding vector_cosine_ops);
