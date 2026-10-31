# Phase 0.1 基础设施实施计划

依据：主人已派发 0.1，PG17/raisemyfish 已就绪，DSN 仅从本地 .env 读取。本计划限基础设施，不定义后续业务 repository 或恢复状态机，不接入人格草稿。Codex 本会话实施，DSH 独立验收后再集成提交。

## 设计与文件边界

- `internal/config/`：TOML、可选 dotenv、环境覆盖、默认值与校验；显式环境优先于 dotenv；只定义基础设施和后续通道启用配置，0.1 拒绝开启尚未装配的通道。错误不回显配置值。
- `internal/infra/`：zap 日志、PG 生命周期、单实例锁、状态文件；构造注入，Setup/Close，日志按天滚动保留7天。
- `internal/database/`：GORM+pgx 连接、PG17版本检查；显式事务迁移、checksum 版本表和事务 advisory lock。不使用 AutoMigrate。
- `migrations/`：嵌入 0001_init.sql 与 seed.sql；7张业务表严格采用 docs/09 schema，另加 schema_migrations 迁移账本，不抢先添加恢复字段。新增 seed ID 仅为六档来源字典，不包含用户事实。
- `cmd/fishd/`：装配、SIGINT/SIGTERM 取消、PG探活、单实例、状态心跳、优雅释放；migrate up 单独执行，正常运行不隐式改 schema。
- `cmd/fishctl/`：status 检查PG/迁移与服务心跳，未运行返回非零；tail 跟随当日日志并支持取消/跨日切换。无HTTP服务。
- `config/fishd.toml`、README、部署/模型文档：记录默认路径与命令。`.env` 不入仓、DSN不打印。
- CI：移除 checkout 前 job hashFiles，升级并固定兼容的 lint action/配置，启用 PG17 集成测试；本地跑同套检查。

## 测试与步骤

- [x] 初始化 go.mod（module RaiseMyFish，go1.25），添加 TOML/dotenv/zap/GORM/pgx 的实际依赖。
- [x] 编写配置优先级、无凭据泄漏、非法配置测试；记录未实现时编译失败，再完成实现，测试通过。
- [x] 编写真实PG迁移测试：独立空schema、重复迁移、并发迁移、外键/索引/seed、checksum拒绝篡改与事务回滚，测试通过。
- [x] 编写日志双输出/跨日留存、状态原子写入/过期、单实例与取消测试，再完成infra，测试通过。
- [x] 编写fishd取消和fishctl状态/日志测试，再完成入口装配，测试通过。
- [x] Windows go test ./...、go vet ./...、go build ./...、本轮代码lint与真实PG集成通过；Go1.25/Linux全仓race通过。Windows32位GCC不支持amd64race；WSL无法直连Windows localhost PG，组合race+PG交给同环境CI实际验收，未宣称本机组合检查通过。
- [x] 对目标库显式 migrate up 两次、编译后的fishd启动/Windows Ctrl+C、status/tail/第二实例拒绝smoke通过；只打印脱敏结果。
- [x] 教学变更说明、Codex交接报告和可验证快照已生成。下列勾选仅代表Codex实施/自测进度，Phase任务正式验收勾选仍由DSH执行，不提交/push未验收功能。

## 明确补充

迁移账本记录 version/checksum/applied_at；已存在却无账本的同名表视为需要人工核对，不能用 IF NOT EXISTS 掩盖漂移。回滚命令本轮不开放，避免误删事实表；初始版本可在隔离测试schema中验证事务回滚。运行时只需DB凭据；LLM/B站凭据在对应模块接入时才校验。
