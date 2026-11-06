-- P0-M05：提炼任务表，把「扫 status=done + 事件时间水位」换成显式任务队列。
--
-- 为什么要独立任务表：
--   1. 未回复的入向消息（status=pending/skipped）也有记忆价值，不该等 done；
--   2. 事件「完成」的时间可能晚于后续事件，事件时间水位会把这类晚完成事件排除；
--   3. 提炼版本升级时需要重新提炼同一条事件，需要「事件 + 版本」而不是纯事件游标。
--
-- 一条事件最多一条任务（event_id 主键）；版本升级用 ON CONFLICT 提升 extraction_version
-- 并重置为 pending，不新增行。
CREATE TABLE extraction_jobs (
    event_id TEXT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    extraction_version INT NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'pending',
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT ck_extraction_jobs_status CHECK (status IN ('pending','running','done','failed')),
    CONSTRAINT ck_extraction_jobs_version CHECK (extraction_version >= 1),
    CONSTRAINT ck_extraction_jobs_attempts CHECK (attempts >= 0),
    CONSTRAINT ck_extraction_jobs_lease_pair CHECK ((lease_owner IS NULL) = (lease_until IS NULL))
);

-- 领取：只扫待处理与运行中（运行中用于回收过期租约）。
CREATE INDEX idx_extraction_jobs_claim ON extraction_jobs(created_at, event_id)
    WHERE status IN ('pending','running');

-- 回收：按租约到期时间找出被遗弃的任务。
CREATE INDEX idx_extraction_jobs_lease ON extraction_jobs(lease_until, event_id)
    WHERE lease_owner IS NOT NULL;
