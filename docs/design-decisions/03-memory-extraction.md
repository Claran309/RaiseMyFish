# 03 · 记忆提炼

## 问题

同一条鱼要跨通道记住人：观众自述、主人偏好、承诺、关系。原始事件量大且含噪音，不能全塞进 prompt，也不能靠模型自觉「写记忆」。

## 方案

```
done 事件 → extraction_jobs(租约) → 规则/LLM 提炼 → Candidate
         → 写入闸门(价值分类) → memory_records(版本链)
         → 召回(关键词+向量) → reply_meta.memory_ids
```

- 双层：`events` 全量事实 + `memory_records` 提炼层；召回只读提炼层与近期消息。
- 隐私在 SQL 层：`Scope(channel/session/private)` 过滤，不靠 prompt 自觉。
- 版本链：`fact_key` 相同只保留一条 active；改口时旧行 `valid_to/superseded_by/status` 可追溯，并写 `memory_conflicts`。
- 写入质量：偏好需稳定信号（喜欢/习惯/以后…）；只提「主人」、玩笑、复读不写正式表。
- 归因：回复把注入的 `memory_ids` 写进 meta 与 `memory_usage`，可回答「为什么记得」。

## 关键代码路径

- `internal/memory/extract.go`：规则候选（承诺/事实/主人相关/首次出现）。
- `internal/memory/quality.go`：`ClassifyValue` 写入闸门。
- `internal/memory/extractworker/worker.go`：租约、去重、版本键、关系抽取。
- `internal/database/memories.go`：版本链事务与冲突审计。
- `internal/memory/search.go`：默认召回混合检索与逐级回退。

## 失败路径

- 提炼失败：任务重试，超过上限进 dead；水位不推进。
- 私密未授权：fail-closed，不提炼。
- 向量失败：召回回退关键词，不返回空。
- 改口冲突：旧行失效 + 审计，不物理删除。

## 三个可回答问题

1. 为什么不在召回时靠 prompt 过滤隐私？—— 查询层过滤才是硬边界，prompt 只是软约束。
2. 为什么事实要版本化？—— 改口/反悔必须可追溯，且默认召回只能看到当前有效事实。
3. 怎么判断记忆写得好不好？—— `memory_usage` 引用归因 + 写入闸门 + 后续评测集，而不是看条数。
