# RaiseMyFish 延迟诊断与工程质量优化计划（L 系列 · 2026-11-23）

> 依据：`D:/CodeStudy/growing/项目/RaiseMyFish-延迟诊断与优化清单-20261123.md`。
> 关系：与 [b-optimization-2026-11-23.md](b-optimization-2026-11-23.md)（B1–B11）并行；本文件是 **L 系列**，重点不是“堆功能”，而是先把延迟口径修对、把评论 dead 止住。
> 性质：改进工作包，不新增 Phase、不换技术栈、不改既有契约边界。
> 状态：待主人确认后按批次派发。

### 0.1 本轮已落地（2026-11-23）

| 项 | 状态 | 说明 |
|---|---|---|
| 3.7 文档口径 | ✅ 已写 | `docs/10-observability-cost.md` 新增「延迟口径」章节：平台时间/采集时间/生成时间/发送时间定义与公式、fresh/all、n<30、backfill 规则 |
| 8.5 排查手册 | ✅ 已写 | `docs/runbook.md` 新增「评论/私信延迟排查」：先看 fresh/all、再拆阶段、再看 dead/skipped 原因 |
| 3.1 `collected_at` | ⏳ 待做 | 需迁移 `0025`；注意 `0024_manual_memory_source.sql` 尚未接入 `migrationSteps()`，必须先核对迁移链 |
| 3.2 `event_timings` | ⏳ 待做 | 新表 + TimingRepo + collector/scheduler/generate/sender 写入点 |
| 3.3–3.6 | ⏳ 待做 | 5 段延迟、backfill、fresh/all、SLO |
| L1–L5 | ⏳ 待做 | 见后文 |

---

## 0. 一句话结论

1. **评论 P50 48 秒、私信 P50 84 秒不是“AI 慢”**，是轮询间隔（60/120 秒）的物理下限；当前统计还混入了 backfill 旧评论，P90 6.7 小时不可直接用于简历。
2. **先做测量口径**（`collected_at` + `event_timings` + fresh/all 分组），否则后面的优化都是在优化假指标。
3. **真正优先修的是评论 dead 405 条**（近 30 天 comment done 175 / dead 405，dead 率 68.7%）；先补错误分类与持久化，再谈降延迟。
4. **不引入 Redis/MQ/多 Agent**：瓶颈是轮询与死信，不是架构不够复杂；延迟目标按“2 分钟内必回、不漏、不重”定，不追求直播的 1 秒级。

---

## 1. 现状盘点（按补充清单逐项核对）

> 证据来自当前工作区源码；「缺失」= 没有找到实现，不代表设计上永远不做。

