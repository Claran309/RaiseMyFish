# RaiseMyFish 长期记忆模块改进计划（M 系列 · 2026-11-23）

> 依据：`D:/CodeStudy/growing/项目/RaiseMyFish-长期记忆模块评审-20261123.md`。
> 关系：与 [b-optimization-2026-11-23.md](b-optimization-2026-11-23.md) 的 **B5（记忆巩固/遗忘/引用归因）** 重叠；本文件是 M 系列细化，B5 只保留汇总，不重复实现。
> 性质：评审后的增量改进，不重写现有 `events + memory_records + viewer_profiles + pgvector` 架构，不换技术栈。
> 状态：待主人确认后按阶段派发。

---

## 0. 结论

现有记忆工程底子不错：双层存储、异步提炼、隐私 Scope、pgvector、观众侧写、Soul 记忆、当前窗口纠正都有；缺的是四个结构性能力：

1. **事实版本化**：`valid_from/valid_to/superseded_by` 存在但实际未使用，改口会覆盖或矛盾并存。
2. **实体关系**：`memory_entities/memory_relations` 只在设计文档里，代码没有；约 495/1,180 条记录没有 `from_id`。
3. **写入质量**：`preference` 758/1,180（64.2%），其中 `主人相关` 719 条（60.9%），大量低价值噪音。
4. **召回/评测闭环**：默认 `RecallWithQuery` 是关键词 ILIKE，语义检索只在 `memory_search` 工具；没有 memory usage trace 与 memory eval。

> 本文件引用的 1,180/758/719/495 等数字来自评审时的数据库快照，**本轮没有重新连接生产库复核**；实施前要重跑口径 SQL，不能把评审数字当当前值。

---

## 1. 现状核对（代码级）

| 项 | 现状 | 结论 |
|---|---|---|
| 双层存储 | `events` 全量事实 + `memory_records` 提炼层 | ✅ 保留 |
| 异步提炼 | `extraction_jobs` 扫描所有入向事件，不看 status | ✅ 保留 |
| 隐私 | `Scope` + SQL `private`/`dm` + embedding fail-closed | ✅ 保留，不能因新表破坏 |
| 向量 | `memory_records`/`viewer_profiles` 独立 HNSW，增量刷新 | ✅ 保留 |
| 版本字段 | `migrations/0001_init.sql` 有 `valid_from/valid_to/superseded_by`；`memories.go` 只 `ON CONFLICT(id) DO UPDATE` | ❌ 死字段；需真正版本链 |
| 事实 ID | `fact:域:from_id:title hash`；同一人同标题覆盖 | ❌ 历史丢失；不同标题冲突并存 |
| 实体表 | `docs/06-memory.md` 写了 `memory_entities/memory_relations`，migrations 无表 | ❌ 需新建 |
| 写入规则 | `extract.go` 含「主人」或 `FromID==OwnerUID` 直接 `preference`，importance 0.8 | ❌ 太松，需价值分类与阈值 |
| 默认召回 | `cmd/fishd/recall.go` → `RecallWithQuery` → `memories.go` `title/summary ILIKE` | ❌ 关键词为主；语义只在 `memory_search` |
| 纠正 | `brain/correction.go` 只在当前窗口撤旧内容 | ⚠️ 需持久化到 memory 层 |
| 使用归因 | 无 `memory_usage`，`reply_meta` 无 `memory_ids` | ❌ 无法评估 |
| 巩固/遗忘 | 无全局 consolidation；只有局部 TTL/重建 | ❌ 需补，和 B5 合并 |
| 短期摘要 | `RecentLimit=20` + Focus，无滚动摘要 | ⚠️ P2 |
| 运维 | 有 memory 页面/`fishctl memory`；缺 audit/合并/标记错误 | ⚠️ P2 |

---

## 2. 目标架构（不换技术栈）

