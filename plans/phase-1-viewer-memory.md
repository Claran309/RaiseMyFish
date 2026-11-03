# 观众记忆（PerViewer Memory）计划（对齐 DSH people 档案）

> 背景：用户问「某人发过摸摸你，过一段问『我刚刚摸过你，记得吗』」；DSH 蹲直播版已有 people.mjs 人物档案（170 人）。
> 目标：按观众（uid）存储/召回互动记忆 + 档案注入，让大肥鱼记得「谁做过什么」。
> 归期：**P1**（评论/私信通道同期），不塞进 P0。
> 参考：`~/.dsh/bili-live-export/FEATURES.md`（DSH 三功能说明）。

## 现状缺口

| 环节 | 现状 | 缺口 |
|---|---|---|
| 存储 | events 有 from_id/from_name；memory_records 无观众字段 | 加 from_id/from_name |
| 提炼 | 规则：承诺/主人相关/首次出现 | 加「观众互动」规则 + 档案聚合 |
| 召回 | Scope=通道+会话，无观众维度 | 加 PerViewer 检索 |
| 判断 | P0.5 规则无观众触发 | 加「我/你+互动词」触发 |
| 注入 | 无观众档案 | ContextInput 加 Viewer 档案段 |
| 重建 | extractworker 300s 扫 done（无「只直播时」条件） | 加直播活跃节流 |

## 设计（P1）

### 1. 存储（迁移 + 契约）

**方案 A（对齐 DSH 语义，推荐）**：独立 `viewer_profiles` 表（或 memory_records 加列）：
- `uid TEXT`（主键，昵称会变，uid 不变——DSH 教训）
- `name TEXT`（昵称快照）
- `hits INT`（说过几句）
- `replies INT`（鱼回了几次）
- `last_seen TIMESTAMPTZ`
- `recent_texts JSONB`（最近 15 条他说的话）
- `recent_replies JSONB`（最近 10 条鱼回的）

**方案 B**：`memory_records` 加 `from_id/from_name` 列 + 按 kind 存。

→ **裁决：方案 A**（独立档案表，查询快、字段语义清晰；memory_records 留给提炼记忆）。

### 2. 档案聚合（people 对齐）

- 从 events 按 `from_id` 聚合（in 事件：他说过什么；out 事件：鱼回了什么）
- 只给「说过 ≥2 句」的人建档（DSH 教训：一次路过不建，防文件爆炸）
- 每人只存最近 N 条（15 说/10 回）
- 重建：`extractworker` 加「档案重建」子任务（每小时节流 + 只在直播活跃时跑）

### 3. 召回（Reader）

`PerViewer(ctx, channel, fromID, limit)`：
- 查 `viewer_profiles` 的该 uid 档案
- 返回给 `ContextInput.Memory.Viewer`（新字段）

### 4. 注入（Brain）

`ContextInput` 加 `Viewer string`（档案片段，约 300-400 字）：
```
【对方档案 · 老熟人】
- 说过 121 次 / 鱼回过 124 次 / 最后出现 11-02
## 他说过的话（最近 8 条）
- 16:38 没爱了，好歹认识这么久了…
## 本鱼回过的（最近 4 条）
- 没变，就脾气硬了点
```

### 5. 判断（P0.5 扩展）

`NeedViewerMemory` 规则：文本含「我/你 + 摸/抱/亲/喜欢/记得」类互动词 → true

## 验收（P1）

- 场景测试：A 发「摸摸你」→ 10 分钟后 A 问「我刚刚摸过你，记得吗」→ 回复引用 → 通过
- 跨通道：直播室 A 说过，评论里 A 再来 → 能召回（scope 公开）
- 隐私：私密互动不进公开召回；他人看不到 A 的记忆
- 档案：只给 ≥2 句的人建；uid 主键（改名不丢）；重建每小时节流 + 只直播时跑

## 顺带：fact 采集器

当前 `kind='fact'` 无写入方。P1 补一个「事实采集」：
- 从事件提炼事实（如「A 说自己是学生」「A 爱吃辣」）
- 规则版先做（关键词），语义版留 P2 RAG

## 对接 DSH 三功能

| DSH | 我们 |
|---|---|
| ① people.mjs 人物档案 | 本计划（viewer_profiles 表 + 注入） |
| ② 定时总结（每小时/只直播时） | extractworker 档案重建子任务（节流对齐） |
| ③ 弹幕归档（danmaku-log JSONL） | ✅ events 表已覆盖（全量 in/out 事件），无需另做 |
