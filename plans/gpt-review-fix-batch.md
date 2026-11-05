# GPT 审查修复派发单（2026-11-05）

> 严格依据 `docs/reviews/2026-11-05-project-review-and-improvement-design.md`（764 行）整理。
> 50 个问题 ID（C/I/M/S/O 系列）+ 8 个用户问题（U01-U08）+ 工作包 W00-W08 + 回归矩阵 T01-T26。
> 分级：P0 启用前必须处理 / P1 主要功能失效 / P2 质量性能 / P3 探索。

## 一、P0（启用相关路径前必须处理）

| ID | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| M01 | `events_recent.go` ViewerRecent 仅按 from_id；`viewers.go` 档案重建未过滤 private/DM | 公共回复可经档案/观众历史读到私密来源；聚合和向量也受污染 | 统一收敛所有读取与派生路径的私密过滤（不只 memory.Reader 一道检查）；重建已混入私密来源的派生数据 |
| M02 | `extractor.go` LLM 分支没有先执行 AllowPrivate 短路 | 未授权私密事件符合长文本/@ 等触发时仍会被外部提炼写入 | 隐私边界在任何模型调用之前执行（模型 0 次、写入 0 次） |
| M03 | `worker.go` fact ID 不含隐私/来源会话；`memories.go` 冲突更新保留原范围 | 公共与私密同主体/标题冲突时私密摘要进入公共行 | 私密域隔离；更新校验原行与新行范围一致 |
| M12 | `memories.go` SoulCandidates 无私密范围及未来生效筛选；`soul.go` 转换丢失主体/权限/来源 | 自动 Soul 接线后可把私密/未来事实提升到全局文件（当前未接，接线前风险） | Soul 保留权限与来源；先修权限/主体/来源再接线 |

## 二、P1（主要功能失效）

### 上下文/ReAct
| ID | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| C01 | `react.go` runReact 仅传 System/Question；`agent.go` Input 无上下文快照 | 档案/双方对话/状态/长期设定/场景资料未进入 ReAct | 统一入口：ContextBuilder 生成快照，普通 Brain/ReAct/主动弹幕共用 |
| C04 | `generate.go` 近期记忆由 Decide 关键词控制；GetProfile 错误与未找到未区分 | 基础连续对话不应按「刚才/记得」才注入；失败不可伪装陌生人 | 记忆默认注入（非关键词门控）；错误与未找到分开处理 |
| C05 | `proactive.go` 与被动回复构造不同 ContextInput；ReAct 又只带人格+问题 | 主动发言不知道刚说过什么 | 主动路径共用 ContextBuilder + Visual |
| C06 | `agent.go` degrade 可取 Args/Thought | 工具参数/内部决策文字可被当回复发出 | 仅接受最终正文；降级也过正文校验 |
| C03 | `react.go` reactCacheKey 只含 Scope＋正文 | 同房间跨人复用、纠正后仍复用旧回答 | 先停动态答案缓存（只缓存只读检索） |

### 查询正确性
| ID | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| C02 | `events_recent.go` RecentEvents/RecentReplies：SQL 顺序是 channel,session,dir，参数是 ch,DirIn,session | 房间号与方向交换，正常房间查询为空 | 修参数并补真实 PG 种子数据断言（T01） |
| I01 | `viewer_search.go` 昵称与发言/回复同档匹配，无 UID 精确条件 | 提到某人不等于就是某人；UID 查找不可靠 | 身份精确命中优先级（UID > 昵称 > 别名 > 内容） |
| I02 | 无完整别名/音近消歧链 | 问句剥离不能替代谐音/简称/改名/多候选 | 别名表 + 拼音候选 + 多候选歧义保留（不自动合并） |
| I03 | hybrid/search/viewer 语义候选缺相关性拒绝 | 陌生人名可被无关歌词/梗覆盖 | 最低相关性、候选分差、来源有效性、允许 0 结果（found/ambiguous/not_found/unavailable） |

