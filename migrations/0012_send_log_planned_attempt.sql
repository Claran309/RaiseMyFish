-- S05：send_log.attempt_id 同时承载 live 的 send_attempts 与评论/私信的 delivery_attempts。
-- 唯一约束保留（一份尝试只记一行）；删除只指向 live 尝试表的外键。
ALTER TABLE send_log DROP CONSTRAINT IF EXISTS send_log_attempt_id_fkey;
