-- 压测终态对账：R1 四档应 unfinished=0；R3/R6 允许保留 backlog 但不丢事件。
SELECT status, count(*) AS rows
  FROM events
 WHERE dir = 'in'
 GROUP BY status
 ORDER BY status;
