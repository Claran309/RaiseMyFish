-- L0 延迟口径：采集时间、采集方式与分阶段计时。
--
-- 设计约束：
--   - 只做加法；历史事件不补造时间，缺失阶段保持 NULL，统计按 unknown 处理。
--   - event_timings 只存时间戳，不存正文/凭据，避免把隐私数据复制一份。
--   - 阶段时间由触发器自动维护，业务代码只需要主动写 generated_at。

ALTER TABLE events ADD COLUMN IF NOT EXISTS collected_at TIMESTAMPTZ;
ALTER TABLE events ADD COLUMN IF NOT EXISTS collect_mode TEXT;
CREATE INDEX IF NOT EXISTS idx_events_collected_at ON events(collected_at);

CREATE TABLE IF NOT EXISTS event_timings (
    event_id TEXT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    collected_at TIMESTAMPTZ,
    enqueued_at TIMESTAMPTZ,
    claimed_at TIMESTAMPTZ,
    generated_at TIMESTAMPTZ,
    send_started_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    verified_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_event_timings_sent_at ON event_timings(sent_at);

-- 入向事件落库即建立计时行；collected_at 缺失时用 created_at 兜底（仅新行）。
CREATE OR REPLACE FUNCTION fish_event_timing_insert() RETURNS trigger AS $$
BEGIN
    IF NEW.dir = 'in' THEN
        INSERT INTO event_timings(event_id, collected_at)
        VALUES (NEW.id, COALESCE(NEW.collected_at, NEW.created_at))
        ON CONFLICT (event_id) DO UPDATE
        SET collected_at = COALESCE(event_timings.collected_at, EXCLUDED.collected_at),
            updated_at = now();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_event_timing_insert ON events;
CREATE TRIGGER trg_event_timing_insert
AFTER INSERT ON events
FOR EACH ROW EXECUTE FUNCTION fish_event_timing_insert();

-- 状态/租约变化自动记录排队、领取与核验完成时间。
CREATE OR REPLACE FUNCTION fish_event_timing_update() RETURNS trigger AS $$
BEGIN
    IF NEW.dir = 'in' THEN
        INSERT INTO event_timings(event_id) VALUES (NEW.id)
        ON CONFLICT (event_id) DO NOTHING;
        UPDATE event_timings
           SET collected_at = COALESCE(collected_at, NEW.collected_at),
               enqueued_at = COALESCE(event_timings.enqueued_at, CASE
                   WHEN NEW.status = 'queued' AND OLD.status IS DISTINCT FROM 'queued'
                   THEN NEW.status_changed_at END),
               claimed_at = COALESCE(event_timings.claimed_at, CASE
                   WHEN NEW.lease_owner IS NOT NULL
                        AND OLD.lease_owner IS DISTINCT FROM NEW.lease_owner
                   THEN clock_timestamp() END),
               verified_at = CASE
                   WHEN NEW.status = 'done' AND OLD.status IS DISTINCT FROM 'done'
                   THEN clock_timestamp() ELSE verified_at END,
               updated_at = now()
         WHERE event_id = NEW.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_event_timing_update ON events;
CREATE TRIGGER trg_event_timing_update
AFTER UPDATE OF status, lease_owner ON events
FOR EACH ROW EXECUTE FUNCTION fish_event_timing_update();

-- 首次成功发送时间取 send_log.at；失败/未确认尝试不覆盖已有成功时间。
CREATE OR REPLACE FUNCTION fish_send_log_timing() RETURNS trigger AS $$
BEGIN
    IF NEW.event_id IS NOT NULL AND NEW.ok IS TRUE THEN
        INSERT INTO event_timings(event_id, sent_at)
        VALUES (NEW.event_id, NEW.at)
        ON CONFLICT (event_id) DO UPDATE
        SET sent_at = COALESCE(event_timings.sent_at, EXCLUDED.sent_at),
            updated_at = now();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_send_log_timing ON send_log;
CREATE TRIGGER trg_send_log_timing
AFTER INSERT OR UPDATE OF ok, at ON send_log
FOR EACH ROW EXECUTE FUNCTION fish_send_log_timing();

-- live 与评论/私信各自在 armed 时写入 send_started_at；重试不覆盖首次开始时间。
CREATE OR REPLACE FUNCTION fish_attempt_timing() RETURNS trigger AS $$
BEGIN
    IF NEW.event_id IS NOT NULL AND NEW.state = 'armed' AND NEW.armed_at IS NOT NULL THEN
        INSERT INTO event_timings(event_id, send_started_at)
        VALUES (NEW.event_id, NEW.armed_at)
        ON CONFLICT (event_id) DO UPDATE
        SET send_started_at = COALESCE(event_timings.send_started_at, EXCLUDED.send_started_at),
            updated_at = now();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_send_attempt_timing ON send_attempts;
CREATE TRIGGER trg_send_attempt_timing
AFTER INSERT OR UPDATE OF state, armed_at ON send_attempts
FOR EACH ROW EXECUTE FUNCTION fish_attempt_timing();

DROP TRIGGER IF EXISTS trg_delivery_attempt_timing ON delivery_attempts;
CREATE TRIGGER trg_delivery_attempt_timing
AFTER INSERT OR UPDATE OF state, armed_at ON delivery_attempts
FOR EACH ROW EXECUTE FUNCTION fish_attempt_timing();
