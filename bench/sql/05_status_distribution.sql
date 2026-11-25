-- 事件状态分布（近 30 天）：通道×方向×状态。
SELECT channel, dir, status, count(*) AS n
  FROM events
 WHERE at >= now() - interval '30 days'
 GROUP BY 1, 2, 3
 ORDER BY 1, 2, 3;
