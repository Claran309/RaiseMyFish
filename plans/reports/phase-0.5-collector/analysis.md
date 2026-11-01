# 0.5 直播采集移植交付（待DSH独立验收）

2026-11-01，Codex。基线bb70277；主人授权的0.2/0.3/0.4已在9dd7fd5，余下bb70277协作文档也已push，远端当时与本地一致。本轮0.5新功能未提交/push。

## 范围及结果

移植本机dsh-bili-whale@0.4.5的协议到独立Node 24标准库脚本，保留MIT和源SHA；删除DSH config/文件队列/状态/策略筛选。源实际默认poll、实验WS每25秒轮换；新版本只走单次常驻WS，由Go统一退避和暂停。stdout仅受控JSONL。

Go Collector遵守现有Name/Start/Health：显式自身过滤、规范房间/显式平台ID/平台时间、live/in/public/pending、4000字符截断、并发Health、最小子进程环境、所有边界超时/取消、连续失败上限。RunToBus通过接口幂等Publish，错误显式停采集并保留退出原因链。

新增默认关闭config与fishd装配；开启后只采集到Bus，不启动Scheduler/模型/发送。鉴权/运行时暂停不退出基础设施。check不启动Node/访问B站/写事件、usage或status。CI增加Node24及离线测试；尚未提交的新CI未宣称已在远端执行。

## 验证与问题修复

最终Node17/17；全仓Go unit/integration/vet/build、integration标签lint（0 issues）、WSL race通过。覆盖：collector73.1%、collector/live79.8%、config92.0%、fishd83.1%、database88.7%。PG仅隔离schema；WSL race与Windows真实PG分开验证。WSL既有选项提示保留，随后测试通过，未更改宿主配置。

实际控制台文本归档transcripts/，profile.cover保留覆盖率明细。vet/build成功无输出，退出0记录verification.json，不制造原始空日志。

| 问题 | 证据/修复 |
|---|---|
| 缺模块/接口 | red-node/red-go/red-config，先测试再实现 |
| Node输出1MiB与Go512KiB不一致 | red-line-limit→green-line-limit；600000字正文真实子进程可到4000rune归一化 |
| Node把数组强制转为UID/room/时间 | 内部助手先RED（身份2项、时间1项）再加原始类型守卫；主线程独立复跑最终17项GREEN。原始RED工具输出在内部助手历史，未伪造为本地原始日志 |
| Retryable限流被EOF覆盖成network | red-retry-kind→green-retry-kind，保留typed child分类 |
| Publish失败收尾丢退出错误 | red-shutdown-chain→green-shutdown-chain，Join保留两条原因链 |
| lint收尾 | lint-first/second保留；检查error channel返回值，helper固定os.Executable；可信无shell子进程G204仅行级说明，最终0 issues |

合成Node→Go→Bus→真实PG验证：自身消息不入库、同ID重复两次仅一行、长ID/规范房间未改变、usage0；fishd完整启用与取消路径也通过。另有标记脚本证明check完全未启动子进程，既不采集也不写status。

内部Node实现助手、规格审查、质量审查均不是WorkBuddy/DSH；已明确分路径并停写，主线程自行整合、复跑验证。最终内部复核无必须修复项，不代替DSH验收。

## 未验证与后续

无真实B站/模型调用或发送。未验证真实房间的稳定ID、长连接、平台风控/断线或离线补漏。只支持已列出的DANMU_MSG显式ID，缺ID会安全报错跳过；没有时间/正文哈希兜底。WS不能保证进程离线不漏，不把本轮工程测试当整个P0退出标准。

0.8按主人方向使用danmaku.mjs/POST /msg/send，但发送前意图、未知结果、核验、限流与out记账仍须单独设计/验收。本轮不提前实现。

## DSH交接

请按Collector/Event契约独立验证协议和归一化、自过滤/重复、进程取消/背压、退避上限/鉴权暂停、Health、安全日志、默认关闭与check无进程/无外部调用、真实PG写入失败收尾。不要只转抄开发自测；生产前另取得真实房间样本验证显式ID。

manifest/snapshot冻结本轮源码/依赖与证据，verify.ps1校验；源文件版本/哈希见source-reference.json与tools/bili-whale/ADAPTATION.md。局部.gitattributes保护本轮证据原字节，不修改旧证据。既有Event/Collector/Memory契约、迁移、Scheduler/Brain/人格/判断器未写；config/main增量单独冻结。

教学说明：docs/update/2026-11-01-1430-直播采集子进程与总线接入.md。新功能等待DSH独立回执，工作区不暂存、不提交；本通知仅仓库发布，不声称跨应用会话已送达。
