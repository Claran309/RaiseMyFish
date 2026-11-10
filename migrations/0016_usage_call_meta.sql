-- F07：llm_usage 补齐调用用途、安全关联标识与缓存命中字段。
--
-- purpose 为空只可能是升级前的旧行：不回填、不猜测用途，保持 unknown 语义。
-- channel 为空表示后台增强/embedding 调用，不属于任何对话通道。
-- cached_in_tokens 只在提供方真实返回 prompt_tokens_details.cached_tokens 时写入，
-- 不从其它字段推算。

ALTER TABLE llm_usage ADD COLUMN IF NOT EXISTS purpose TEXT;
ALTER TABLE llm_usage ADD COLUMN IF NOT EXISTS correlation_id TEXT;
ALTER TABLE llm_usage ADD COLUMN IF NOT EXISTS cached_in_tokens INT;
