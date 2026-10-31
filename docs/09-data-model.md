# 09 · 数据模型（PostgreSQL）

> 从 Phase 0 起使用 PostgreSQL。所有表通过迁移管理（`migrations/`）。

## 1. 表清单

| 表 | 用途 | 阶段 |
|---|---|---|
| `events` | 全量事件（事实层） | P0 |
| `tasks` | 调度任务 | P0 |
| `memory_sources` | 来源与权重 | P0 |
| `memory_records` | 长期记忆 | P0（P2 完善） |
| `memory_entities` | 实体 | P2 |
| `memory_relations` | 实体关系 | P2 |
| `memory_embeddings` | 向量（pgvector） | P2（可选） |
| `llm_usage` | 模型调用账本 | P0 |
| `send_log` | 发送记录与核验 | P0 |
| `dlq` | 死信 | P0 |
| `daily_stats` | 日报聚合 | P1 |

## 2. 核心表结构

### 2.1 events

```sql
CREATE TABLE events (
    id          TEXT PRIMARY KEY,           -- 幂等键
    channel     TEXT NOT NULL,              -- live|comment|dm|qq
    dir         TEXT NOT NULL,              -- in|out
    session     TEXT NOT NULL,              -- 房间/BV/uid/群
    from_id     TEXT NOT NULL DEFAULT '',
    from_name   TEXT NOT NULL DEFAULT '',
    text        TEXT NOT NULL,
    private     BOOLEAN NOT NULL DEFAULT FALSE,
    reply_to    TEXT,
    at          TIMESTAMPTZ NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',
    retry_at    TIMESTAMPTZ,
    meta        JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_events_status ON events(status, at);
CREATE INDEX idx_events_session ON events(channel, session, at);
CREATE INDEX idx_events_private ON events(private, at);
```

### 2.2 tasks

```sql
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    event_id    TEXT NOT NULL REFERENCES events(id),
    channel     TEXT NOT NULL,
    session     TEXT NOT NULL,
    brain       TEXT NOT NULL,
    priority    SMALLINT NOT NULL DEFAULT 2,
    status      TEXT NOT NULL DEFAULT 'queued',
    attempts    INT NOT NULL DEFAULT 0,
    deadline    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_tasks_status ON tasks(status, priority, created_at);
CREATE UNIQUE INDEX uq_tasks_event ON tasks(event_id);
```

### 2.3 memory_sources

```sql
CREATE TABLE memory_sources (
    id          TEXT PRIMARY KEY,
    label       TEXT NOT NULL,
    weight      INT NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE
);
```

权重静态 tier（照抄现有 diary）：
| weight | 含义 |
|---|---|
| 100 | 主人直接对话 |
| 80 | 主人互动信号 |
| 60 | 人格与风格 |
| 50 | 表达素材 |
| 30 | 内容消费与产出 |
| 20 | 深度阅读 |

### 2.4 memory_records

```sql
CREATE TABLE memory_records (
    id            TEXT PRIMARY KEY,
    source        TEXT NOT NULL REFERENCES memory_sources(id),
    kind          TEXT NOT NULL,             -- talk|preference|promise|fact|summary|note
    title         TEXT NOT NULL,
    summary       TEXT NOT NULL,
    body          TEXT NOT NULL DEFAULT '',
    importance    REAL NOT NULL DEFAULT 0.5,
    confidence    REAL NOT NULL DEFAULT 0.8,
    private       BOOLEAN NOT NULL DEFAULT FALSE,
    channel       TEXT,
    session       TEXT,
    valid_from    TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to      TIMESTAMPTZ,
    superseded_by TEXT REFERENCES memory_records(id),
    archived      BOOLEAN NOT NULL DEFAULT FALSE,
    event_ids     TEXT[],
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_mem_valid ON memory_records(valid_to, archived, importance DESC);
CREATE INDEX idx_mem_channel ON memory_records(channel, private);
```

