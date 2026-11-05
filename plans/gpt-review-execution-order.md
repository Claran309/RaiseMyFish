# GPT 审查修复执行队列（严格按文档顺序，逐项执行）

> 来源：`docs/reviews/2026-11-05-project-review-and-improvement-design.md`
> 规则：按编号顺序逐项修复，每项交付后 DSH 验收；**不得跳过、不得漏项**。
> 每项：修什么（文档原文）→ 验收要求（文档 T 矩阵）。

## W01：隐私与基础查询（最先，P0）

| 顺序 | ID | 修复内容 | 验收 |
|---|---|---|---|
| 1 | **M01** | ViewerRecent 仅按 from_id；档案重建未过滤 private/DM → 统一私密过滤（所有读取与派生路径：ViewerRecent/档案/人物向量/事件片段/Soul 候选/摘要/搜索缓存/面板导出） | T02（同 UID 公开/私密/跨平台） |
| 2 | **M02** | extractor.go LLM 分支未先执行 AllowPrivate 短路 → 隐私边界在模型调用前执行 | T03（AllowPrivate=false 长私信，LLM 0 次/写入 0） |
| 3 | **M03** | fact ID 不含隐私/来源会话；冲突更新保留原范围 → 私密域隔离、更新校验范围 | T04（公共→私密、私密A→私密B 不跨范围） |
| 4 | **C02** | RecentEvents/RecentReplies SQL 参数顺序错（channel,session,dir vs ch,DirIn,session）→ 修参数 | T01（真实 PG 插入 in/out/done → 精确返回） |

## W02：统一上下文快照（C 系列）

| 顺序 | ID | 修复内容 | 验收 |
|---|---|---|---|
| 5 | **C01** | react.go runReact 只传 System/Question → ContextBuilder 统一快照（普通 Brain/ReAct/主动共用） | T16（单步/ReAct/主动一致） |
| 6 | **C04** | 近期记忆由 Decide 关键词控制；GetProfile 错误与未找到不区分 → 记忆默认注入；错误分开 | 上下文测试 |
| 7 | **C05** | proactive 与被动不同 ContextInput；ReAct 只带人格+问题 → 主动共用快照 + Visual | 主动路径测试 |
| 8 | **C06** | agent.go degrade 可取 Args/Thought → 仅接受最终正文 | T17（坏 JSON/非 final 不发内部内容） |
| 9 | **C03** | reactCacheKey 只含 Scope+正文 → 停动态答案缓存（只缓存只读检索） | T09/T10（跨人/纠正不复用） |

## W03：身份解析与检索（I 系列）

| 顺序 | ID | 修复内容 | 验收 |
|---|---|---|---|
| 10 | **I01** | viewer_search 昵称/发言同档匹配无 UID 精确 → 身份精确优先（UID > 昵称 > 别名 > 内容） | T12（甲是谁/我是谁/你是谁/贞德说过啥） |
| 11 | **I02** | 无完整别名/音近消歧 → 别名表 + 拼音候选 + 多候选歧义（不自动合并） | T11（同音错字/简称/改名/两个同音） |
| 12 | **I03** | 语义候选缺相关性拒绝 → 最低相关性/分差/来源有效性/允许 0 结果 | T13（随机未知人名/无关歌词） |

## W04：记忆提炼与索引（M 系列）

| 顺序 | ID | 修复内容 | 验收 |
|---|---|---|---|
| 13 | **M04** | parseLLMRecords 不填 FromID/FromName；archive=false 退规则 → 主体可信来源；拒绝不写 | T05/T06 |
| 14 | **M05** | donesource 只扫 done + 事件时间水位 → extraction_jobs（事件+版本+租约） | T07（晚完成/skipped/迟到） |
| 15 | **M06** | newMemoryWorker 未提供 Recent/OwnerName/Soul/SoulOpts → 修主体/时间/权限/过滤后接线 | 生产装配测试 |
| 16 | **M07** | worker 接 Recent 读取「现在最新」→ 历史回放以上界 | T08 |
| 17 | **M08** | 记忆/观众向量主要靠 fishctl → 定时增量刷新 | 索引一致性 |
| 18 | **M09** | memelearn 以 Recall 非空认定已收录 → 精确去重独立 | T15（新梗语义相近不重复） |
| 19 | **M10** | viewer embed 拼成单向量（鱼的话被当观众特点） → 拆分身份/本人事实/互动索引 | 检索质量 |
| 20 | **M11** | Records 按 importance 取前 N → 按相关性+主体配额 | 上下文质量 |