### 记忆
| ID | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| M04 | `extractor.go` parseLLMRecords 不填 FromID/FromName；archive=false 退规则 | 主体丢失；模型明确否决仍被写入 | 主体由可信事件/实体解析提供；archive=false 是明确拒绝 |
| M05 | `donesource.go` 只扫 done，用事件时间水位 | 未回复的有价值入向消息不提炼；晚完成事件被排除 | extraction_jobs 任务表（事件+提炼版本，租约） |
| M06 | `main.go` newMemoryWorker 未提供 Recent/OwnerName/Soul/SoulOpts | 模块宣称支持但生产未接 | 先修主体/时间/权限/候选过滤，再决定接线 |
| M07 | worker 若接 Recent 读取「现在最新」而非历史上界 | 补历史时混入未来 | 离线回放以被处理事件为上界 |
| M08 | 记忆/观众向量更新主要靠 fishctl 手工 | 新事实/档案变化与向量不一致 | 定时增量刷新（与知识一致） |
| M09 | `memelearn.go` 以 Recall 非空认定已收录 | 语义结果让新梗被误判重复 | 精确去重独立于相关性检索（规范词/别名精确覆盖） |

### 消息链路
| ID | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| S03 | 结构化 @ 参数已开始贯通（离线测试通过） | 需真实平台核验（提醒/自指排除/字段语义） | 真实账号/房间受控观察确认 |
| S04 | `serve.go` 仅 live 装 SendRepo；评论/DM 直接 POST | 投递接受后到标 sent 前崩溃窗口，恢复可重发 | 扩展不可变投递计划 + attempt（未知只核验） |
| S05 | `send.go` sendActual/记账限定 live；非账本通道仅标输入状态 | 评论/DM 正常回复无 send_log/out 事实 | 实际正文、平台 ID、输入结算原子记账 |
| S06 | `deliver.go` Resume 的预期 uncertain 已结清仍返回原 error | 可终止调度并取消服务 | 业务失败已结清后返回正常结果；结清失败才升级 |
| S07 | `comment collector` 固定第一页 20 条 | 无楼中楼增量拉取 | 分页追水位、处理旧线程新回复、记录截断 |
| S08 | `dm.go`/`bili/dm.go`/`collector/dm` 最多 20 会话/详情 50 条 | 无完整分页/持久水位 | 账号级会话和消息游标 |
| S09 | `accounts.go`/`collector/dm`/`bili/dm` HTTP 动态切号但 Fetcher.SelfID 固定旧号 | list/detail 可跨账号；旧任务用新号回复 | 固定一次操作的账号快照 + actor 绑定 + 切换屏障 |
| S10 | `sender/dm.go` reserveRecipient/commitRecipient 限制模式 | 不同 peer 并发都可通过 | 按 actor/day 持久预占（不改变默认全收策略） |

### 运维/自愈/依赖
| ID | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| O01 | `serve.go` sideEffectDeps 只取一次 giftGate 值 | 工具箱建立后关礼物仍可能执行 | 注入动态回调，开→关→开跟随门控 |
| O02 | `watchdog/exec.go` runCommand 用 CommandContext.Run 等待常驻进程 | 超时会杀死刚启动的 fishd | 服务管理命令或规范托管 |
| O03 | `main.go` Load 在 applyActiveAccount 前 | DB 活跃账号存在、env 无 cookie 时启动被拒 | 拆结构/凭据校验；先选账号再校验凭据 |
| O04 | `web/server.go` 任意 Host/Origin 及 text/plain JSON 可被接受 | 跨站写接口风险 | 来源/类型/令牌校验 + httptest |
| O14 | CI postgres:17 不声明 pgvector 安装，迁移强依赖 CREATE EXTENSION | 干净 CI 不可用 | 已验证 PG17+vector 镜像或显式安装 |
| O15 | `react.go` 每事件新建工具箱与限频 map | 跨事件 follow/like/dm 限频不共享 | 共享/持久化限频（保持按 actor+动作语义） |

## 三、P2（质量/性能）

