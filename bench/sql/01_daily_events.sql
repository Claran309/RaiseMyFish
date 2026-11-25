-- 每日事件量：按天、通道、方向聚合。
-- 口径：events.at（平台/入队时间），不是 created_at；跨天按 UTC 切日。
SELECT to_char(date_trunc('day', at), 'YYYY-MM-DD') AS day,
       channel,
       dir,
       count(*) AS n
  FROM events
 WHERE at >= now() - interval '30 days'
 GROUP BY 1, 2, 3
 ORDER BY 1, 2, 3;
