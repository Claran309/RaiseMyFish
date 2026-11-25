-- 串会话检测：回复的 out 事件必须与触发它的 in 事件同通道、同会话、同私密级别。
SELECT count(*) AS cross_session
  FROM events o
  JOIN events i ON i.id = o.reply_to
 WHERE o.dir = 'out'
   AND (o.channel <> i.channel OR o.session <> i.session OR o.private <> i.private);