| ID | 位置 | 问题 | 修复方向 |
|---|---|---|---|
| C07 | `tools.go` 结果串接统一裁 400 字；formatMemory 不呈现 CrossChannel | 角色/时间/回复链信息丢失 | 结构化工具结果（状态/query_id/范围/as_of/结果项/证据） |
| M10 | `viewer/embed.go` 昵称+发言+鱼回复拼成一个向量 | 鱼说的「白饭」被当成观众特点 | 拆分身份/本人事实/互动事件索引 |
| M11 | `memories.go` Records 按 importance 取前 N | 固定少数主题长期占上下文 | 按问题相关性与主体配额选取 |
| S11 | `bili/dm.go` LastMessage/parseDMContent；fallback | 详情失败时 Content 仍 JSON 包装 | 两条路径统一正文解析 |
| S12 | `queue.go` Claim/scheduler 容量 | 大 live 积压推迟 DM/核验 | 测端到端等待；会话头/aging/通道份额 |
| S13 | `gift.mjs`/`gift.go` POST 后缺 code/null/字符串被当明确拒绝 | 可能已消费却退额度 | 异常协议保守 unknown；可信未消费才退款 |
| O05 | `logview.go` Tail 每秒全量 ReadFile | 无新增也全量分配 | 增量偏移读取 |
| O06 | `framer.go`/`pipeline.go` 部分帧失败返回 nil | 上层无法清理 | 创建者负责失败清理 |
| O07 | `config/web.go` ResolveKey 只用 os.Getenv | dotenv 同名 key 不生效 | 统一解析来源 |
| O08 | `watch/describer.go` 视觉响应无 usage/记账 | 费用总览少算 | 注入用量记录 + unknown |
| O09 | `control.go` watchController 持续失败仍显示 running | 面板状态失真 | 上报最后成功/失败/连续次数 |
| O10 | `account.go`/`accounts.go` Delete 与 Activate 竞态 | 删除活跃账号竞态 | 统一锁/条件更新 |
| O11 | `watchdog` PID 复用只判存活就 Kill | 可能误杀 | 核对进程创建身份 |
| O12 | `webtools/search.go` 跨域重定向带走 X-API-KEY | 密钥泄漏 | 禁止跳转/同源 |
| O13 | Makefile/CI 缺 Dockerfile/compose/deployment | 交付物缺失 | 发布前补齐 |
| O14 | CI pgvector（见 P1） | — | — |

## 四、P3（远期，单独评审）

GPT 第 17 节：房间话题时间轴、关系与互动记忆、跨场连续陪伴、有边界的长期注意力、主动行为价值评估、多模态不确定性。

## 五、工作包与验收（GPT 15 节）

| 包 | 范围 |
|---|---|
| W00 | 固定当前代码/配置版本与观测证据（manifest/案例/可重放样本） |
| W01 | 隐私、提炼授权、跨范围去重与基础查询修复（P0 + C02/I01） |
| W02 | 统一上下文快照与生产接线（C01/C04/C05/C06） |
| W03 | 身份解析、别名与证据检索（I01-I03） |
| W04 | 提炼任务、版本与索引持续更新（M04-M09） |
| W05 | 多通道可靠性（S04-S10） |
| W06 | 运行控制、自愈、资源与费用（O01-O15） |
| W07 | 人格、主动行为与多模态质量 |
| W08 | 完整集成、退出标准、交付和文档同步 |

回归矩阵 T01-T26（真实 PG + 假 Provider，见 GPT 15.2）。

## 六、执行约束（GPT 18 节）

- 指明领取的问题 ID、允许路径、基线；读当前真实源码
- 先补能证明问题的回归，再最小正确修复
- 说明从 repository 到生产装配到模型输入/平台请求的完整数据流
- 关键 SQL/权限/事务/锁/恢复提供真实 PG 证据
- 新观测同步 API/页面/httptest
- 不混入他人改动；DSH 独立验收
- 不从本文件直接领取全部未来任务；按现有流程裁决与更新契约
