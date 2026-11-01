# 0.5 直播采集移植与总线接入

2026-11-01，主人确认0.2/0.3/0.4 DSH通过并授权提交/push；这些功能已在9dd7fd5，余下协作文档bb70277也已推送。本轮仅0.5，0.8采用danmaku.mjs/POST /msg/send的方向记录在此，不提前实现或真实发送。

## 范围与责任

Codex负责协议核查、稳定ID、生命周期/凭据边界和装配，新增 `tools/bili-whale/`、`internal/collector/`；必要新增/修改config与fishd的live采集开关/无调用诊断、.env.example、CI的Node测试；本派发/报告/教学说明/自己的报告。现有Collector/Event接口不改，不改迁移、Scheduler/Brain/人格/判断器或旧证据。WorkBuddy无本轮并行写入窗口，DSH独立验收。默认开关关闭；不跑真实模型、真实B站监听或发送。

## 移植裁决

- 源为本机dsh-bili-whale@0.4.5的danmaku-await.mjs（MIT，保留许可证及源文件哈希）。保留WBI/包头/压缩解包/WS认证与心跳思路；删除DSH config、主人/昵称/模板筛选、文件队列、旧状态及正文日志。
- 源实际默认poll，WS每25秒轮换；新适配明确只走WS，持续到断开/取消/超时，重连统一交Go指数退避。不会把源现状描述成已经验证稳定的WS常驻。
- 稳定ID只接收显式平台id_str/dmid字段，不把info[0][5] nonce、时间或内容哈希当ID。大整数原字面保留；无稳定ID或平台时间明确报解析问题并跳过，不造ID。room使用服务器解析的规范房间ID；配置短号可解析，但事件session/key始终规范房间。
- 只做自消息过滤；是否回复由Scheduler负责。Go归一化为live/in/public/pending，键live:{canonical_room}:{msg_id}，正文限4000字符并在安全Meta记录截断。
- Node stdout为有版本的JSONL（ready/event/heartbeat/error），无cookie/token/原始异常/完整平台payload。HTTP固定B站HTTPS地址，WS限定平台chat.bilibili.com节点；所有HTTP/握手/存活检测有超时，压缩和缓冲有上限。
- 凭据仅来自显式FISH_BILI_COOKIE或_FILE（Cookie文本/既有credentials JSON）；不自动读DSH/主目录，不传命令行、不记录日志。子进程只获所需Cookie/文件与最小运行环境，不继承DB/LLM凭据。nav验证登录UID与配置self_id相同才接收；鉴权失败暂停。
- Go Collector严格遵守现有Start/Health；同一事件重复产出由Bus去重。网络短断按指数退避，连续失败有上限，稳定连接后才重置；健康错误安全展示。取消终止并等待单个Node子进程，无shell/孙进程。
- collector.RunToBus只通过接口Publish；发布失败显式停采集并上报，不把落库失败当成功。fishd只装配采集→Bus，不启动模型或发送；默认关闭，check不启动子进程/访问B站。

## 已知限制

WS本身不能补回进程离线期间所有消息；源history有不可靠兜底ID，故本轮不搬假补漏。不声称已满足整个P0两小时/离线不漏门槛。生产启用前仍需真实房间验证稳定ID字段、长连接与断线行为，0.11另验；本轮用合成协议/本机子进程/真实PG验证工程路径。

## 执行

1. 先写Node协议/WS mock及Go正常/解析/自身/退避/取消/Health/Bus失败测试并运行RED。
2. 实现独立Node适配、Go子进程Collector和Bus桥接；再补默认关闭config/fishd装配与check诊断。
3. Node unit、全仓Go unit/integration/vet/build/lint/WSL race；PG隔离schema，0模型/平台调用；内部只读复核与修复。
4. 冻结源码/源哈希/RED/GREEN及教学说明，交DSH独立验收；本轮新功能不提前提交/push。

- [x] 已验收积压已同步远端
- [x] 范围与移植选择
- [x] RED测试 — Node/Go缺模块；跨语言输出上限、可重试分类、发布收尾错误链及Node畸形标量均有失败验证。
- [x] 实现/PG接入 — 真实Node合成流到PG去重、fishd启用采集/取消及无调用check通过。
- [x] 最终工程验证与交接 — Node17项、Go全仓unit/integration/vet/build/lint/WSL race通过，报告/快照/教学说明已归档。
- [ ] DSH独立验收
