# 0.4 调度器交付（待DSH独立验收）

2026-11-01，Codex。主人确认0.3独立验收通过并放行0.4，按§5.5自主实现。本轮范围及设计先发布于 `plans/phase-0.4-scheduler-dispatch.md`，现有Event/Memory契约、Scope映射及旧接口不改；新增模块入口与仓储接口为本次交付基线。

## 已实现

五道闸：结构/公开私密ACL，self/bot/广告/长度/纯符号/重复噪音，事件去重与会话锁，冷却/按通道令牌/PG每日准入额度，container/heap优先级。可信Owner ID与正文冒认区分；按优先级/At/ID排序，同时用PG已有较早未结束事件保护同会话顺序。

schedule首次过滤与延期；generate读取已有Task，不重复初始噪音/额度；verify独立回调且不创建Task。等待/执行Claim都续租，所有分支在仓储读取后、启动下游前再次Renew围栏；丢租先取消，回调返回前保持会话锁。回调必须合作取消并先经Bus结清，未分类错误、panic或本代未结清使Run失败，不自动无限调用下游。

每日额度是按CreatedAt计的新Task准入，不是实际发送账本；事务锁覆盖查额、写Task和阶段切换，重建实例不清零。令牌约束准入速率，失败准入退款；内存冷却/令牌/内容去重不宣称跨重启持久化，实际发送限流/衔接仍留0.8。默认Location UTC且日历切日，夏令时25小时日已测。

## 测试先行与问题修复

实际执行输出归档在 `transcripts/`（UTF-8/LF文本，避免既有*.log及logs/忽略规则）：

- red-unit.txt / red-pg.txt：先写单测和PG测试，因调度/仓储接口未实现而编译失败。
- red-settlement.txt：正常结清先于续租/回调返回会漏冷却；到期冷却未清理。通过持有代次快照缓存正常V+1释放、到期清理修复。
- red-late-receipt.txt：retry重领早于旧回调返回时漏冷却。晚正回执仅保守设置本地冷却，绝不更新新持有者PG状态。
- red-dispatch-fence.txt：generate/verify查询期间丢租仍启动回调。所有阶段派发前再次Renew后GREEN。
- red-external-reclaim.txt：另一实例已重领为外代queued，本实例未经add/续租就收到旧回调，会误报ErrUnsettled。先按代次识别外代，本代未结清仍报错；green-external-reclaim.txt及回归通过。
- 内部审查另发现测试fake map裸读并发竞态，已加锁；测试Timer改原子状态。Windows gcc不能做64位race不当作通过，主线程WSL定向及全仓race均实际通过。

内部scheduler_review最终只读复核无剩余必须修复项，相关回归重复运行通过；它不是DSH，未代填验收。补充测试中有直接验证既有行为者，不虚称每条都单独RED。

## 工程验证

全仓unit、真实PG integration、vet/build、带integration标签lint（0 issues）、WSL race均通过。scheduler覆盖84.0%、store98.0%、database含integration88.7%；profile.cover为覆盖率明细。真实PG只操作随机隔离schema，WSL race不含PG integration。WSL既有配置选项提示保留，测试随后通过，未更改宿主配置。

真实PG覆盖：并发每日配额只能一人准入、重建仓储后计数保留、已入库同会话顺序、旧租约拒绝、任务ID冲突回滚、已有Task不重复扣额、总线+Runner三事件组合（生命周期为合成回调，0模型/平台调用）。组合验证同会话后到的高优先级不越过前文、仅三份Task且最终done。

## DSH独立验收

请按派发范围与P0-EVENT-2独立设计用例，不只复制开发自测。重点复核五道闸通过/拦截、Stage分流、优先级与会话顺序、忙/冷却延期、令牌退款、日界和PG配额竞争/重启、等待/活动续租、丢租不提前解锁、结清/重领/回执竞态、合作取消及未结清报错。

manifest/snapshot冻结本轮源码与依赖；verify.ps1校验本轮和0.3旧快照/未修改源码。旧证据不覆盖。既有契约与迁移、Brain/人格/知识/判断器源码未写，go.mod/sum仅增加授权的x/time v0.12.0。

本轮证据目录单独的.gitattributes禁止Git换行转换，保护JSON/脚本/输出的原字节哈希；不修改根属性或旧证据目录。

DSH请在自己的报告写回执并同步看板；本轮无cmd/config生产接入、无额外模型调用/平台写入、未提交/push，保留其他共享工作区改动。0.5/0.8继续等待具体派发，本通知只发布仓库，不宣称跨应用会话已送达。

教学说明：`docs/update/2026-11-01-1230-五道闸调度与持久化准入.md`。