| 清单项 | 当前实现 | 差距 |
|---|---|---|
| **3.1 `collected_at`** | `events` 表只有 `at`（平台时间）、`created_at`；`event.Event` 只有 `At` | 没有采集时间；`internal/database/stats.go` 用 `e.at` 算延迟 |
| **3.2 `event_timings`** | `events` 有 `stage/status_changed_at`；`send_log` 有发送时间；没有独立阶段表 | 缺 `enqueued_at/claimed_at/generated_at/send_started_at/verified_at` |
| **3.3 5 段延迟** | `latencyRowsSQL` 只算 `sent_at - event_at`（平台时间 → 首次成功发送） | 无采集/排队/生成/发送拆分 |
| **3.4 backfill 标记** | comment 有 `meta.source = "bili-comment"`；没有 `collect_mode` | 补历史与正常轮询无法区分 |
| **3.5 统计口径** | `LatencyStat` 只有 Count/P50/P90/Avg/Max；`fishctl status`/面板按 24h 单窗口 | 没有 `all` 与 `fresh(<5m)` 两组，也没有 `n<30` 提示 |
| **3.6 固定 SLO** | 无 | 需要写入配置/文档并在面板展示目标 |
| **3.7 文档** | `docs/10-observability-cost.md` 没有“平台时间/采集时间/生成时间/发送时间”定义 | 补文档 |
| **4.1 错误分类持久化** | `logWorkFailure` 只写 zap；`send_attempts` 有 `error_kind`，但 `events`/`event_errors` 没有 | 补 `error_kind` 落库 |
| **4.2 dead 原因面板** | 有错误列表/日志，没有按通道×环节×error_kind 的分布 | 补统计查询与页面 |
| **4.3 重试与降级** | 已有 retry/dead 状态机、预算裁剪、部分 provider 重试 | 需按 error_kind 补策略与最大次数 |
| **4.4 DLQ 重放** | 有 `fishctl dlq`? 需核对；当前死信有 9 条未重放 | 补 list/replay + 幂等检查 |
| **4.5 评论/私信回复窗口** | `MaxEventAge` 只对 `live` 生效（`internal/scheduler/scheduler.go:323-325`） | comment/dm 需独立 `max_event_age_seconds` |
| **4.6 每通道最大事件年龄** | 同 4.5 | 补 comment/dm 配置 |
| **4.7 告警** | 有静默告警/日报基础 | 补 dead 率、DLQ、队列最老事件告警 |
| **5.1 轮询参数** | comment `feed_interval_seconds=60`、`interval_seconds=60`、`discover_interval_seconds=600`；dm `interval_seconds=120` | 需逐步调 20/30/180、45/60 |
| **5.2 自适应轮询** | 固定间隔 | 缺活跃/空闲/412/429 退避与抖动 |
| **5.3 增量游标** | comment 有进程内 `frontierAt/frontierID`；dm 有 `state/dm-cursors.json` | comment 游标未持久化；dm 未按会话 `last_msg_id` 扩展 |
| **5.4 并行与限流** | 采集器按现有循环；无有界并发/每轮时间预算 | 补多账号/多目标有界并发 |
| **6.1 按通道配额** | `scheduler.max_concurrent=16`、`llm.max_concurrent=8` 全局共享 | 无 live/comment/dm 软配额 |
| **6.2 优先级** | 调度器有优先级/优先堆基础 | 未按 backfill/fresh 分层防挤占 |
| **6.3 快慢双路径** | comment/dm 恒走 ReAct（`shouldUseReAct` 固定 true） | 缺短评论/私信单步路径 |
| **6.4 Prompt 预算** | 有全局 prompt 预算 | 无 comment/dm 独立预算与 tokens/reply 统计 |
| **6.5 模型路由** | 支持多 provider 路由 | 没有 per-channel routing |
| **6.6 合并重复请求** | 有会话锁/幂等；无同用户短时间合并 | 补时间窗去重 |
| **7.1 面板** | 有延迟卡/错误页 | 缺 fresh P50/P90/P99、队列深度、最老事件、dead/skip 分布 |
| **7.2 Trace** | 无 per-event trace（见 B2） | 与 B2 合并做，不要两套 |
| **7.3 日报** | 有日报基础 | 补 fresh/all、dead Top5、成本 |
| **7.4 告警** | 有静默告警 | 补 SLO 超 2 倍告警 |
| **8.1 索引** | 有部分索引；无 `collected_at` 索引 | 新字段后补 |
| **8.2 数据量** | 2 万级事件 | 暂不分区；>100 万再按月 |
| **8.3 测试** | 有 `stats` 测试基础；无 fake clock 的轮询/游标测试 | 补 |
| **8.4 配置校验** | interval 有上下限 | 补 `max_event_age > interval` 校验 |
| **8.5 文档** | 有 runbook | 补“评论/私信延迟排查” |

---

## 2. 硬约束（与 B 系列相同）

| 约束 | 说明 |
|---|---|
| 不换技术栈 | Go 1.25+ / PostgreSQL 17 / GORM + pgx / zap / net/http；不引入 Redis/MQ/Milvus/K8s |
| 不改既有契约 | 新增字段/表/接口走迁移与契约评审；旧 `at` 保留为平台时间，不改语义 |
| 迁移只加不减 | 迁移编号接在 `0024` 后；已应用迁移不原地改写 |
| 隐私 | 延迟统计只查事件元数据/时间，不把消息正文写进新表；trace 只记 ID/长度/耗时 |
| 测试 | 每个 P0 项有单测；涉及 PG 的用真实 PG 集成测试；`go test -race` 通过 |
| 真实调用 | 不做真实平台压测、不擅自跑付费模型；轮询参数先小步观察 412/429 |

---

## 3. 批次与顺序

