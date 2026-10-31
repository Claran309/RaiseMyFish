# 0.2 存储交付（待 DSH 独立验收）

Codex，2026-11-01。基线提交 e72fef9；共享工作区已有大量历史未提交交付，本轮只新增派发单所列 9 个 Go 文件与新报告/教学说明，不暂存、不提交、不 push。

## 范围及事实

- EventRepo：幂等插入、精确读取、按现行契约执行条件状态迁移。20 并发插入唯一成功；20 并发 CAS 唯一成功。
- TaskRepo：事务内锁事件，必须是范围相同的 queued 入事件；12 并发创建唯一任务。错误会话、未 queued、不存在事件、跨事件任务 ID 冲突均拒绝；失败不留下任务。
- MemoryRepo：幂等写记录；SQL 过滤 Recent/CrossChannel/Records，包含公开/私密、DM/QQ、跨会话、错误降密旧数据、有效性、未来时间及稳定排序。返回最小 DTO，不读 Body/Meta 给模型。
- UsageRepo 沿用。没有新依赖或迁移；未改 Brain、人格、知识数据、评分器、旧 run。无真实模型调用，无平台读写，默认业务通道不启用。
- 本轮不实现完整 Reader、提炼/版本化、总线领取/恢复、调度或发送。P0-G01/G02 留 0.3 前正式裁决；P0-G03 留发送前。本次只是存储基线 P0-STORE-1，不冻结上述缺口。

## 测试先行证据摘要

以下是当次工具输出的摘要，并非伪装为完整原始日志：

1. 新增测试后，`go test ./internal/store` 与定向 integration 首次失败，报 ValidateEvent、ErrInvalid、NewEventRepo 等接口未定义。
2. 补签名与空实现后，store 测试真实行为失败：无效事件未拒绝、pending→pending 被接受、无效任务被接受。
3. 真实 PG 连接后，MemoryRepo 空实现被 TestMemoryRepositorySQLIsolation 检出“记忆写入失败 <nil>”。
4. 实现 SQL 后三个核心仓储测试通过；消除 GORM slice 映射警告后重新通过。
5. 追加 CAS 并发、QQ 范围/稳定排序、无效/取消输入与关闭连接回归，全部通过。追加的这些是覆盖既有实现的验证测试，不宣称每条都单独经历 RED。

## 工程验证

unit.log、integration.log、vet.log、build.log、lint.log、lint-integration.log、race.log 为执行输出；空 vet/build 日志表示命令无输出且退出 0，结果同时记入 verification.json。

全仓 unit、真实 PG integration、vet/build、普通与 integration 标签 lint、WSL race 通过。coverage.log/out：store 98.0%，database 91.5%。WSL 启动输出包含宿主 .wslconfig 选项提示，随后所有测试通过、退出 0；没有更改宿主配置。race 不含 PG integration，真实 PG 另在 Windows 执行。

补跑 integration 标签 lint 时，首次发现 8 条 SA1012，全部来自刻意传 nil Context 的边界测试。已在相应测试行用带原因的局部指令说明测试意图，不关闭生产检查；首次输出保留为 lint-integration-first.log，复跑为 0 issues。

内部 storage_review 只读审查无 Critical/Important；唯一 Minor 建议为关闭连接测试，已落实。内部审查不是 DSH，不写其回执。

## DSH 独立验收建议

不要只重放开发自测。请从契约独立构造不同数据验证：重复 ID 不覆盖、并发迁移/任务唯一、任务事务失败无半成品、公开和陌生私密会话不能读出其他私密消息/摘要、失效行过滤、查询上限和安全错误展示。测试仅操作隔离 schema，不启用真实通道或模型。

文件哈希和冻结源码见 manifest.json / snapshot/。DSH 完成后在自己的 reports/dsh.md 写回执并更新看板；可将 Go 存储接口签名同步到契约索引。0.3 前先处理 Claim 后过滤和持有权恢复，不直接照旧调度示例实现。

WorkBuddy 暂不开展 0.5/0.8；待 0.3 接口及平台消息 ID/发送核验协议明确后再派轻量适配任务。通知仅发布于仓库，不宣称跨应用会话已送达。

教学说明：`docs/update/2026-11-01-1057-事件任务与记忆存储基础.md`。
