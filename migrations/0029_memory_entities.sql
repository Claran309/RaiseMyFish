-- M2 实体与关系：支持「这条记忆属于谁」「A 与 B 是什么关系」的 1 跳查询。
-- 只建表与索引；回填与抽取由代码按可信证据执行，不自动推断。

CREATE TABLE IF NOT EXISTS memory_entities (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,              -- person/channel/game/topic
    canonical_name TEXT NOT NULL,
    aliases TEXT[] NOT NULL DEFAULT '{}',
    uid TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_memory_entities_uid ON memory_entities(uid) WHERE uid IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_memory_entities_type_uid ON memory_entities(type, uid) WHERE uid IS NOT NULL;

CREATE TABLE IF NOT EXISTS memory_relations (
    id BIGSERIAL PRIMARY KEY,
    from_entity_id TEXT NOT NULL REFERENCES memory_entities(id),
    predicate TEXT NOT NULL,        -- roommate/friend/classmate/related_to
    to_entity_id TEXT NOT NULL REFERENCES memory_entities(id),
    valid_from TIMESTAMPTZ,
    valid_to TIMESTAMPTZ,
    confidence REAL NOT NULL DEFAULT 0.7,
    source_event_ids TEXT[] NOT NULL DEFAULT '{}',
    CHECK (from_entity_id <> to_entity_id)
);
CREATE INDEX IF NOT EXISTS idx_rel_from ON memory_relations(from_entity_id, predicate);
CREATE INDEX IF NOT EXISTS idx_rel_to ON memory_relations(to_entity_id);

ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS entity_id TEXT REFERENCES memory_entities(id);
CREATE INDEX IF NOT EXISTS idx_mem_entity ON memory_records(entity_id);
