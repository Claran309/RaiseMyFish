-- 重复发送检测：同一 event_id 成功发送超过一次即为异常。
SELECT event_id, count(*) AS ok_sends
  FROM send_log
 WHERE ok IS TRUE
 GROUP BY event_id
HAVING count(*) > 1
 ORDER BY ok_sends DESC, event_id
 LIMIT 50;