| 批次 | 内容 | 预计 | 依赖 |
|---|---|---|---|
| **L0 测量口径** | 3.1–3.7：`collected_at`、`event_timings`、5 段延迟、backfill、fresh/all、SLO、文档 | 2–3 天 | 无；先于一切延迟优化 |
| **L1 死信止血** | 4.1–4.7：error_kind 落库、dead 分布、重试策略、DLQ 重放、stale 窗口、per-channel MaxEventAge、告警 | 2–3 天 | L0 的 `event_timings` 可并行 |
| **L2 降 P50** | 5.1–5.4：轮询参数小步调整、自适应轮询、增量游标、有界并发 | 3–4 天 | L1 稳定；先单通道观察 |
| **L3 降排队** | 6.1–6.6：通道配额、优先级、快慢双路径、prompt 预算、模型路由、合并请求 | 3–5 天 | L2 数据 |
| **L4 可观测收尾** | 7.1–7.4 + 8.3–8.5：面板、日报、告警、测试、文档 | 2–3 天 | L0–L3；与 B2 trace 合并 |
| **L5 P2 数据质量** | 8.1–8.2、索引与分区评估 | 0.5–1 天 | 新字段上线后 |

**如果只有 1 周**：只做 **L0 + 评论轮询 60→20 秒 + 私信 120→45 秒**，但必须先完成 L0 的口径修复；否则数字不可信。

---

## 4. 详细工作包

### 4.1 L0 测量口径（P0，2–3 天）

#### 4.1.1 `collected_at`
- **迁移链前置发现**：`migrations/0024_manual_memory_source.sql` 已存在，但 `internal/database/migration.go` 的 `migrationSteps()` 只到 23；新增 0025 前必须先确认 0024 是否经由其他脚本应用，避免迁移跳号/checksum 失配。
- 迁移 `migrations/0025_event_collected_at.sql`：
  ```sql
  ALTER TABLE events ADD COLUMN IF NOT EXISTS collected_at TIMESTAMPTZ;
  CREATE INDEX IF NOT EXISTS idx_events_collected_at ON events(collected_at);
  ```
- `internal/event/types.go` 增加 `CollectedAt time.Time`（零值表示历史数据未记录）。
- `internal/database/events.go` 的 `eventRow`/`rowFromEvent`/`event()` 增加字段；`Insert` 时若 `CollectedAt.IsZero()` 用 `time.Now().UTC()` 兜底（老调用方不传也不阻塞），采集器逐步显式传值。
- 采集器（comment/dm/live）写入时设置 `CollectedAt: time.Now().UTC()`；历史补采标 `collect_mode=backfill`。

#### 4.1.2 `event_timings`
- 迁移新建：
  ```sql
  CREATE TABLE event_timings (
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
  ```
- 新增 `store.TimingRepo` 与 `database.TimingRepo`，只做 upsert/按事件查询；不跨层直接 SQL。
- 写入点：collector 落库后（collected_at）、scheduler claim（enqueued_at/claimed_at）、generate 完成（generated_at）、sender 开始（send_started_at）、send_log 成功（sent_at）、verify 完成（verified_at）。
- 历史事件不补造时间；查询时 `COALESCE` 缺失阶段并标注 `unknown`。

#### 4.1.3 5 段延迟
- `internal/database/stats.go` 新增 `LatencyBreakdown`：
  - `collect_ms = collected_at - at`
  - `queue_ms = claimed_at - collected_at`
  - `generate_ms = generated_at - claimed_at`
  - `send_ms = sent_at - send_started_at`
  - `e2e_platform_ms = sent_at - at`
  - `e2e_user_ms = sent_at - collected_at`
- 同时保留旧 `Latency` 口径，避免面板/简历口径突变；新接口加 `?mode=user|platform`。
- 测试：固定时间数据断言每段数值；`collected_at` 为空时返回 `unknown`，不伪造 0。

#### 4.1.4 backfill 标记
- `event.Meta` 增加结构化字段（或独立列）`collect_mode = feed|poll|backfill`，不改 `at`。
- 首轮 24h 扫描、批量补处理、旧评论回复全部标 `backfill=true`；统计时 `WHERE collect_mode <> 'backfill'` 得到 fresh。

#### 4.1.5 fresh/all 与样本不足
- `fishctl status` / `/api/status` 输出：
  - `all`: n/P50/P90/P99/Avg/Max
  - `fresh`: 只含 `collected_at >= now()-5m` 的非 backfill 事件
- `n < 30` 显示「样本不足」，不显示分位；`n=0` 显示 `—`。
- 面板延迟卡同样区分；不要用一个 24h 数字覆盖全部。

