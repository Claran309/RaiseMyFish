# 给 Codex 的 Phase 0 提示词

> 通用要求和任务正文都必须附带 `plans/coordination.md` 的约束；DSH 负责独立验收和会话辅助，以下任务只按看板派发执行。

> 复制时只复制对应任务那一节（含「通用要求」）。
> 你是本项目（RaiseMyFish）的**首席开发 agent，拥有最终裁决权**。

---

## 通用要求（每次带上）

- 先读：`plans/assignment.md` → `AGENTS.md` → 对应契约 → `plans/phase-0.md`
- 遵守文件所有权表：你负责 `go.mod`、`cmd/`、`internal/{event,store,database,config,infra,scheduler,brain}`
- 不要碰：`internal/collector`、`internal/sender`、`internal/persona`、`personas/`
- 不直接改 `contracts/` 与 `plans/`（需要改就告诉主人，由本鱼执行）
- 技术争议你自己裁决；遇到账号/环境问题告诉主人
- 完成后按 docs/testing.md 自测并写 plans/reports/codex.md；DSH 对快照独立验收后，由 Codex 统一集成提交/push。

---

## 0.1 仓库骨架与基础设施

```
你是 RaiseMyFish 的首席开发 agent。任务：Phase 0.1 仓库骨架与基础设施。

必读：plans/assignment.md、AGENTS.md、contracts/event.md、docs/09-data-model.md、plans/phase-0.md。

范围（只碰这些）：
- go.mod / go.sum（module 名 RaiseMyFish，Go 1.25+）
- cmd/fishd（装配依赖、优雅退出）、cmd/fishctl（至少 status / tail）
- internal/config（TOML + 环境变量覆盖 + 校验）
- internal/database（GORM + pgx 驱动初始化、迁移执行）
- internal/infra（外部连接 Setup/Close）
- internal/store（repository 接口骨架）、internal/event（Event 类型骨架）
- migrations/0001_init.sql

要求：
- 日志用 zap（JSON，落文件 + stderr），按 AGENTS.md 约定
- 迁移建表：events / tasks / memory_sources / memory_records / llm_usage / send_log / dlq
  （字段与索引按 docs/09-data-model.md，别自己加字段）
- 配置校验失败要打印原因并退出
- 不引入 Redis / MQ / Milvus

验收：
- go build ./... 通过
- fishd 能启动并打印有效配置后退出（或常驻）
- 迁移能在空 PG 库执行（如果本机 PG 未就绪，写清如何验证）
- go vet 通过

完成后按交接格式汇报。
```

---

## 0.2 store 层（repository + GORM 实现）

```
你是 RaiseMyFish 的首席开发 agent。任务：Phase 0.2 store 层。

必读：docs/09-data-model.md、contracts/event.md、AGENTS.md。
前置：0.1 已完成。

范围：
- internal/store：repository 接口（EventRepo / TaskRepo / MemoryRepo / UsageRepo）
- internal/database：GORM 实现
- 对应单元测试

要求：
- 幂等插入用 clause.OnConflict{DoNothing: true}
- 状态迁移用条件更新 + RowsAffected 校验
- 复杂查询/锁用 Raw / Exec
- 所有查询带 context
- 禁止把用户输入拼进 Where 字符串

验收：
- 幂等插入测试（同 id 两次）
- 状态迁移测试（合法/非法迁移）
- 集成测试用真实 PG（本机未就绪则用 testcontainers 或说明）

完成后按交接格式汇报。
```

---

## 0.3 事件总线

```
你是 RaiseMyFish 的首席开发 agent。任务：Phase 0.3 事件总线。

必读：contracts/event.md（唯一真源）、docs/03-events.md、AGENTS.md。
前置：0.2 已完成。

范围：
- internal/event：Bus 接口实现（Publish / ClaimPending / Mark / Notify）
- 崩溃恢复逻辑（replying 超时 → queued；sent 只补核验不重发）
- 单元测试

要求：
- Publish 幂等（inserted=false 表示重复）
- ClaimPending 原子 pending→queued，多进程用 FOR UPDATE SKIP LOCKED
- Mark 条件状态迁移，非法迁移返回错误
- Notify 用进程内 channel
- 严格遵守契约状态机

验收：
- 同 id 两次 Publish → 第二次 false
- 并发两个消费者 → 同一事件只被一个拿到
- 非法 Mark → 错误
- 崩溃恢复：replying 超时重置为 queued

完成后按交接格式汇报。**此任务完成后事件契约冻结**，请通知主人。
```

---

## 0.4 调度器

```
你是 RaiseMyFish 的首席开发 agent。任务：Phase 0.4 调度器。

必读：docs/04-scheduler.md、contracts/event.md、AGENTS.md。
前置：0.3 已完成（事件契约冻结）。

范围：
- internal/scheduler：五道闸（ACL / 噪音 / 幂等并发 / 节流 / 优先级）
- 优先队列（container/heap）、冷却、令牌桶（x/time/rate）、会话锁、每日配额
- 单元测试

要求：
- 五道闸顺序与文档一致
- 同 session 并发只产一个 Task
- 节流不丢事件（延后重排）
- 不在调度器里拼 prompt、不发送

验收：
- 每道闸有单测
- 同 session 并发测试
- 令牌桶边界测试（第 N 条通过、N+1 延后）
- 恢复测试（崩溃后 pending/queued 重新调度）

完成后按交接格式汇报。
```

---

## 0.6 Brain（OpenAI 兼容 + Provider 抽象）

```
你是 RaiseMyFish 的首席开发 agent。任务：Phase 0.6 Brain。

必读：contracts/brain.md、docs/05-brain.md、personas/（WorkBuddy 产出）、AGENTS.md。
前置：0.2 验收；0.7a/b 人格素材与加载器、Persona/Memory 最小类型就绪；不等待 0.7c 完整人格回归。测试可用 fixture，实际人格不得以占位包冒充验收。

范围：
- internal/brain：Brain 实现（上下文组装 + 预算截断 + 失败分类）
- internal/brain/provider：Provider 接口 + OpenAI 兼容实现
- llm_usage 记账
- 单元测试 + mock LLM 集成测试

要求：
- 上下文组装：system(persona soul/rules/samples) + user(context/event/instructions)
- 稳定内容在 system，动态内容在 user 末尾（缓存友好）
- 预算截断：先裁记忆、后裁 persona，soul 必须保留
- 超时/5xx 重试 2 次，4xx 不重试
- 每轮记录 provider:model/token/耗时

验收：
- 组装顺序测试
- 预算截断测试
- mock LLM 成功/超时/4xx 三条路径
- 换模型只改配置（llm.routing）

完成后按交接格式汇报。
```
