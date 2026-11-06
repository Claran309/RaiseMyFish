-- S04：评论/私信等非直播通道的不可变投递计划与尝试。
-- 计划必须在首次 POST 前落库；尝试在 POST 前 arm。arm 之后崩溃一律按未知处理，
-- 恢复时只核验不重发，避免平台已接受却再次发送。
CREATE TABLE delivery_plans (
 event_id TEXT PRIMARY KEY REFERENCES events(id),
 channel TEXT NOT NULL,
 account_id TEXT NOT NULL,
 destination TEXT NOT NULL,
 text TEXT NOT NULL,
 text_sha TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'ready',
 current_attempt_id TEXT NOT NULL,
 attempt_count INT NOT NULL DEFAULT 1,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 CHECK(channel IN ('comment','dm')),
 CHECK(attempt_count BETWEEN 1 AND 3),
 CHECK(state IN ('ready','armed','accepted','unknown','rejected','not_posted','skipped','dead','manual'))
);
CREATE TABLE delivery_attempts (
 id TEXT PRIMARY KEY,
 event_id TEXT NOT NULL REFERENCES delivery_plans(event_id),
 channel TEXT NOT NULL,
 account_id TEXT NOT NULL,
 destination TEXT NOT NULL,
 text_sha TEXT NOT NULL,
 number INT NOT NULL,
 state TEXT NOT NULL DEFAULT 'ready',
 nonce TEXT NOT NULL,
 armed_at TIMESTAMPTZ,
 permit_until TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 ok BOOLEAN,
 verified BOOLEAN NOT NULL DEFAULT FALSE,
 msg_id TEXT NOT NULL DEFAULT '',
 error_kind TEXT NOT NULL DEFAULT '',
 UNIQUE(event_id,number),
 UNIQUE(account_id,nonce),
 CHECK(number BETWEEN 1 AND 3),
 CHECK(state IN ('ready','armed','accepted','unknown','rejected','not_posted','skipped','dead','manual'))
);
ALTER TABLE delivery_plans ADD CONSTRAINT fk_delivery_current_attempt FOREIGN KEY(current_attempt_id) REFERENCES delivery_attempts(id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX idx_delivery_attempts_event ON delivery_attempts(event_id,number DESC);
