-- R6：记忆提炼积压与处理结果。
SELECT status, count(*) AS rows
  FROM extraction_jobs
 GROUP BY status
 ORDER BY status;

SELECT count(*) AS done_events_without_memory
  FROM events e
 WHERE e.dir='in' AND btrim(e.text) <> ''
   AND NOT EXISTS (SELECT 1 FROM extraction_jobs j WHERE j.event_id=e.id);