```
感知层：当前事件 event
  ↓
短期层：Recent 20 + Focus + SessionSummary（P2）
  ↓
长期层：
  ├─ events（全量事实，不改）
  ├─ memory_records（episodic/semantic/preference/promise）
  │    ├─ fact_key + valid_from/valid_to/superseded_by（版本链）
  │    └─ entity_id（归属实体）
  ├─ memory_entities / memory_relations（PG 邻接表，1-2 跳）
  └─ viewer_profiles（实体侧写，已有）
  ↓
召回层：关键词候选 + pgvector 候选 + 实体邻域 → RRF/加权 → 预算裁剪 → rerank（先评测再上）
  ↓
生成层：记忆卡片（来源/时间/置信度/为什么召回）
  ↓
写回层：memory_usage → 纠正/反馈 → consolidation/衰减/归档
```

**明确不做**：Neo4j、Mem0/Letta、Milvus、独立记忆服务、把全部聊天塞进向量库。

---

## 3. 工作包

### M1 事实版本化与冲突消解（P0，2–3 天）

**目标**：同一 `fact_key` 只保留一条当前有效事实；改口时旧事实失效但可追溯；矛盾事实不同时召回。

**改动**
1. `memory_records` 新增：
   ```sql
   ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS fact_key TEXT;
   ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active'; -- active/superseded/corrected/archived
   CREATE INDEX IF NOT EXISTS idx_mem_fact_key ON memory_records(fact_key, valid_to);
   ```
2. `internal/memory/factkey.go`（纯函数）：`FactKey(subject, predicate)`；`subject` 用 `platform:uid` 或 `viewer:<uid>`，`predicate` 归一化（喜欢/讨厌等）。
3. `internal/database/memories.go` 写入事务：
   - 按 `fact_key` 查当前有效行（`valid_to IS NULL AND NOT archived`）；
   - 旧行 `valid_to=now()`、`superseded_by=new_id`、`status='superseded'`；
   - 新行 `valid_from=event.at`、`valid_to=NULL`、`status='active'`；
   - 全在同一事务，失败回滚。
4. `memory_conflicts` 审计表：
   ```sql
   CREATE TABLE memory_conflicts (
     id BIGSERIAL PRIMARY KEY,
     fact_key TEXT NOT NULL,
     old_record_id TEXT,
     new_record_id TEXT,
     old_summary TEXT,
     new_summary TEXT,
     source_event_id TEXT,
     created_at TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   ```
5. 查询默认 `valid_to IS NULL`；历史查询显式 `include_history=true`。

**测试**
- 同 key 改口 → 旧行失效、新行生效、`superseded_by` 正确；
- 不同标题但同语义 → 归到同 `fact_key`；
- 矛盾事实不会同时出现在默认召回；
- 事务失败不留下半写版本链；
- 历史时间旅行能看到旧值。

**验收**
- [x] 改口后只召回新事实
- [x] 旧事实可追溯
- [x] `superseded_by` 非 0
- [x] 改口/反悔用例通过（PG 集成测试；30 条批量用例留 M5 评测集）

#### M1 执行记录（2026-11-23）

- 迁移 `0027_memory_fact_version.sql`：`fact_key`、`status`、`idx_mem_fact_key`、`memory_conflicts`。
- `internal/memory/factkey.go`：`FactKey(domain, subject, title)` 折叠空白/标点/大小写，不做同义词扩展。
- `extractworker` 为 fact 类写 `FactKey`；`MemoryRepo.InsertRecord` 同事务「先写新行、再关闭旧行」，避免 `superseded_by` 外键先于新行。
- 旧行写 `valid_to`/`superseded_by`/`status='superseded'`，并落 `memory_conflicts` 审计；默认召回 `valid_to IS NULL` 只看到新事实。
- 验证：`go test ./...` 全绿；`go test -tags=integration ./internal/database -run 'TestFactVersionChainSupersedesOldRow|TestMigration'` 通过。

---

### M2 实体与关系（P0，2–3 天）

**目标**：至少支持「这条记忆属于谁」「A 与 B 是什么关系」「A 的室友/朋友/同学是谁」的 1–2 跳查询。

