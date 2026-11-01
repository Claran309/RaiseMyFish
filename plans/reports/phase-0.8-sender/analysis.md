# 0.8 P0-SEND-1交付（待DSH独立验收）

2026-11-01，Codex。主人已明确批准契约提案并要求继续完整0.8；现已实现Node薄适配、Go Service/驱动、PG发送协议、deliver分流和失败out查询过滤。

## 行为与范围

Sender公开入口带Claim与Reply.Scope；只接受live/in/public和1..40字固定回复。先持久化意图，再只读Node预检，PG原子arm后才能commit一次POST。原许可Owner/Version绑定POST结算；unknown只核验、已知ID不能换，最大三次物理尝试和三次核验。未知耗尽保留人工状态，不伪造明确未送达。

实际发送默认关闭，无生产模型/发送worker装配。令牌桶仅Sender物理投递扣取、PG跨重启保护间隔与同文防重；verify不扣。审计唯一send_log/out，结果与输入状态/lease释放同事务。failed/unknown/skipped out不进记忆，memory不重复写。

新迁移0003，保留旧迁移；公共业务库未升级。真实房间、真实接口响应/限流和0.11运行门槛未验证，不宣称平台exactly-once。

## 实际测试与修复

transcripts/为可提交的原控制台文本归档；本地*.log保留但受既有ignore影响。source/provenance记录及profile.cover另列manifest。空vet/build表示工具退出0无输出，结果记verification.json。

- Node最初缺模块RED；质量审查后，verify CLI预检失败误报not_posted/丢attempt_id以三条反例RED→修复unknown。父线程最终增加HTTP401/403分类RED→GREEN，投递后仍unknown。
- Go缺接口RED；Scope/证据来源校验、未知不重试、发送令牌退款和已知MsgID绑定均有验证。驱动EventID用token误拒真实冒号ID，实际RED→修复；真实Node测试放integration tag，缺Node失败不skip。
- PG助手先编译RED，后以stub在真实schema观察Prepare失败，再实现。实际RED捕获零ID、失败核验抹ID、跨原许可伪Post/换ID、known ID nonce证据、manual防重遗漏，均先修后GREEN；部分原始RED在内部助手工具历史，不伪造为本地原始日志。
- Service/驱动丢原因链、普通启动错误误报取消均由父线程RED复现修复。lint首轮5项收尾：switch、校验测试临时路径并局部说明、内部nodeObject去无用返回值；最终0 issues。
- 内部实现、规格/质量复核均不是DSH/WorkBuddy；角色分路径明确，父线程最终整合，所有内部必须项已关闭。

## 最终验证

Node35/35；Go全仓unit/integration/vet/build/lint/WSL race全部通过。sender97.2%、sender/live77.0%、scheduler83.9%、database84.8%。Windows真实PG与WSL普通race双轨；默认Windows gcc不支持64位不当作通过。WSL既有配置提示保留，测试退出0，无宿主配置修改。

组合覆盖成功回执、无ID后只读核验、持续unknown三次人工、默认关闭。真实移植入口在测试子进程中注入fake fetch，与Go驱动/Service/真实PG完成单POST/强证据/原子记账。无真实B站或项目模型调用，llm_usage仍0。

## DSH交接重点

请按三个已更新契约独立设计数据和故障，不仅重跑自测。重点：私密/错Scope无公开审计；不可变回复；arm之前不POST；原许可绑定；断管/超时/5xx只verify；knownID严格匹配；令牌与PG间隔/同文防重；manual依然潜在投递；send_log/out唯一且回滚；失败out不进记忆；取消回收；v2→v3及旧checksum。

源码/依赖与证据在manifest/snapshot，verify.ps1核对本轮和旧0.5快照。旧证据不覆盖。人格/知识/v5未动。教学说明：docs/update/2026-11-01-1800-持久化弹幕投递与核验记账.md。

## 并行提交与所有权说明

另一会话产生8d68f00，将进行中代码与WorkBuddy派发单提交，已保留；它不等于本轮最终验收。当前以此报告manifest精确源码为准，部分最终修复仍在工作区；本会话没有暂存/提交/push未验收0.8。

COMM-027已登记当前Codex仍按主人继续0.8，WorkBuddy重复0.8a的文件outbox/另一套stdout与已批契约冲突，须暂停重复制脚本。未改其他会话报告、coordination临时分工与0.9草稿；本轮不提前做0.9。本报告仅仓库通知，不表示外部会话已读或已停写。
