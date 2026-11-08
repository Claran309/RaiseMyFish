# 测试环境问题修复派发单（2026-11-09）

> 来源：O14（CI pgvector）验证时，白酱在 Docker 环境下对完整集成套件测试发现的 3 处失败。均已定位为**既有测试的时序/负载敏感问题**（与 O14 镜像、迁移 0006 无关）。
> 处理：单独立项修复，不改变已验收的代码语义。

## T-01：TestSchedulerRunRealPostgresWithoutModel 时序敏感

**现象**：测试把 `RetryDelay` 设为 5ms，`deferClaim` 用 Go 时钟 `now+5ms`，而库层要求该时间在事务内严格大于 `clock_timestamp()`。Docker Desktop 下事务往返已超过 5ms（直接复现 future=false，对照组 +1s 成功）；本机原生 PG 上同一测试 PASS。

**根因**：测试的「未来时间」由 Go 进程时钟派生，但校验用数据库时钟。容器/远程 PG 的时钟漂移与网络往返让 5ms 余额被吃掉。

**修法**（二选一，建议 a）：
- a) 测试的 RetryDelay 从数据库时钟派生（`SELECT clock_timestamp()` 取当前时间再加偏移），让「未来」相对 DB 时钟成立
- b) 测试的 RetryDelay 从 5ms 调大到 1-2 秒（牺牲测试速度换稳定）

**验收**：Docker PG 与本机原生 PG 上同一测试都 PASS（`go test -tags=integration ./internal/database/ -run TestSchedulerRunRealPostgresWithoutModel`）。

**禁止**：不改调度/存储语义（只改测试的延迟派生方式）。

## T-02：TestKnowledgeCheckWithRealPostgresNoModelOrUsage 上下文耗尽

**现象**：全套并行满载时，20s 上下文在解析 `memes/batch-007.md` 时耗尽。

**根因**：满载下 knowledge 解析耗时超过测试给的 20s 预算。

**修法**：
- 测试的 context 超时从 20s 调大（如 60s），或
- 解析结果缓存（test fixture 只解析一次），或
- 测试串行化关键段

**验收**：满载（`-p 4` 并行全仓）下测试稳定 PASS。

**禁止**：不改 knowledge 解析逻辑（只改测试预算/夹具）。

## T-03：internal/knowledge 包超 10m 默认超时

**现象**：全套并行满载下，`go test ./...` 在 internal/knowledge 包超过 Go 默认 10m 超时；该包不依赖 PG。

**根因**：knowledge 包测试（含真实文件解析、323 个测试用例）在满载并行下耗时 >10m（实测平时 200-330s，满载翻倍以上）。

**修法**：
- 该包测试加 `-timeout 20m`（Makefile/CI 的测试命令），或
- 优化慢测试（拆分大 fixture/减少重复解析）

**验收**：满载并行全仓测试能跑完。

**禁止**：不改 knowledge 业务逻辑。

## 执行规则

- 三个都是测试环境问题，**不改业务语义**
- 每项交付后 DSH 验收（Docker + 本机双环境）
- 不执行 git