#### 4.1.6 SLO 与文档
- `config/fishd.toml` 增加 `[slo]`（live/comment/dm 目标），只用于告警/展示，不阻塞业务。
- `docs/10-observability-cost.md` 增补「平台时间 / 采集时间 / 生成时间 / 发送时间」定义与公式。

#### L0 验收
- [ ] 新事件 `collected_at` 非空；历史事件为空且不阻塞查询
- [ ] `event_timings` 能按 event_id 查到 7 个阶段
- [ ] `fishctl status` 同时输出 all/fresh；n<30 标注样本不足
- [ ] 面板延迟卡区分 fresh/all
- [ ] `docs/10-observability-cost.md` 口径与 SQL 一致
- [ ] `go test -race ./...` 相关包通过

---

### 4.2 L1 死信止血（P0，2–3 天）

#### 4.2.1 error_kind 持久化
- 迁移 `event_errors` 表（或 `events.error_kind`）：
  ```sql
  CREATE TABLE event_errors (
      id BIGSERIAL PRIMARY KEY,
      event_id TEXT NOT NULL REFERENCES events(id),
      stage TEXT NOT NULL,          -- generate/send/verify
      error_kind TEXT NOT NULL,     -- provider/timeout/budget/prompt_too_long/rate_limit/auth/content/internal
      message TEXT NOT NULL DEFAULT '',
      created_at TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  ```
- `cmd/fishd/generate.go` 的 `logWorkFailure` 同步写库；只写分类与安全摘要，不写完整 prompt/凭据。
- `safeErrorKind` 与 `error_kind` 枚举统一；未知错误落 `internal`。

#### 4.2.2 dead 分布
- `internal/database/stats.go` 增加 `DeadStats(channel, stage, error_kind, count)`。
- 面板/日报展示「通道 × 环节 × error_kind」Top 5；先回答 405 条死在哪。

#### 4.2.3 重试/降级策略
- 可重试：`provider`（5xx/网络）、`timeout`、`rate_limit` → 指数退避 + 最大次数（配置）。
- 预算/prompt_too_long：先裁知识、再裁记忆、最后单步降级；仍失败才 dead。
- `auth`/`content`/权限：直接 dead，不重试。
- 不改变现有事件状态机，只补错误分类与重试上限。

#### 4.2.4 DLQ 重放
- `fishctl dlq list/replay`（或现有 `fishctl` 命令扩展）：重放前查 `send_log`/幂等键，避免重复回复。
- 面板可选「重放」按钮，必须先 confirm。
- 重放结果记 `event_errors` 或 audit 日志。

#### 4.2.5 回复窗口
- `[scheduler.channels.comment] max_event_age_seconds`（默认 6h）与 `[scheduler.channels.dm]`（默认 24h）。
- 超窗事件标 `skipped`，原因 `stale`，不进入 LLM；不占用 dead。
- `internal/scheduler/scheduler.go:323-325` 的 live-only 判断改成按通道配置。

#### 4.2.6 告警
- comment dead 率 > 5%/天、dm dead 率 > 2%/天；
- DLQ > 0 且 24h 未处理；
- 队列最老事件 > 10 分钟；
- 告警只走既有日志/终端/面板，不引入新中间件。

#### L1 验收
- [ ] `event_errors` 可按通道×环节×error_kind 聚合
- [ ] 405 条 dead 的 Top 原因能回答
- [ ] 重放有幂等保护
- [ ] comment/dm 超窗事件标 stale，不进 LLM
- [ ] 告警规则落配置/文档

---

### 4.3 L2 降低 P50（P1，3–4 天）

- 参数小步：comment `feed_interval_seconds` 60→30（观察一周）→20；动态 `interval_seconds` 60→30；`discover_interval_seconds` 600→180；dm `interval_seconds` 120→60→45。
- 每次只改一个通道；记录 412/429 率、fresh P50/P90、LLM 成本；不达标回退。
- 自适应轮询：活跃期快、空闲期慢；412/429 指数退避 + ±20% 抖动；每账号/房间独立限速。
- 增量游标：comment `(ctime, rpid)` 持久化到 DB/文件；dm 每会话 `last_msg_id + last_ts`；首启只补 24h 并标 backfill。
- 有界并发：多账号/多目标 4–8 goroutine，每轮 10 秒时间预算。

### 4.4 L3 降低排队（P1，3–5 天）

