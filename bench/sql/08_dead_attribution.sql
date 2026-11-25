-- dead 归因覆盖率：多少 dead 事件有 event_errors 记录。
-- 历史 dead 在 0026 迁移之前产生，没有错误分类；报告必须如实呈现，
-- 不能把「没有记录」说成「没有原因」。
SELECT count(*) AS dead_events,
       count(*) FILTER (WHERE x.n > 0) AS with_error_rows,
       count(*) FILTER (WHERE COALESCE(x.n, 0) = 0) AS without_error_rows
  FROM events e
  LEFT JOIN (
    SELECT event_id, count(*) AS n FROM event_errors GROUP BY event_id
  ) x ON x.event_id = e.id
 WHERE e.dir = 'in'
   AND e.status = 'dead'
   AND e.at >= now() - interval '30 days';
