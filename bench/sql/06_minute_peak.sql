-- 入站分钟峰值与日均：近 30 天。
-- 峰值用于确定压测 1x 的基准，不按 24h 单窗口。
WITH per_min AS (
  SELECT date_trunc('minute', at) AS minute, count(*) AS n
    FROM events
   WHERE dir = 'in'
     AND at >= now() - interval '30 days'
   GROUP BY 1
)
SELECT max(n) AS peak_per_minute,
       round(avg(n)::numeric, 2) AS avg_per_minute,
       count(*) AS active_minutes
  FROM per_min;