- 按通道配额：live 6 / comment 4 / dm 2 软配额（初值），或加权公平队列。
- 优先级：live 实时 > comment/dm fresh > backfill；backfill 不挤占 fresh。
- 快慢双路径：短评论/私信（<30 字、无工具需求）走单步 Brain，否则 ReAct。
- Prompt 预算：comment/dm 独立 `max_prompt_tokens`（例如 16k）；记录 tokens/reply、cost/reply。
- 模型路由：per-channel routing；简单评论/私信用更便宜模型，复杂问题走主模型。
- 合并重复请求：同用户 30 秒时间窗合并，会话锁 + 幂等。

### 4.5 L4 可观测收尾（P1，2–3 天）

- 面板：fresh P50/P90/P99 + n、队列深度、最老事件年龄、dead/skip 原因分布、LLM 并发使用率/排队时间。
- Trace：与 **B2** 合并做，一条评论/私信事件 `collect → queue → generate → send → verify`；不要两套 trace。
- 日报：收到/回复/跳过/失败、P50/P90（fresh+all）、dead Top5、成本。
- 告警：4.7 + fresh P90 超 SLO 2 倍。

### 4.6 L5 P2 数据质量（0.5–1 天）

- 索引：`idx_send_log_event_ok_at`、`idx_events_channel_dir_status_at`、`idx_events_collected_at`。
- 数据量：2 万级不分区；>100 万再按月。
- 测试：`stats_test.go` 固定数据 P50/P90；comment fake clock 轮询/水位；dm 增量游标。
- 配置校验：`max_event_age > interval`；interval 上下限。
- 文档：`docs/runbook.md` 加“评论/私信延迟排查”，`docs/10-observability-cost.md` 加口径。

---

## 5. 目标 SLO（建议，先写进配置/文档，不阻塞业务）

| 通道 | 指标 | 目标 |
|---|---|---|
| live | 简单回复 P99 | < 3 秒 |
| live | ReAct P99 | < 8 秒 |
| comment | fresh P90（20 秒轮询后） | < 60 秒 |
| comment | dead 率 | < 1% |
| dm | fresh P90（45 秒轮询后） | < 90 秒 |
| dm | dead 率 | < 1% |
| 所有通道 | 队列最老事件 | < 5 分钟 |
| 所有通道 | 重复发送率 | 0 |

---

## 6. 四周路线图

| 周 | 目标 | 动作 |
|---|---|---|
| 第 1 周 | 指标修对 + 止死信 | L0 + L1；`collected_at`/`event_timings`、error_kind、DLQ 重放、基线 |
| 第 2 周 | 降 P50 | comment 60→20/30、dm 120→45/60；自适应；持久化游标 |
| 第 3 周 | 降排队 | 通道配额、快慢双路径、prompt 预算、模型路由 |
| 第 4 周 | 验收 + 包装 | 面板/trace/告警；复测 fresh P50/P90；写前后对比 |

---

## 7. 与 B 系列的交叉点

| L 项 | 与 B 系列关系 | 处理 |
|---|---|---|
| L4.5 Trace（7.2） | 与 **B2 per-event trace** 同一能力 | 只做一套，L 系列提供 `collect/queue/generate/send/verify` 语义，B2 提供 OTel/面板实现 |
| L0.5 面板 | 与 **B6 README/面板** 的数字口径 | 先以 L0 `fresh/all` 为准，B6 引用同一份数据 |
| L1.1 error_kind | 与 **B5 记忆纠正**无关 | 独立 |
| L3.2 快慢双路径 | 与 **B1 原生 tool calling** 相关 | B1 完成后判断短评论是否单步；不反向改 B1 |
| L5 索引 | 与 **B11 清理**独立 | 先迁移后清理 |

---

## 8. 不做什么

- ❌ 不把评论轮询降到 1 秒（平台限流/封号风险，收益不成正比）；
- ❌ 不只看 `P50(all)`：评论 P90 6.7 小时主要是 backfill，先拆；
- ❌ 不在 n<30 的窗口展示 P50；
- ❌ 不为“低延迟”引入 Redis/MQ/多 Agent；
- ❌ 不回复几小时前的旧评论/旧私信；
- ❌ 不把 dead 事件当延迟样本忽略（P50 只覆盖成功者）；
- ❌ 不在没有 `collected_at` 的情况下宣称“优化了 P50”。

---

## 9. 变更记录

- 2026-11-23：初版，依据主人补充清单编制；与 B 系列计划交叉引用。
