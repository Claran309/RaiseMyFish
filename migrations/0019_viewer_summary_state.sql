-- 观众侧写的来源版本、失败补偿与投影脏状态。
--
-- 来源是生成侧写时用到的公开弹幕 ID 集合；source_hash 由生成器版本+UID+事件正文/时间
-- 规范化计算，保存时在同一事务里重算比对（CAS），来源撤回/改密/删除后不得再落库。
-- last_success 与 fallback 分开记录：模型恢复后仍可判断当前内容是否来自模型。
-- next_retry 是有上限的补偿时间；投影 dirty 用 viewer_summary_jobs 表达，
-- 知识文件写失败只补投影，不重新调用模型。

ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_source_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_source_ids JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_generator TEXT NOT NULL DEFAULT '';
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_keywords JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_last_success_at TIMESTAMPTZ;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_fallback_at TIMESTAMPTZ;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary_next_retry_at TIMESTAMPTZ;
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS summary_attempted_at TIMESTAMPTZ;

-- 投影任务：publish 把档案侧写写入 viewers.md，purge 在来源撤回后删除自动条目。
-- 同一 UID 只保留一条任务，动作可被覆盖（撤回后 publish 必须升级为 purge）。
CREATE TABLE IF NOT EXISTS viewer_summary_jobs (
    uid TEXT PRIMARY KEY,
    action TEXT NOT NULL CHECK (action IN ('publish', 'purge')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS idx_viewer_summary_jobs_due ON viewer_summary_jobs(next_retry_at);
