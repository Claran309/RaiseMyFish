# 06 · 记忆系统设计

> 记忆 = 原始事件（事实层）+ 提炼记录（长期层）+ 召回（读取）。
> 参考现有 `~/.dsh/diary/` 的结构，换掉它的机制。

P0供Brain消费的最小只读数据与范围见 [P0-MEMORY-1契约](../contracts/memory.md)。0.6仅可定义DTO/纯校验和注入接缝，Reader真实召回、提炼与查询隔离留0.9；下文完整长期记忆能力不等于已实现。

## 1. 双层存储

| 层 | 表 | 写谁 | 特点 |
|---|---|---|---|
| 事实层 | `memory_events`（即 `events` 表，不重复建） | 发送器/采集器，同步、必写 | 全量、不可变 |
| 长期层 | `memory_records` | 提炼 worker，异步 | 提炼、带重要性与有效期 |

**事实层必须无条件写**——即使回复失败、被跳过，事件本身也是「发生过的事」。
长期层只写「值得记住的」。

## 2. 写入路径

```
路径 A（必写，同步）：
  collector/sender → events 表（status 明确）

路径 B（提炼，异步）：
  worker 扫描新 done 事件 → 价值识别 → INSERT memory_records
```

**价值识别**（规则优先，LLM 兜底）：

| 信号 | 价值 | 处理 |
|---|---|---|
| 主人说的话/偏好/指令 | 高 | 立即提炼，importance=0.9 |
| 承诺/约定（"下次送你"） | 高 | 立即，带 valid_from |
| 首次出现的人/事 | 中 | 批量，importance=0.5 |
| 普通弹幕/闲聊 | 低 | 不写长期层（只留事件） |

**禁止**：靠模型「自觉调用记忆写入」——写入由代码路径触发。

## 3. 记忆记录结构

```go
type MemoryRecord struct {
    ID          string
    Source      string    // 来源（channel 或 worker）
    Kind        string    // talk | preference | promise | fact | summary | note
    Title       string
    Summary     string    // 一句话摘要（召回时优先用）
    Body        string    // 完整内容（召回后展开）
    Importance  float64   // 0~1
    Confidence  float64   // 0~1（memory 高、journal 低）
    Private     bool

    EntityIDs   []string  // 关联实体
    EventIDs    []string  // 来源事件（可追溯）

    ValidFrom   time.Time // 生效时间
    ValidTo     *time.Time // 失效时间（空=当前有效）
    SupersededBy *string  // 被哪条新记录取代
}
```

## 4. 事实版本化（冲突消解）

同一事实变化（余额 40 → 90）：

```
旧记录：余额=40，valid_from=10-28，valid_to=10-31
新记录：余额=90，valid_from=10-31，valid_to=null
```

- 写入时按 `(entity, key)` 找当前有效记录 → 标记 `valid_to=now`
- 查询默认只返回 `valid_to IS NULL`
- 历史仍可回溯（「他之前说过 40」）

**实体**：`memory_entities`（人/频道/游戏/话题），关系放 `memory_relations`。

## 5. 召回（读取）

### 5.1 三因子评分

```
score = source_weight × time_decay(Δt) × importance × (1 + semantic_sim)
```

| 因子 | 说明 |
|---|---|
| source_weight | 来源权重（照抄现有 diary 的 tier 模型：主人 100 / 互动 80 / 人格 60 / …） |
| time_decay | 指数衰减，半衰期可配（默认 7 天）；`importance≥0.9` 不衰减 |
| importance | 记录时写入 |
| semantic_sim | pgvector 相似度（P2 起；P0/P1 用 0） |

### 5.2 预算分配

总预算（默认 3000 token 记忆部分）按层分配：

| 层 | 预算占比 |
|---|---|
| 当前会话最近对话 | 40% |
| 共享状态（跨通道） | 20% |
| 最近 24h 重要记忆 | 25% |
| 长期语义召回 | 15%（P2 启用） |

超出预算按 score 截断，**人设永远优先**，不被记忆挤掉。

### 5.3 隐私过滤（在 SQL 层）

召回时按当前通道 ACL 过滤：

```sql
-- 公开通道：private 记录一条都不返回
WHERE (NOT private) OR ($1 = ANY (private_channels))
```

不依赖 prompt 自觉。

## 6. 归档与遗忘

- 衰减后 score 低于阈值的记录 → `archived=true`，不进召回，可查
- 同话题多次低价值记录 → LLM 合并为一条 summary
- 事实层按天分区归档（保留 90 天后转冷存储）

## 7. 与现有 diary / lanmei-dream 的关系

| diary 现有 | 自研 |
|---|---|
| `memory/<source>/<date>.md` | PG `memory_records` |
| registry 权重 tier | `memory_sources` 表（照抄） |
| `remember/recall` CLI | `fishctl memory` 命令 |
| 文件锁 + CAS | PG 事务 |
| journal 低置信 | `confidence` 字段 + `kind=journal` |
| archive 快照 | 冷存储表/导出 |

**LOD 分级参考（来自 lanmei-dream）**：

| 层级 | 内容 | 对应本设计 |
|---|---|---|
| L0 | 原始对话 | `events` 表 |
| L1 | EpisodeSummary（brief + detailed + facts） | `memory_records`（kind=summary） |
| L2 | TopicCluster（+ pgvector 向量） | `memory_records`（kind=summary，带向量） |

RaiseMyFish 的双表（events + records）已覆盖 L0/L1；
L2 的"主题聚合 + 向量"在 Phase 2 的 2.6 语义召回里实现。

迁移：写一个 importer 扫 `memory/` → 导入 PG，保留 `global-memory/1` 导出格式（人可读）。

## 8. 测试要求

- 写入：高/中/低价值事件分别落到正确路径
- 版本化：同一实体两次写入 → 旧记录失效、新记录有效
- 召回：三因子排序正确；预算截断正确
- 隐私：公开通道查询不到 private 记录
- 恢复：重启后未提炼事件能被 worker 补上

## 9. 防坑清单

- ❌ 依赖模型自觉写记忆
- ❌ 只存提炼不存原始（无法追溯）
- ❌ 记忆无限增长不归档
- ❌ 召回不做预算（把上下文撑爆）
- ❌ 私密记录混进公开召回
