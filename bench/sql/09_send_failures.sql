-- 失败发送：近 30 天 send_log 失败的通道与错误类别。
-- 老数据的 error 可能为空；空值单独归为 unknown，不丢样本。
SELECT coalesce(NULLIF(channel, ''), 'unknown') AS channel,
       coalesce(NULLIF(error, ''), 'unknown') AS error_kind,
       count(*) AS attempts,
       count(DISTINCT event_id) AS events
  FROM send_log
 WHERE ok IS FALSE
   AND at >= now() - interval '30 days'
 GROUP BY 1, 2
 ORDER BY attempts DESC
 LIMIT 50;