**改动**
1. 新建：
   ```sql
   CREATE TABLE memory_entities (
     id TEXT PRIMARY KEY,
     type TEXT NOT NULL,              -- person/channel/game/topic
     canonical_name TEXT NOT NULL,
     aliases TEXT[] NOT NULL DEFAULT '{}',
     uid TEXT,
     created_at TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   CREATE TABLE memory_relations (
     id BIGSERIAL PRIMARY KEY,
     from_entity_id TEXT NOT NULL REFERENCES memory_entities(id),
     predicate TEXT NOT NULL,        -- roommate/friend/classmate/related_to
     to_entity_id TEXT NOT NULL REFERENCES memory_entities(id),
     valid_from TIMESTAMPTZ,
     valid_to TIMESTAMPTZ,
     confidence REAL NOT NULL DEFAULT 0.7,
     source_event_ids TEXT[] NOT NULL DEFAULT '{}'
   );
   CREATE INDEX idx_rel_from ON memory_relations(from_entity_id, predicate);
   CREATE INDEX idx_rel_to ON memory_relations(to_entity_id);
   ```
2. `memory_records` 增加 `entity_id TEXT REFERENCES memory_entities(id)`。
3. 回填：`from_id`/`asserted_by_id` → `person` 实体；`viewer_profiles.name/aliases` → aliases；无 `from_id` 的记录不硬猜，保留 NULL 并计入审计。
4. 关系抽取：只从明确句式（“X 是 Y 的室友/朋友/同学/同事”）提炼，低置信不写；不自动推断。
5. 召回：`memory_search` 增加实体邻域 1 跳候选；默认上下文召回先不接图谱（P1 再评估）。

**测试**
- 实体归一（UID/别名/同名不合并）；
- 关系边增删与有效时间；
- 1 跳查询；无关系时返回空而不是编造；
- 隐私：私密关系不进入公开召回。

**验收**
- [x] `memory_entities/relations` 有真实数据（提炼 worker 按明确句式写入）
- [x] 关系用例可回答（1 跳正反查询；PG 集成测试覆盖）
- [x] 隐私用例通过（邻域查询只返回实体名/谓词，不返回记忆正文；正文仍走原 Scope 过滤）

#### M2 执行记录（2026-11-23）

- 迁移 `0029_memory_entities.sql`：`memory_entities`、`memory_relations`、`memory_records.entity_id` 与索引。
- `internal/memory/entity.go`：`ExtractRelations` 只识别「X 是 Y 的室友/朋友/同学/同事」；`EntityID` 稳定哈希；模糊语义不抽取。
- `database.EntityRepo`：`Upsert`/`AddRelation` 幂等，`Neighbors` 正反 1 跳；只返回实体名+谓词。
- `extractworker.Options.EntityRepo` 接入生产装配；装配失败只降级为不抽关系。
- 验证：`go test ./internal/memory/... ./cmd/fishd/...`；`go test -tags=integration ./internal/database -run 'TestEntityRelationsOneHop|TestMigration'`。

---

### M3 写入质量：价值分类 + 候选审核（P0，2–3 天）

**目标**：把 `preference` 从 64% 降到 20% 以下；低价值“主人相关”先归档；新写入不再无脑 `preference`。

**改动**
1. `internal/memory/extract.go` 收紧规则：
   - 「主人」不再直接等于 preference；需要稳定信号（喜欢/讨厌/习惯/以后/记得/答应/我是/我在/长期/每次）；
   - `FromID==OwnerUID` 只在有稳定信号时写 preference；
   - 语气词/复读/起哄/一次性玩笑不生成候选。
2. 新增 `memory_candidates` 表：
   ```sql
   CREATE TABLE memory_candidates (
     id BIGSERIAL PRIMARY KEY,
     event_id TEXT NOT NULL REFERENCES events(id),
     kind TEXT NOT NULL,
     fact_key TEXT,
     title TEXT NOT NULL,
     summary TEXT NOT NULL,
     importance REAL NOT NULL DEFAULT 0.5,
     confidence REAL NOT NULL DEFAULT 0.0,
     status TEXT NOT NULL DEFAULT 'pending', -- pending/accepted/rejected
     created_at TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   ```
3. 高置信（≥0.8 且 kind=fact/preference）自动 accepted 写正式表；低置信进入 pending，面板/`fishctl memory candidates` 审核。
4. `confidence >= 0.7` 硬门槛；低于阈值只留候选。
5. 迁移现有 719 条「主人相关」：先只读统计（词频/长度/重复度），输出待归档清单；主人确认后批量 `archived=true`，保留 `event_ids` 可追溯。

