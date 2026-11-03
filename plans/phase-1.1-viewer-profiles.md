# 观众档案（PerViewer Profiles）派发单（P1 起步）

> 目标：对齐 DSH people 档案——按 uid 聚合「谁说过什么/鱼怎么回」，注入 prompt 让鱼认人。
> 不需要开播（从历史 events 聚合即可验证）。

## 一、迁移（viewer_profiles 表）

```sql
CREATE TABLE IF NOT EXISTS viewer_profiles (
    uid          TEXT PRIMARY KEY,           -- 观众 UID（主键：昵称会变，uid 不变）
    name         TEXT NOT NULL,              -- 昵称快照（最近一次）
    hits         INT NOT NULL DEFAULT 0,     -- 说过几句（in 事件数）
    replies      INT NOT NULL DEFAULT 0,     -- 鱼回了几次（out 事件数）
    last_seen    TIMESTAMPTZ NOT NULL,       -- 最后出现
    recent_texts JSONB NOT NULL DEFAULT '[]',-- 最近 15 条他说的话
    recent_replies JSONB NOT NULL DEFAULT '[]', -- 最近 10 条鱼回的
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS idx_viewer_profiles_last_seen ON viewer_profiles(last_seen DESC);
```

- `internal/database/migration.go` 加迁移（新文件 `migrations/0004_viewer_profiles.sql`）
- `internal/store/` 加 ViewerRepo 接口：`UpsertProfile/GetProfile/TopViewers(limit)`

## 二、档案聚合（对齐 DSH people.mjs）

- 从 `events` 聚合：
  - in 事件（dir='in'，from_id 非空）按 from_id 分组：hits 计数、recent_texts（最近 15 条 text）
  - out 事件（dir='out'，meta.input_id 关联）→ replies 计数、recent_replies（最近 10 条）
- **只给 hits ≥ 2 的人建档**（DSH 教训：一次路过不建）
- 重建时机：
  - `extractworker` 加档案重建子任务
  - **每小时节流 + 只在直播活跃时跑**（最近 5 分钟内有 in 事件）
- 测试：造 3 个观众（1 句/2 句/5 句）→ 断言只建 2 个档案、字段正确

## 三、召回与注入

- `internal/memory/reader.go`：`PerViewer(ctx, channel, fromID, limit)` 查档案
- `brain.ContextInput` 加 `Viewer string`（档案片段，约 300-400 字）：
  ```
  【对方档案 · 老熟人】
  - 说过 121 次 / 鱼回过 124 次 / 最后出现 11-02
  ## 他说过的话（最近 8 条）
  - 16:38 没爱了，好歹认识这么久了…
  ## 本鱼回过的（最近 4 条）
  - 没变，就脾气硬了点
  ```
- `serve.go` Generate：从事件 from_id 查档案 → 注入 ContextInput.Viewer
- 空档案不注入（陌生人）

## 四、判断（可选，本批可延后）

`reasoning.Decide` 加 `NeedViewerMemory`：文本含「我/你 + 摸/抱/亲/喜欢/记得」→ 档案注入（默认每轮都注入简版？先每轮注入——档案短）

## 五、配置

```toml
[viewer]
enabled = true
min_hits = 2          # 建档门槛
recent_texts = 15
recent_replies = 10
rebuild_interval_seconds = 3600  # 每小时
```

## 六、测试

- 聚合：3 观众（1/2/5 句）→ 2 档案
- 注入：有档案注入片段、无档案空
- 重建节流：1 小时内不重复、非直播活跃不跑
- 迁移：幂等

## 七、禁止

- 不改 contracts 语义（新表可加）、不碰调度/发送、不执行 git

## 八、交付格式

- 文件列表、迁移、测试结果、配置说明、未做项、是否暂停写入
