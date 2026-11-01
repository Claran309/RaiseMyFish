-- P0-EVENT-2：事实时间与执行时间分离；租约代次阻止旧worker覆盖新结果。
ALTER TABLE events ADD COLUMN stage TEXT NOT NULL DEFAULT 'schedule';
ALTER TABLE events ADD COLUMN status_changed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp();
ALTER TABLE events ADD COLUMN lease_owner TEXT;
ALTER TABLE events ADD COLUMN lease_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN lease_until TIMESTAMPTZ;

UPDATE events e SET
    stage = CASE
        WHEN e.status IN ('replying','sent') OR e.dir='out' THEN 'verify'
        WHEN EXISTS (SELECT 1 FROM tasks t WHERE t.event_id=e.id) THEN 'generate'
        ELSE 'schedule' END,
    status_changed_at = created_at,
    lease_owner = CASE WHEN status IN ('queued','replying','sent') THEN 'migration-v2' END,
    lease_version = CASE WHEN status IN ('queued','replying','sent') THEN 1 ELSE 0 END,
    lease_until = CASE WHEN status IN ('queued','replying','sent') THEN clock_timestamp() END;

ALTER TABLE events ADD CONSTRAINT ck_event_stage CHECK(stage IN ('schedule','generate','verify'));
ALTER TABLE events ADD CONSTRAINT ck_event_lease_version CHECK(lease_version >= 0);
ALTER TABLE events ADD CONSTRAINT ck_event_lease_pair CHECK((lease_owner IS NULL) = (lease_until IS NULL));
CREATE INDEX idx_events_claim_due ON events(status, retry_at, at, id) WHERE dir='in';
CREATE INDEX idx_events_lease_expiry ON events(lease_until, id) WHERE dir='in' AND lease_owner IS NOT NULL;
