-- C2/N08：向量索引状态拆三层（源 hash / 文本 hash / 索引指纹）。
--
-- 背景：
--   1) 摘要更新保留旧向量：查询只检查「向量非空」，于是用旧语义给新正文排序；
--   2) 后台读取旧摘要后，另一路重建/清洗并清空向量，旧 embedding 计算返回后
--      仍按主键覆盖写回，把刚清除的污染向量重新种回去。
--
-- 三层各司其职：
--   *_embedding_source_hash  源字段（快照/清洗后要重算的那份内容）的 hash，
--                            由 SQL 从原始列直接计算；查询要求它与当前源一致，
--                            写回要求它与读取时一致（CAS）。
--   *_embedding_hash         被向量化文本的 hash，只用于「内容没变就跳过」。
--   *_embedding_fingerprint  向量空间指纹（提供方/模型/维度/输入模板），
--                            同维度换模型也不能复用旧向量空间。
--
-- 刻意不回填旧行：既有向量的 source/fingerprint 均为 NULL，
-- 迁移后立即被查询排除，由下一次增量任务按新契约重算；
-- 把未知出处的向量「补签」成可信状态等于把潜在污染永久合法化。

ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding_source_hash TEXT;
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS embedding_fingerprint TEXT;

ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS identity_embedding_source_hash TEXT;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS identity_embedding_fingerprint TEXT;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS facts_embedding_source_hash TEXT;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS facts_embedding_fingerprint TEXT;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS interaction_embedding_source_hash TEXT;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS interaction_embedding_fingerprint TEXT;
