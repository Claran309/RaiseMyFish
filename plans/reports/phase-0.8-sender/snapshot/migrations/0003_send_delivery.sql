-- P0-SEND-1：计划保存不可变回复，尝试保存投递许可与核验证据。
ALTER TABLE events DROP CONSTRAINT ck_event_stage;
ALTER TABLE events ADD CONSTRAINT ck_event_stage CHECK(stage IN ('schedule','generate','deliver','verify'));
CREATE TABLE send_plans (
 event_id TEXT PRIMARY KEY REFERENCES events(id),
 actor_id TEXT NOT NULL,
 room_id TEXT NOT NULL,
 text TEXT NOT NULL,
 text_sha TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'ready',
 current_attempt_id TEXT NOT NULL,
 attempt_count INT NOT NULL DEFAULT 1,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE send_attempts (
 id TEXT PRIMARY KEY,
 event_id TEXT NOT NULL REFERENCES send_plans(event_id),
 actor_id TEXT NOT NULL,
 room_id TEXT NOT NULL,
 text_sha TEXT NOT NULL,
 number INT NOT NULL,
 state TEXT NOT NULL DEFAULT 'ready',
 nonce TEXT,
 permit_until TIMESTAMPTZ,
 armed_at TIMESTAMPTZ,
 armed_owner TEXT,
 armed_version BIGINT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 verify_count INT NOT NULL DEFAULT 0,
 prep_failures INT NOT NULL DEFAULT 0,
 ok BOOLEAN,
 verified BOOLEAN NOT NULL DEFAULT FALSE,
 msg_id TEXT NOT NULL DEFAULT '',
 evidence TEXT NOT NULL DEFAULT 'none',
 error_kind TEXT NOT NULL DEFAULT '',
 UNIQUE(event_id,number),
 UNIQUE(actor_id,nonce),
 CHECK(number BETWEEN 1 AND 3),
 CHECK(verify_count BETWEEN 0 AND 3),
 CHECK(prep_failures BETWEEN 0 AND 3),
 CHECK(state IN ('ready','armed','accepted','unknown','rejected','not_posted','skipped','dead','manual'))
);
ALTER TABLE send_plans ADD CONSTRAINT fk_send_current_attempt FOREIGN KEY(current_attempt_id) REFERENCES send_attempts(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE send_log ADD COLUMN attempt_id TEXT UNIQUE REFERENCES send_attempts(id);
CREATE INDEX idx_send_actor_permit ON send_attempts(actor_id,permit_until DESC) WHERE armed_at IS NOT NULL;
CREATE INDEX idx_send_duplicate ON send_attempts(actor_id,room_id,text_sha,permit_until DESC) WHERE armed_at IS NOT NULL;