## W05：消息链路（S 系列）

| 顺序 | ID | 修复内容 | 验收 |
|---|---|---|---|
| 21 | **S03** | 结构化 @ 已贯通（离线过） → 真实平台核验 | 受控真实观察 |
| 22 | **S04** | 评论/DM 直接 POST，崩溃窗口可重发 → 不可变投递计划+attempt | T19（四阶段崩溃） |
| 23 | **S05** | 评论/DM 无 send_log/out → 原子记账 | out 事实存在 |
| 24 | **S06** | Resume uncertain 已结清仍返回 error → 返回正常结果 | T06（verify uncertain 结清后其他会话仍成功） |
| 25 | **S07** | 评论固定第一页 20 → 分页追水位 | T20 |
| 26 | **S08** | DM 20 会话/50 条无分页 → 账号级游标 | T20 |
| 27 | **S09** | HTTP 动态切号但 SelfID 固定 → actor 绑定+切换屏障 | T18（切号时刻） |
| 28 | **S10** | 限制模式 reserve 竞态 → actor/day 持久预占 | 并发测试 |
| 29 | **S11** | 详情失败 Content 仍 JSON → 统一正文解析 | T20 |
| 30 | **S12** | 优先级仅领取窗口；live 积压推迟 DM → 测端到端等待 | T20/P95 |
| 31 | **S13** | gift POST 后缺/坏 code 当明确拒绝 → 保守 unknown | T26 |

## W06：运维与自愈（O 系列）

| 顺序 | ID | 修复内容 | 验收 |
|---|---|---|---|
| 32 | **O01** | sideEffectDeps 只取一次 giftGate → 动态回调 | 开→关→开 |
| 33 | **O02** | watchdog runCommand 超时杀常驻进程 → 服务管理命令/托管 | T22 |
| 34 | **O03** | 启动先校 cookie 再选 DB 账号 → 拆校验顺序 | T25 |
| 35 | **O04** | 面板任意 Host/Origin + text/plain → 来源/类型/令牌 | T21 |
| 36 | **O14** | CI postgres:17 无 pgvector → 换镜像/显式安装 | 干净 CI |
| 37 | **O15** | 每事件新建 toolbox+限频 map → 共享限频 | 两事件共护栏 |
| 38 | **O05** | logview Tail 每秒全量读 → 增量偏移 | T23 |
| 39 | **O06** | framer 部分帧失败返回 nil → 创建者清理 | T24 |
| 40 | **O07** | ResolveKey 只用 os.Getenv → 统一来源 | T25 |
| 41 | **O08** | 视觉响应无 usage → 记账 | 费用覆盖 |
| 42 | **O09** | watch 持续失败仍 running → 上报失败 | 面板状态 |
| 43 | **O10** | Delete/Activate 竞态 → 锁/条件更新 | 并发 |
| 44 | **O11** | watchdog PID 复用误杀 → 核身份 | T22 |
| 45 | **O12** | search 跨域重定向带 key → 禁跳转 | 双端点确认 |
| 46 | **O13** | 缺 Dockerfile/compose/deployment → 补交付物 | 发布前 |

## W07：质量（C07 等）

| 顺序 | ID | 修复内容 | 验收 |
|---|---|---|---|
| 47 | **C07** | 工具结果串接裁 400 字 → 结构化结果（状态/范围/as_of/证据） | 工具测试 |

## W08：集成与退出

| 顺序 | ID | 内容 |
|---|---|---|
| 48 | **T 矩阵** | T01-T26 全量回归（真实 PG + 假 Provider） |
| 49 | **2h 实测** | 稳定版测试 |
| 50 | **文档同步** | 契约/README/计划更新 |

## P3（远期，不提前实现）

房间话题时间轴、关系与互动记忆、跨场连续陪伴、有边界的长期注意力、主动行为价值评估、多模态不确定性——**单独评审**。

## 执行规则

1. 严格按编号顺序（1→50），每项交付后 DSH 验收
2. 每项先补证明问题的回归，再最小修复
3. 真实 PG 证据 + 假 Provider
4. 不混入他人改动；不跳项、不漏项
