-- 压测延迟口径：events.at -> 首次 send_log.ok（与线上报告一致，不另立口径）。
WITH first_ok AS (
  SELECT DISTINCT ON (l.event_id)
         e.channel, e.at AS event_at, l.at AS sent_at
    FROM send_log l
    JOIN events e ON e.id = l.event_id
   WHERE l.ok IS TRUE AND e.dir = 'in' AND l.at >= e.at
   ORDER BY l.event_id, l.at ASC
)
SELECT channel,
       count(*) AS samples,
       percentile_cont(0.50) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at-event_at))) AS p50_s,
       percentile_cont(0.90) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at-event_at))) AS p90_s,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (sent_at-event_at))) AS p99_s,
       max(EXTRACT(EPOCH FROM (sent_at-event_at))) AS max_s
  FROM first_ok
 GROUP BY channel
 ORDER BY channel;
