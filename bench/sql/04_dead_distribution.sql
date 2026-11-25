-- dead 事件按通道×环节×error_kind 分布（近 30 天）。
-- 只统计仍为 dead 的入向事件；已重放/已结清的不算。
SELECT e.channel,
       coalesce(NULLIF(x.stage, ''), 'unknown') AS stage,
       coalesce(NULLIF(x.error_kind, ''), 'unknown') AS error_kind,
       count(DISTINCT e.id) AS events,
       count(x.id) AS errors
  FROM events e
  LEFT JOIN event_errors x ON x.event_id = e.id
 WHERE e.dir = 'in'
   AND e.status = 'dead'
   AND e.at >= now() - interval '30 days'
 GROUP BY 1, 2, 3
 ORDER BY events DESC, errors DESC
 LIMIT 50;
