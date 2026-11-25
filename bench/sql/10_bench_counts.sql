-- staging 对账：压测前后跑同一份，events/send_log/llm_usage 必须可解释。
SELECT 'events' AS table_name, count(*) AS rows FROM events
UNION ALL SELECT 'send_log', count(*) FROM send_log
UNION ALL SELECT 'llm_usage', count(*) FROM llm_usage
UNION ALL SELECT 'extraction_jobs', count(*) FROM extraction_jobs
UNION ALL SELECT 'event_errors', count(*) FROM event_errors
UNION ALL SELECT 'dlq', count(*) FROM dlq;
