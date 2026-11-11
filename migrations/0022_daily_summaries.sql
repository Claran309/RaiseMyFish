-- 每日总结：一天一行，按上海时区日期归档；重复生成按天覆盖。
CREATE TABLE daily_summaries (
    day DATE PRIMARY KEY,
    content TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    event_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
