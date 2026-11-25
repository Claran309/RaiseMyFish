-- 各通道回复/失败：近 30 天入向事件 LEFT JOIN send_log。
-- 口径：同一事件可能多次重试，只按 event_id 去重计数。
WITH in_events AS (
  SELECT id, channel, at
    FROM events
   WHERE dir = 'in'
     AND at >= now() - interval '30 days'
)
SELECT e.channel,
       count(*) AS received,
       count(DISTINCT l.event_id) FILTER (WHERE l.ok IS TRUE) AS replied,
       count(DISTINCT l.event_id) FILTER (WHERE l.ok IS FALSE) AS failed
  FROM in_events e
  LEFT JOIN send_log l ON l.event_id = e.id
 GROUP BY e.channel
 ORDER BY e.channel;