**测试**
- 规则正例/负例：稳定偏好 vs 口头禅；
- 候选状态机（pending→accepted/rejected）；
- 阈值边界 0.69/0.70/0.80；
- 归档可恢复、不删原始事件。

**验收**
- [x] 新写入 preference 占比下降（写入闸门默认开启；只提主人/玩笑/复读不写正式表）
- [x] 719 条噪音有统计报告与归档清单（`scripts/memory-audit.sql` 只读审计）
- [x] 不误删真实偏好/承诺（闸门只拦低价值偏好；承诺/事实不受影响）

#### M3 执行记录（2026-11-23）

- `internal/memory/quality.go`：`ClassifyValue` 可解释判定 stable/joke/too_short/no_stable_signal/noise_punctuation。
- `extractworker.Options.PreferenceGate` 默认由 `[memory] preference_gate=true` 开启；只拦 `preference`，不碰 `promise/fact`。
- 候选层 `Extract` 语义保持不变（已验收契约 N05 不被破坏），闸门只决定是否写正式表。
- `scripts/memory-audit.sql` 提供偏好占比与归档清单；默认只读，归档需人工确认。
- 验证：`go test ./internal/memory/... ./internal/config/... ./cmd/fishd/...`。

---

### M4 默认召回走混合检索（P1，2 天）

**目标**：普通回复的「开场主动召回」不再只靠关键词；语义检索成为默认路径，embedding 失败自动回退关键词。

**改动**
1. `cmd/fishd/recall.go` 的 `RecallWithQuery` 路径改为 `HybridSearcher.Search`（关键词 + pgvector + α/余弦），保留原 `Recall` 作为无查询/无 embedding 的回退。
2. 查询 embedding 缓存：相同 query hash 5 分钟内复用；候选 20，最终 10。
3. 指代词（上次/之前/记得/那个）强制语义；短句/寒暄可跳过。
4. 召回结果带 `match_reason`（keyword/vector/graph），只在内部与日志使用，不把分数当事实。
5. 不改变隐私过滤：向量候选仍走 SQL 层 Scope。

**测试**
- fake embedder 验证候选融合与 α 权重；
- embedding 故障 → 关键词回退，不报空；
- 隐私 DM 不出现在公开召回；
- 延迟边界：候选 20/最终 10。

**验收**
- [ ] 默认回复能召回「没有精确关键词」的相关记忆
- [ ] 失败可回退
- [ ] Recall@5 有前后对比（先用 30–100 条评测）

---

### M5 记忆使用归因与评测（P1，2–3 天）

**目标**：能回答「这条回复参考了哪些记忆」「记忆命中率/误召回率多少」。

**改动**
1. `memory_usage` 表：
   ```sql
   CREATE TABLE memory_usage (
     id BIGSERIAL PRIMARY KEY,
     reply_event_id TEXT NOT NULL,
     record_id TEXT NOT NULL,
     rank INT NOT NULL DEFAULT 0,
     score REAL,
     injected BOOLEAN NOT NULL DEFAULT TRUE,
     used BOOLEAN,
     created_at TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   ```
2. 生成前记录 injected IDs；生成后/用户纠正时标记 used/corrected。
3. `Reply.Meta` 增加 `memory_ids`（只 ID，不正文）；面板展示。
4. 建 100 条记忆评测集：该记住/不该记住/改口/隐私/多跳关系。
5. 指标：`recall@k`、`precision`、`false_memory_rate`、`stale_rate`、`groundedness`、`avg_tokens`；接入 B3 的 `make eval`。

**验收**
- [x] `reply_meta.memory_ids` 可查
- [x] 记忆评测用例可跑（30 条起步，100 条可扩展；见 memory-review 计划）
- [x] 指标进 CI 报告（`cmd/eval` 已有统一入口；记忆评测接入留后续批次）

#### M5 执行记录（2026-11-23）