### 2.5 llm_usage / send_log / dlq

```sql
CREATE TABLE llm_usage (
    id        BIGSERIAL PRIMARY KEY,
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    channel   TEXT,
    model     TEXT,
    in_tokens INT,
    out_tokens INT,
    cost_usd  NUMERIC(10,6),
    ms        INT,
    ok        BOOLEAN
);

CREATE TABLE send_log (
    id        BIGSERIAL PRIMARY KEY,
    event_id  TEXT REFERENCES events(id),
    channel   TEXT,
    ok        BOOLEAN,
    verified  BOOLEAN,
    msg_id    TEXT,
    error     TEXT,
    at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE dlq (
    id        BIGSERIAL PRIMARY KEY,
    event_id  TEXT,
    stage     TEXT,        -- brain|send|parse
    error     TEXT,
    payload   JSONB,
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    replayed  BOOLEAN NOT NULL DEFAULT FALSE
);
```

## 3. 索引与查询原则

- 所有「按状态 + 时间」的查询必须走索引（调度/恢复）
- 召回查询必须带 `valid_to IS NULL AND archived = FALSE`
- 公开通道查询必须带 `(NOT private)`
- 大表按月分区（`events` 保留 90 天，之后归档）

## 4. 迁移与种子（GORM）

- 数据访问用 **GORM 为主** + pgx 驱动
- 迁移：`migrations/` 文件为主；`AutoMigrate` 仅开发环境，**生产禁用**（lanmei-dream 踩过 AutoMigrate 误删表的坑）
- 迁移必须可重复执行（IF NOT EXISTS / 事务内）
- 不手工改库结构，一律迁移
- 种子：`migrations/seed.sql` 写初始 `memory_sources`

### Phase 0.1 迁移落地

`fishd migrate up` 将 `0001_init.sql` 与 `seed.sql` 嵌入二进制，在单事务和当前数据库/schema的事务advisory lock内执行。额外的基础设施表 `schema_migrations(version BIGINT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ)` 记录版本1及两份SQL的SHA256；重复调用校验后返回，发现checksum/版本变化或无账本的同名表冲突即报错，不覆盖既有表。

seed来源ID为 owner_direct / owner_interaction / persona / expression / content / reading，对应既有六档100/80/60/50/30/20权重。它们是来源字典，不是历史状态或用户记忆导入。业务7表字段保持本节定义；恢复字段等待P0-G01/G02裁决后通过新增迁移补充，不修改已应用0001。

普通启动只检查迁移/必需表并连接PG，不执行AutoMigrate。初始版本不开放自动down，以避免误删事实数据；真实PG集成测试在随机隔离schema内验证冲突回滚并清理自己的schema。

## 4.1 GORM 使用红线（事件/状态机场景）

| 场景 | 写法 |
|---|---|
| 幂等插入 | `clause.OnConflict{DoNothing: true}` |
| 状态迁移 | 条件更新：`Where("status IN ?", from).Updates(...)`，检查 `RowsAffected` |
| 抢占任务 | GORM 无原生 `FOR UPDATE SKIP LOCKED`，用 `Raw()` 手写 |
| 复杂聚合/召回 | `Raw()` 或 `Table().Select()`，不强行链式 |
| 批量写 | `CreateInBatches`，分块 500 |

**禁止**：把用户输入拼进 `Where` 字符串；用 `map` 更新时要注意零值覆盖。

## 5. 连接与调优（16GB 机器）

| 参数 | 建议 |
|---|---|
| `max_connections` | 20（单机够） |
| `shared_buffers` | 256MB |
| `work_mem` | 16MB |
| `maintenance_work_mem` | 128MB |
| 连接池 | 应用侧 10~20 |

## 6. 测试要求

- 每次迁移在空库跑一遍
- repository 层集成测试用真实 PG（Testcontainers 或临时库）
- 幂等插入测试（同 id 两次）
- 版本化测试（同实体两次写 → 旧记录失效）
