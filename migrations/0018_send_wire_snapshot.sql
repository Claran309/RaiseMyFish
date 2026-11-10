-- 发送计划落库前固定 wire text 与结构化 @ 目标。
-- text_version=1 是升级前的旧计划（text 为模型原文，@ 目标不落库）；
-- text_version=2 起 text 即实际 wire text，reply_mid/reply_name 一并持久化。
ALTER TABLE send_plans ADD COLUMN IF NOT EXISTS reply_mid TEXT NOT NULL DEFAULT '';
ALTER TABLE send_plans ADD COLUMN IF NOT EXISTS reply_name TEXT NOT NULL DEFAULT '';
ALTER TABLE send_plans ADD COLUMN IF NOT EXISTS text_version SMALLINT NOT NULL DEFAULT 1;
