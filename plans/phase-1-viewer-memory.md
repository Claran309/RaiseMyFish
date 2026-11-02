# 观众记忆（PerViewer Memory）计划

> 背景：用户问「某人发过摸摸你，过一段问『我刚刚摸过你，记得吗』」——当前只有会话级记忆，没有观众级。
> 目标：按观众（from_id）存储/召回互动记忆，让大肥鱼记得「谁做过什么」。
> 归期：**P1**（评论/私信通道同期），不塞进 P0。

## 现状缺口

| 环节 | 现状 | 缺口 |
|---|---|---|
| 存储 | events 有 from_id/from_name；memory_records 无观众字段 | 加 from_id/from_name |
| 提炼 | 规则：承诺/主人相关/首次出现 | 加「观众互动」规则 |
| 召回 | Scope=通道+会话，无观众维度 | 加 PerViewer 检索 |
| 判断 | P0.5 规则无观众触发 | 加「我/你+互动词」触发 |

## 设计（P1）

### 1. 存储（迁移 + 契约）

`memory_records` 加列：
- `from_id TEXT`（观众 UID，NULL = 会话级/主人）
- `from_name TEXT`（昵称快照）
- 唯一键扩展：`(scope, from_id, kind, title)` 或独立索引

契约 `contracts/memory.md` 补字段说明；`internal/store/memory.go` 的 MemoryRecord 加字段。

### 2. 提炼规则（extractworker）

新增规则「观众互动」：
- 触发词：摸摸/抱抱/亲亲/贴贴/喜欢/爱你/常来/又来了/第一次来 等亲密互动
- 输出：`kind=interaction`（新 kind，或映射 preference）
- 带 from_id/from_name
- 频率控制：同一观众同类互动 1 小时内只记一次（防刷屏）

### 3. 召回（Reader）

新增 `PerViewer(ctx, channel, fromID, limit)`：
- 查该观众的 interaction/fact 记录（最近 N 条）
- 返回给 ContextInput.Memory.Viewer（新字段）

### 4. 判断（P0.5 扩展）

`NeedViewerMemory` 规则：文本含「我/你 + 摸/抱/亲/喜欢/记得」类互动词 → true

### 5. 注入（Brain）

ContextInput.Memory 加 `Viewer []Record`（当前观众的记忆）；brain 拼进 prompt「你记得这位观众：...」

## 验收（P1）

- 场景测试：A 发「摸摸你」→ 10 分钟后 A 问「我刚刚摸过你，记得吗」→ 回复引用 → 通过
- 跨通道：直播室 A 说过，评论里 A 再来 → 能召回（scope 公开）
- 隐私：私密互动不进公开召回；他人看不到 A 的记忆
- 幂等/防刷：同一互动不重复记

## 顺带：fact 采集器

当前 `kind='fact'` 无写入方。P1 补一个「事实采集」：
- 从事件提炼事实（如「A 说自己是学生」「A 爱吃辣」）
- 规则版先做（关键词），语义版留 P2 RAG
