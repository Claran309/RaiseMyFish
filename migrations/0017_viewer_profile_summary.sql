-- 观众侧写：定时用 LLM 总结观众公开弹幕，结果供 prompt 认人与知识库召回。
-- profile_summary 为空表示尚未生成；summary_updated_at 只在该字段成功写入时推进，
-- 定时任务据此跳过「摘要后没有新发言」的档案，避免每小时重复付费。

ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS profile_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE viewer_profiles ADD COLUMN IF NOT EXISTS summary_updated_at TIMESTAMPTZ;
