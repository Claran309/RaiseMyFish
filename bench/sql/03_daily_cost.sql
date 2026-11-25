-- 每日 token/费用：近 30 天，按 UTC 天聚合。
-- unknown_calls = cost_usd 为空（未配置报价）的次数；不能当 0 费用。
SELECT to_char(date_trunc('day', at), 'YYYY-MM-DD') AS day,
       count(*) AS calls,
       coalesce(sum(in_tokens), 0) AS in_tokens,
       coalesce(sum(out_tokens), 0) AS out_tokens,
       sum(cost_usd) AS cost_usd,
       count(*) FILTER (WHERE cost_usd IS NULL) AS unknown_calls
  FROM llm_usage
 WHERE at >= now() - interval '30 days'
 GROUP BY 1
 ORDER BY 1;
