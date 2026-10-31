# 0.4 调度器实现与验收范围

2026-11-01，主人确认0.3 DSH通过并放行0.4；按§5.5自主推进。使用TDD与实现计划技能执行，现有P0-EVENT-2接口、错误与Scope语义不改；新调度模块与其仓储适配在本任务内定义。

## 允许路径

Codex独写 `internal/scheduler/`、新增 `internal/store/scheduler.go`、`internal/database/scheduler.go` / `scheduler_integration_test.go`、go.mod/go.sum（仅授权的x/time/rate）、本派发/本轮报告/教学说明/自己的codex报告。无迁移、无cmd/config生产接入、无既有契约/ADR/人格/知识/判断器/旧证据修改；push继续搁置。0.5/0.8不在本轮。

## 实现选择

- 单Run循环串行管理heap、活动任务、会话锁、令牌、冷却和去重窗口；并发仅下游工作回调。独立Generate/Verify回调防止补偿误进生成；本模块不拼prompt、不发送。
- schedule依次过ACL、噪音、事件/会话并发、节流、优先级；generate/verify不重跑首次噪音/扣初始额度，但仍验证结构/访问范围及已入库会话顺序。恢复生成读取已有Task；verify不要求伪造新Task。
- 优先级仅来自配置的可信Owner ID/鱼名称匹配，正文自称主人不能拿Owner优先级。按Priority、At、ID排序；同一Channel/Session先处理已入库较早未结束事件，避免优先级颠倒会话顺序。
- 冷却用注入Clock.AfterFunc（生产time.AfterFunc），基于下游明确的Replied回执设置；会话锁一直持有到回调返回。所有等待Claim和执行Claim都续租；租约丢失取消回调，但回调返回前不解锁，防止不合作回调与新任务重叠。
- 结清快照含租约代次：正常V+1释放不误当丢租；外代queued/replying不判作本代未结清。晚到正投递回执仅保守设置本地冷却，不允许改写新持有者PG状态。启动每个阶段回调前都再次Renew围栏。
- 每通道x/time/rate限制**新任务准入速率**，失败准入退还预留令牌。每日配额为PG tasks按CreatedAt的新增任务计数；事务内按通道/日锁、查配额、建Task/切阶段，重启不重置。不把它宣称为实际发送数/实际发送间隔；0.8须单独完成发送限流与本额度的衔接。恢复已有Task不再扣初始额度。
- 配额日界按可配置Location（默认UTC）用日历AddDate；不以固定24小时处理夏令时。无新增配额表；冷却/短时内容去重/令牌状态为P0单进程内存态，生产完整持久化发送冷却留0.8/0.11。
- Run按Notify及有界轮询Claim/Recover，队列有容量上限；按最近租约期限安排续租唤醒。外部仓储操作与回调均有Context期限。回调必须遵守取消，并用Bus完成终态或持久化retry后返回；未结清、panic或未分类错误使Run失败，不自动无限重试/重复调用模型。

## 测试与执行顺序

1. 先写并运行RED：五道闸通过/拦截、heap排序、重复与会话忙、冷却/令牌/日界、Stage路由、等待及运行续租、取消退出、任务配额PG竞争/顺序/回滚。
2. 实现纯规则/heap/注入Clock，再调度循环与PG仓储；真实PG只用随机schema，无模型/平台调用。
3. 全仓unit/integration、vet/build/lint/race；内部只读审查，修复后冻结哈希/快照及RED/GREEN证据，写教学说明。
4. DSH独立验收后更新看板；不代填验收、不提前开启业务通道或声明全Phase完成。

## 状态

- [x] 范围与实现选择已发布
- [x] 失败测试 — 缺接口编译失败、结清/冷却/查询后派发及外代回执均有实际RED证据。
- [x] 实现及真实PG — 配额竞争/重启、顺序/围栏、准入回滚、真实总线Runner组合通过。
- [x] 工程验证与交接 — 全仓unit/integration/vet/build/lint/WSL race通过，教学说明与快照已归档。
- [ ] DSH独立验收
