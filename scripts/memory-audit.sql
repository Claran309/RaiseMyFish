-- M3 只读审计：回答「偏好占比多少、哪些是低价值主人相关」。
-- 只读，不改数据；归档清单由人工确认后另跑 UPDATE。

-- 1. 各 kind 占比
SELECT kind, count(*) AS n,
       round(100.0 * count(*) / sum(count(*)) OVER (), 1) AS pct
  FROM memory_records
 WHERE valid_to IS NULL AND NOT archived
 GROUP BY kind
 ORDER BY n DESC;

-- 2. preference 中「主人相关」占比
SELECT count(*) FILTER (WHERE title LIKE '主人相关%') AS owner_related,
       count(*) AS preference_total,
       round(100.0 * count(*) FILTER (WHERE title LIKE '主人相关%') / NULLIF(count(*),0), 1) AS pct
  FROM memory_records
 WHERE valid_to IS NULL AND NOT archived AND kind = 'preference';

-- 3. 候选归档清单：主人相关且摘要很短/像玩笑
SELECT id, summary, importance, confidence, valid_from
  FROM memory_records
 WHERE valid_to IS NULL AND NOT archived
   AND kind = 'preference'
   AND title LIKE '主人相关%'
   AND (char_length(summary) < 8 OR summary ~ '(哈哈|笑死|hhh|草|乐|绷|复读|整活)')
 ORDER BY valid_from DESC
 LIMIT 200;
