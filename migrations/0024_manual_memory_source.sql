-- 面板手动新增记忆需要一个稳定的 source：manual（人工录入，权重介于主人互动与人格之间）。
-- 幂等：ON CONFLICT DO NOTHING，重复执行无副作用。
INSERT INTO memory_sources (id, label, weight, enabled)
VALUES ('manual', '面板手动录入', 90, true)
ON CONFLICT (id) DO NOTHING;
