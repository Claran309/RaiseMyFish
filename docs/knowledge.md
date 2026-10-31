# 15 · 知识库与 RAG

> 2026-10-31 主人要求引入。本文件是**设计摘要**；实施细节以
> [`plans/phase-0.5-implementation.md`](../plans/phase-0.5-implementation.md)（Codex 的 408 行实施计划）为准。
> 与 `docs/06-memory.md`（记忆）、`docs/08-persona.md`（人格）并列。

## 1. 定位：知识 vs 人格 vs 记忆

| 层 | 回答什么 | 存放 | 注入方式 |
|---|---|---|---|
| **人格** | 怎么说（语气/规则/样本/称谓/禁忌） | `personas/<pack>/` 文件 | 全量进 system |
| **知识** | 知道什么（梗/设定/观众/游戏） | 文件（K0）→ 表（K1）→ 向量（K2） | **按需召回**进 user context |
| **记忆** | 记得谁说过什么（偏好/承诺/历史） | events / memory_records | 三因子召回 |

**边界**：知识库不替代人格与记忆；私密知识（观众私信内容）不得复制到公开知识库；情绪词、公开喊主人属于人格规则，不靠知识库解决。

## 2. 知识源

| 知识源 | 内容 | 现状 | 迁移目标 |
|---|---|---|---|
| 梗库 | 约 6800 个唯一词条（6660 内置 + 172 自定义，SQLite） | `meme-ctx.mjs` 包装本地 SQLite | `knowledge/memes.md`（首批 500 条，分批） |
| 设定 | 娇娇、纯爱、两条鱼关系、世界观 | persona-shared 部分 | `knowledge/lore.md` |
| 观众画像 | 常客、梗、禁忌、称呼 | 无 | `knowledge/viewers.md` |
| 游戏知识 | 星露谷等玩法与状态 | 无 | `knowledge/games.md`（可空，P7 前） |
| 平台话术 | B站/QQ 场景口径、禁忌 | persona context | 保留在 persona，不进知识库 |

## 3. 三阶段实现

### K0 · 文件知识库（P0.5，零依赖）

目录：`personas/dafeiyu/knowledge/`（lore.md / memes.md / viewers.md / games.md + `memes/batch-*.md`）。

每个文件一个 YAML front matter，schema `fish-knowledge/v1`：

```yaml
---
schema: fish-knowledge/v1
entries:
  - id: meme-example-001
    title: 原词
    channel: [live, comment, qq_group]
    keywords: [原词, 别名]
    tags: [吐槽]
    priority: 50
    visibility: public        # public / private（private 必须绑来源 channel+session）
    source: meme-source:example
    summary: 原始解释
    usage: 原始接法
---
```

- 召回顺序：过滤通道/权限 → 标题/别名/关键词、标签匹配 → 匹配强度 + priority + ID 稳定排序 → 最多 3 条；无命中返回空，不随机塞梗
- 预算：按主人K0-5最新约束默认10%，可配0~10%，>10%直接拒绝；先保留完整人格/场景/事件，再计算记忆与知识共用剩余预算

### K0-6/7当前交付状态

`fishd`已可按`[knowledge]`装配FileReader，默认关闭；`fishd check`只检查PG与装配信息，Provider/用量写入均为0，`pipeline_connected=false`。尚未接入事件消费或人格评测调用链路，不据Reader加载成功宣称生产知识生效。

新100条离线检索首轮：返回相关46/56=82.14%，正例覆盖43/60（要求≥48，未过），负例误召回0/20，私密越界0，合法Scope镜像20/20，Top3排序及预算检查通过。精确标题/含标题句各20/20，纯场景3/20；发布500条keywords为空，当前词面检索的场景覆盖不足。保留首次冻结和失败，不改题追分；开发侧相关标注仍待DSH独立抽查。开/关各35条真实模型对照尚未授权、未执行。
- 加载失败不能伪装零命中；知识 Scope 与 Task/Event 不一致时 Brain 返回 input 错误、Provider 零调用

### K1 · 结构化知识表（P2）

- 表：`knowledge_entities`（人/梗/游戏/事件）、`knowledge_facts`、`knowledge_links`
- 查询：按标签/实体/关键词，不依赖向量
- 与记忆共用 `memory_sources` 权重模型

### K2 · RAG（P2/P3，默认关闭）

- embedding（模型可配）+ pgvector；混合检索 + 可选 rerank
- 知识源管理：切分、版本、去重、过期
- 达不到质量门槛就保持关闭

## 4. 与 Brain 契约的关系

- **正式契约**：[`contracts/knowledge.md`](../contracts/knowledge.md)（基线 P05-KNOWLEDGE-1，已由 DSH 同步；依据 `plans/phase-0.5-contract-decision.md`）
- 显式新增 `ContextInput.Knowledge *knowledge.Context`，与 `Memory` 并列；不扩展 `memory.Context`、不把知识伪装成记忆
- `nil` 表示未启用；启用且零命中返回带正确 Scope 与 Revision 的空 Context
- 预算、审计、必测清单以正式契约为准

## 5. 验收（K0）

- 冻结至少 100 条检索评测：命中、未命中、权限反例
- 返回条目自相关率 ≥80%；应命中覆盖率同时检查
- 跨私密会话泄漏必须为 0
- 预算、稳定排序、迁移幂等验证
- 假模型调用到真实 PG 用量账本的组合路径
- 人格优化与知识开关对照使用新 run，不覆盖旧证据

## 6. 阶段门禁（摘自实施计划）

| 门禁 | 条件 |
|---|---|
| G0 | 计划落盘（本轮） |
| G1 | 进入 0.7c 对应步骤 + DSH 确认快照 |
| G2 | P0 退出标准通过，或主人明确接受未满足项 |
| G3 | 首批 500 条 + K0 验收通过 |
| G4 | P2 独立放行 |

## 7. 诚实预期

知识库主要补「接梗/兜底/设定/观众」；对「情绪词缺失、公开喊主人」帮助有限。
0.7c 失败标签里接梗+兜底合计只有 3 次，**不能指望 K0 把 57.1% 补到 90%**；人格规则与场景装配才是主战场。

## 8. 非目标

- ❌ 通用知识图谱/百科
- ❌ 实时联网检索（需要时走 MCP）
- ❌ 用知识库替代记忆系统
- ❌ 为知识库引入独立向量数据库（pgvector 足够）
