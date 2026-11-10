-- R05/R06：侧写推断标记与分层。
--
-- derived_by 记录派生来源（model:<generator> / rule:stats），空表示历史行；
-- evidence 保存每字段的依据类型与证据片段，供面板核对，不进入向量。
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS derived_by TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS evidence JSONB NOT NULL DEFAULT '[]'::jsonb;

-- 侧写分两层：稳定明确自述 + 有期限的近期观察。
-- profile_summary 仍是注入用的权威文本；stable/recent 是它的分层片段，
-- fields 保存每字段的 value/basis/evidence/layer/expiry，用于跨轮修订而不是重写。
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_stable TEXT NOT NULL DEFAULT '';
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_recent TEXT NOT NULL DEFAULT '';
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_recent_until TIMESTAMPTZ;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_fields JSONB NOT NULL DEFAULT '[]'::jsonb;

-- 新发言量与回填预算按发言人计数公开事件；没有索引时每个候选都是一次全表扫描。
CREATE INDEX IF NOT EXISTS idx_events_public_from_at ON events(from_id, at)
WHERE dir = 'in' AND NOT private AND channel <> 'dm';