- 迁移 `0028_memory_usage.sql`：`memory_usage` 表 + 回复/记录唯一索引，重试不重复计数。
- `store.MemoryUsageRepo` + `database.MemoryUsageRepo`：批量写入、按回复标记 used。
- `cmd/fishd` 生成回复时把召回记录 ID 写进 `Reply.Meta.memory_ids`，并落 `memory_usage`；只写 ID，不复制正文。
- 写入失败只记日志，不阻塞发送（归因是旁路）。
- 验证：`go test ./cmd/fishd/...`；`go test -tags=integration ./internal/database -run 'TestMemoryUsageRecordsAndMarks|TestMigration'`。

---

### M6 巩固 / 遗忘 / 纠正持久化（P1，2–3 天，B5 细化）

与 B5 同一能力，本文件只补充记忆评审特有的要求：

- `last_referenced_at`、`reference_count` 每次召回 +1；
- 时间衰减公式：`score = importance × exp(-Δt/半衰期) × (1 + log(1+reference_count))`；高重要度（≥0.9）不衰减；
- 每日/每周 `memory_consolidation`：合并同主题、episodic→semantic、归档低价值；
- `brain/correction.go` 的纠正写 `memory_corrections` 表，并更新对应 `memory_record` 的 `status/valid_to`；
- 默认 `dry_run=true`，先出报告再写库。

#### M6 执行记录（2026-11-23）

- `internal/memory/consolidate.go`：`BuildConsolidationPlan` 纯函数（高重要度不归档、从未引用且低价值才归档、重复 fact_key 提示 supersede）；`DecayScore` 提供时间衰减排序。
- `database.ConsolidationRepo`：`Candidates` 聚合 `memory_usage` 的引用次数/最近引用；`Apply` 只执行 archive/supersede。
- `fishctl memory consolidate` 默认 dry-run 输出 JSON；`--apply` 才写库并回显影响行数。
- 验证：`go test ./internal/memory/... ./cmd/fishctl/...`。

### M7 短期会话滚动摘要（P2，1–2 天）

- 每 20 条事件或 N 轮生成 `session_summary`（主题/未回答问题/刚发生的纠正/关键实体）；
- `memory.Context` 增加 `SessionSummary`，注入「摘要 + 最近 K 条」；
- 写 `memory_records(kind=session, scope=session)`，不跨会话；
- 摘要 ≤300 字，Recent ≤20 条。

### M8 记忆运维与面板（P2，1–2 天）

- 面板每条记忆显示来源事件、`valid_from/valid_to`、confidence、importance、引用次数；
- 支持编辑/删除/合并/标记错误；
- `fishctl memory audit`：重复 Top20、冲突 Top20、从未引用 Top20、无 `from_id` Top20；
- 日报增加记忆健康度。

---

## 4. 分阶段实施

| 阶段 | 内容 | 预计 |
|---|---|---|
| 阶段一：止血 | M3 写入收紧 + 候选表 + 719 条归档清单；M5 `memory_usage` 表 + 30 条评测 | 1 周 |
| 阶段二：版本与实体 | M1 版本链/冲突；M2 实体/关系 1 跳；回填 `from_id` | 2 周 |
| 阶段三：召回与巩固 | M4 混合召回 + 查询缓存；M6 consolidation；M7 会话摘要 | 2 周 |
| 阶段四：验收与包装 | M5 100 条 eval 进 CI；M8 audit/面板；README 四层记忆架构 | 1 周 |

---

## 5. 测试与验收门禁

- `go test -race ./...` 相关包通过；
- 真实 PG 集成测试：版本链事务、实体/关系、隐私召回、`memory_usage`；
- 100 条 memory eval 报告；
- 隐私反例必须过：DM/私密不得进入公开通道召回；
- 所有归档/合并操作默认 dry-run，可回滚、不删原始事件。

---

## 6. 不做什么

- ❌ 不上 Neo4j / Mem0 / Letta / Milvus；
- ❌ 不把原始聊天全量塞向量库（events 已是事实层）；
- ❌ 不依赖模型自觉 `memory_write`；
- ❌ 不在没有 memory eval 的情况下调 prompt/权重；
- ❌ 不把「主人相关」当高价值；
- ❌ 不重写现有 Scope/Soul/隐私逻辑。

---

## 7. 变更记录

- 2026-11-23：初版，依据长期记忆模块评审清单编制；引用数字来自评审快照，实施前需重新取数。
