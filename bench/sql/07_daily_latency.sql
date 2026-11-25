-- 每日延迟分位：每条入向事件取第一次成功发送，按事件天聚合。
-- 口径：send_log.at - events.at（与 /api/status、fishctl status 一致）。
WITH first_ok AS (
  SELECT DISTINCT ON (l.event_id)
         e.channel,
         e.at AS event_at,
         l.at AS sent_at,
         to_char(date_trunc('day', e.at), 'YYYY-MM-DD') AS day
    FROM send_log l
    JOIN events e ON e.id = l.event_id
   WHERE l.ok IS TRUE
     AND e.dir = 'in'
     AND e.at >= now() - interval '30 days'
     AND l.at >= e.at
   ORDER BY l.event_id, l.at ASC
)
SELECT day,
       channel,
       count(*) AS n,
       round(percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::numeric, 0) AS p50_ms,
       round(percentile_cont(0.9) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::numeric, 0) AS p90_ms,
       round(percentile_cont(0.99) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at - event_at)) * 1000)::numeric, 0) AS p99_ms
  FROM first_ok
 GROUP BY 1, 2
 ORDER BY 1, 2;
