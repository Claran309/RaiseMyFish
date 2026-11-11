# RaiseMyFish 修改后复核与弹幕智能评估

> 日期：2026-11-09（America/Los_Angeles）。  
> 受审基线：`f48f4726eb71c97fa3a1a7f797efabd188cd581b`，不是11月5日旧工作区。  
> 用户已明确：修改后尚未进行真实弹幕测试。  
> 范围：对照[上轮审查](2026-11-05-project-review-and-improvement-design.md)、修复提交和生产装配，复查剩余缺陷、测试覆盖与智能能力。  
> 本轮不修改业务源码、契约、既有ADR、模型参数或生产数据，不调用真实平台/付费模型。此文是新的审查结论，不回写旧报告伪造历史状态。

## 1. 结论

这批修改有实质进展：ReAct已能收到共享资料，最终答案缓存已移除，近期记忆默认读取，历史查询参数与公开过滤已修，提炼与索引开始后台持续更新，评论/私信有不可变投递计划和成功账本，面板安全与运行观测也已补入。

但还不能把“按问题ID提交了修复”视为行为闭环。部分模块单测通过，生产接线、跨阶段恢复、跨账号归属和失败补偿仍有缺口。本轮新增离线探针已经验证：

1. ReAct渲染没有执行普通Brain的预算与输入校验，合成上下文可不经裁剪达到63,437字节；同一资料交普通Brain则裁为16,235字节。
2. DM的未确认结果会被durable强制升级Verified；本机假HTTP返回200空对象，完整bili→DM→durable→假仓储链仍记录accepted/Verified=true/空消息ID。
3. DM持久水位提交回调能早于最后一条Publish成功；这会留下入库失败后重启跳过消息的窗口。
4. 普通观众说“主人是阿猫”，规则提炼会生成0.8重要度条目，并被Soul归为全局owner资料。
5. 直播管理页三种状态均报`TypeError: pill is not a function`。

对“是否足够智能”的回答分两层：**代码已经具备比旧版更好的基础；尚未具备可证明稳定认人、持续接话、可靠纠错与克制主动发言的完整条件。实际效果因未实测而未知，不能给出认人正确率或智能提升百分比。**

不建议继续盲目增加上下文条数或换更强模型作为第一步。先保证当前问题、当前人、已查证据、发送事实和纠正信息真的进入正确链路，再用冻结用例验证效果。

## 2. 本轮证据与状态约定

- **确认**：具体源码和生产调用链可确定，未必已在生产触发。
- **离线复现**：合成输入、假Provider/仓储或localhost httptest观测到问题；不使用真实凭据/平台。
- **条件风险**：明确触发条件成立才发生，未做该系统级故障注入。
- **优化建议**：目标设计或待测体验，不包装成已发生事故。

代码位置以下以受审HEAD为准，后续提交会改变行号。完整证据索引见[本轮manifest](2026-11-09-recheck-evidence.json)。可复跑探针与输出位于[probes目录](2026-11-09-probes/reproduce.py)。探针有两种语义：

- delivery测试断言正确不变量，在本基线预期5项FAIL；这是发现缺陷的证据，不是验收通过。
- memory探针断言“当前存在这个行为”，因此PASS证明缺陷可复现，不证明功能正确。
- live页面探针预期退出1；context探针打印观测值，退出0不等于安全检查通过。

## 3. 上轮修复哪些已经有效

| 上轮项 | 当前复核 | 仍须保留的边界 |
|---|---|---|
| C01/C04 | 已共享ContextBuilder、默认读取近期记忆，档案错误与未找到分开 | 不是完整的焦点对话/冻结时间快照；ReAct验证和预算未统一，见N01/N02 |
| C02 | RecentEvents/RecentReplies参数次序已修，公开与done out过滤存在 | 需继续检查每条新查询的实际返回，不能只测mock |
| C03 | 最终答案缓存已移除 | 跨人旧答案复用机制已消除，不再当当前缺陷 |
| C05/C06 | 主动路径也接Builder，degrade不再直接取thought或工具步args外发 | 主动价值判断与ReAct降级证据保留仍不足 |
| C07 | 工具结果信封保留角色、时间、回复引用，跨分组轮转渲染 | 最终仍受400字预算；包含字段不等于足够表达完整证据 |
| I01/I03 | UID/昵称/内容分档，语义阈值与歧义状态已有实现 | I02别名/拼音生产未装配；陌生UID错误契约不一致，见N04 |
| M01/M02/M03 | 公共历史/档案过滤、LLM私密授权前置、公私事实键与更新范围检查已修 | 历史污染是否已清洗没有生产证据；收件账号隔离仍缺 |
| M04 | LLM结果带可信来源身份，archive=false不再退规则 | 主体与陈述者混同/否定与玩笑仍需质量验证 |
| M05/M06/M07 | extraction_jobs、生产Recent/Soul接线、历史上下文上界已经存在 | 任务租约与失租写入未闭环，Soul可信提升仍有误判 |
| M08/M09/M10/M11 | 自动刷新、精确梗去重、三路人物向量、主体配额/问题相关排序已实现 | CAS、模型指纹、私密外发策略与中文检索仍不足 |
| M12 | Soul公开/非DM/未来生效筛选及来源字段已有实现 | 公开来源不等于可信owner设定，见N05 |
| S03/S04/S05 | 结构化@离线链路、评论DM计划/arm/成功原子账本已实现 | 真实@未测；恢复stage、actor归属、Verified语义仍不闭环 |
| S06/S11/S12/S13 | 已结清预期错误不再终止调度、DM正文解包、公平领取、gift未知分类已有修复 | 队列延迟目标、真实平台、限流和长期负载仍未验证 |
| S07/S08/S09/S10 | 评论/DM分页水位、同轮Fetch账号快照、操作准入、收信人持久预占已有实现 | 分页覆盖有界；落库ACK、首次计划前切号、unknown预占残留见N09—N13 |
| O01/O04/O06/O07/O10/O11/O12/O15 | 动态礼物门控、面板来源/令牌、帧失败清理、dotenv key、账号仓储竞态、PID身份、搜索重定向、进程共享动作限频均有修复 | 不把这些旧问题继续原样报告；账号超时补偿是另一问题 |
| O02/O03/O05/O08/O09/O13/O14 | 自愈、DB账号启动、日志增量、视觉账本、watch健康、Compose、pgvector镜像都有改动 | 具体残留见N14—N21，不等于原缺陷完全没修 |

S01断线补拉、S02存储短暂失败后采集监督恢复，当前仍是已知边界。两份“按序50项”执行文件没有完整覆盖上轮每个问题ID，也没有新实测/退出证据；不能以执行编号达到50判断原50项均已验收。

## 4. 直接影响弹幕智能的剩余问题

### N01 · P1 · ReAct只统一了文本来源，没有统一校验和预算

位置：[ContextText](../../internal/brain/context.go#L282)、[runReact](../../cmd/fishd/react.go#L78)、[普通Brain验证](../../internal/brain/brain.go#L109)。

ContextText调用buildContextSection和contextBody，绕过inputBounds、validateInput与buildPrompt。普通Brain有Scope验证、Viewer/Visual/Soul输入约束和预算裁剪；ReAct没有这些等价门禁。渲染失败还会降级为“仅带问题”，不是明确停止或受控缺失状态。

离线合成结果，见[原始JSON](2026-11-09-probes/context-probe-result.json)：

| 观测 | 结果 |
|---|---|
| 配置预算 | 16,384（当前保守估计口径，不能称模型精确token） |
| ContextText字节数 | 63,437，直接传ReAct |
| 普通Brain实际发送的消息内容字节合计 | 16,235，已裁剪 |
| 原始Viewer伪闭合标签 | ContextText接受；普通Brain拒绝 |
| 合成跨Scope私密Message | ContextText接受；普通Brain拒绝 |

最后一项是**防御层绕过证明，不是已从生产SQL读出私密数据的证明**；现有Reader仍做查询与DTO校验。但两个执行路径安全条件不同，后续任何资料接线遗漏都更容易进入ReAct。普通观众发言中的尖括号经档案文本也可进入未转义资料段；不能把标签分隔当授权机制。

建议：统一准备一个已验证、已裁剪的生成输入，普通与ReAct只选择执行策略；不能只导出一个返回string的渲染器。对合法尖括号使用结构化编码，不用“全部拒绝”替代输入表示。增加同资料/预算/Scope/坏片段在两路径行为等价的测试。

### N02 · P1 · 当前说话者、焦点对话和回复链仍不完整

位置：[Builder](../../cmd/fishd/context.go#L64)、[近期窗口](../../internal/memory/reader.go#L56)、[Message DTO](../../internal/memory/context.go#L24)、[formatViewerProfile](../../cmd/fishd/generate.go#L75)、[ReAct问题](../../internal/reasoning/agent.go#L255)。

当前Recent仍是房间最近20条混合消息；没有按当前发送者获取实时的双方对话轮次。Message DTO不含ReplyTo；档案的RecentTexts和RecentReplies分两个列表，不能证明哪一句对应哪一句，而且档案仍是后台快照。

ContextText不包含当前Event，runReact只另加Question正文。合成空历史/无档案时，当前Event中的UID和昵称都不在模型prompt；SpeakerUID仅在模型选择viewer_search时用于“我”的解析。热闹房间或排队后，当前事件未必仍在最近20条中，模型便缺少确定的当前人标识。

此外线上读取“执行时最新消息”，没有冻结as_of/事件视角；本轮新来的其他人话语可挤掉用户正在追问的原对话。历史提炼的上界已修，但不能据此说在线快照也有时间一致性。

建议：显式注入current_event、speaker、reply_target、subject candidates；保留reply_to并按证据配对焦点轮次。房间流用于现场，双方对话另有受保护配额。记录snapshot时间和来源版本；用户后续纠正进入本轮之前的可见信息如何处理应有明确策略，不能悄悄混入。

验收：A问、B插入30条、A追问；两人同时问“我是谁”；同名/改名；鱼主动发言夹在回复中。模型输入必须仍明确本轮A、A的原问题、鱼对A的回复及引用关系。

### N03 · P1/P2 · 查到的知识和工具证据仍可能在执行切换时丢失

位置：[ContextText注释和实现](../../internal/brain/context.go#L276)、[Builder知识预召回](../../cmd/fishd/context.go#L68)、[Agent结果](../../internal/reasoning/agent.go#L66)、[degrade](../../internal/reasoning/agent.go#L171)、[Generate回落](../../cmd/fishd/generate.go#L179)。

- Builder按意图读取Knowledge，但ContextText明确不包含Knowledge；默认ReAct时这次预召回的内容没有送给模型，需要模型再次主动查工具。离线KNOWLEDGE_SENTINEL确实未进入渲染结果。
- Agent超时/失败时，已查工具observations只存在局部变量；若最后Text为空，调用方回普通Brain，普通输入没有这些新证据。可能“花时间查对了，但最后回落又不知道”。
- ReAct的8秒只覆盖其内部，前置读取和后续普通Brain的40秒预算不是同一剩余deadline；后台用量写入也有独立等待。当前worker 30秒可最终限制部分路径，但不构成合理的用户回复时限设计。

建议：知识要么在统一预算中注入，要么明确由工具取，避免无效重复召回；Result携带可复用证据而非思维文本，回落继续使用已验证观察。整轮预算覆盖排队、读取、embedding、工具与最终生成，为最终正文预留时间。

另有两个质量边界：Question被截为300 rune，长问题尾部的否定/纠正可能丢失；所有“XX是谁”仍被系统硬指向观众。应按问题主体与场景消歧，不因修了一个案例就把“鲁迅是谁”也强制查观众。128输出token还要容纳JSON、工具参数与final，是否够用需记录截断/解析失败率再决定。

### N04 · P1/P2 · 谐音能力部分未接线，陌生UID错误语义不一致

位置：[SearchOptions](../../internal/viewer/search.go#L190)、[生产Searcher构造](../../cmd/fishd/serve.go#L458)、[UID错误处理](../../internal/viewer/search.go#L281)、[仓储GetProfile](../../internal/database/viewers.go#L101)。

Aliases/Phonetic已经有接口和单测，但生产只传Alpha、MinSimilarity和错误回调，没有别名表加载/更新与拼音扩展。现状是规范化、编辑距离等近似可用，不能宣称完整音近/别名识别已生产生效。

GetProfile查无记录返回store.ErrNotFound，而Searcher数字UID路径识别另一个独立的ErrViewerNotFound。新观众精确UID未命中会变工具错误。离线模拟真实错误契约已复现，见[memory探针](2026-11-09-probes/memory-probe-output.txt)。

建议：统一not-found错误契约；先定义别名来源、确认状态与热更新，再真正注入。用同音多人、陌生人、没有共同字的同音昵称和新观众做生产装配测试，不能只给fake返回专用错误。

M11相关性排序已接线，但按空白/标点拆词，“弓长三益是谁”仍是整句词项，且只匹配title/summary。这不等于已按人物UID取出他的事实；主体解析应先于记忆选择。

### N05 · P1 · 普通观众含“主人”的话会晋升为全局owner设定

位置：[提炼规则](../../internal/memory/extract.go#L181)、[Soul主体判断](../../internal/memory/extractworker/soul.go#L125)、[生产Soul接线](../../cmd/fishd/main.go#L410)。

Soul已从休眠代码变为运行能力。在规则路径中，观众文本含“主人”即可得到0.8重要度preference；不触发LLM、模型失败或额度耗尽时可直接生效，LLM正常判断时则可能明确拒绝。本轮短句探针复现的是规则路径。标题/摘要含“主人”或展示昵称等于OwnerName，又被Soul判为owner。

离线观测：UID=900002普通观众说“主人是阿猫”→importance=0.8→subject=owner/public→RenderSoul出现该条全局设定。**公开数据过滤已修，但可信主体提升没有修。** 恶搞、引用、观众自己的偏好都可能继续污染跨会话人格。

建议：分开asserted_by和subject；主人身份用可信UID绑定，普通观众的转述只保留为带来源候选。全局设定提升要求明确策略与证据。现有将“观众提供的主人设定”视为必收正例的测试也需要重新裁决，不应为了保持旧测试绿保留错误语义。

### N06 · P1 · 提炼任务有租约，但批处理没有续租/失租写入围栏

位置：[Claim与串行处理](../../internal/memory/extractworker/worker.go#L378)、[handleJob](../../internal/memory/extractworker/worker.go#L399)、[任务仓储](../../internal/database/extraction_jobs.go#L125)。

默认一次领50条，共用2分钟到期时间，然后串行调用提炼器；Renew虽有实现却未在worker调用。队尾在等待期间就可能到期。processEvent已写记忆后才Complete；Complete的ErrConflict被忽略。

慢模型/积压下会失租后继续写、反复领取、重复付费或生成不同候选。生产owner已是主机＋PID，不能误报成所有实例使用相同owner；仍需要每次领取代数/token来防旧执行提交。

建议：按实际容量小批领取、执行中续租、失租取消；结果提交与完成使用同事务围栏。验收必须包括排队期间到期、第二worker抢占、旧worker返回、断电重启；单测“忽略Complete冲突”不是正确性证明。

### N07 · 高优先级隐私边界缺口 · 自动embedding未区分外发授权与有效性

位置：[MemoryEmbeddingInputs](../../internal/database/memories.go#L399)、[自动刷新](../../cmd/fishd/memory_embedding.go#L124)、[发送输入](../../internal/memory/embed.go#L138)。

后台现在实际启动，输入SQL仍读取全部非空summary，包括private、archived、valid_to已失效的记录；DTO不携带授权范围，自动任务直接送给配置的embedding服务。

触发前提：embedding.enabled与auto_refresh启用，库内相应记录尚无向量或内容hash发生变化；已有向量且hash未变的记录会增量跳过，不是每轮重复外发全部摘要。本轮没有盘点生产私密存量，也没有证明已经发生未授权外发。`memory.allow_private`当前只控制提炼，不能误称它已被定义为所有私密处理的总开关。

建议明确单独的私密embedding授权/提供方/有效记录选择策略，默认只外发获准资料；用捕获输入的假Embedder验证private/失效/归档记录的选择矩阵，授权在构造外部请求前生效。公开查询过滤不解决外部处理授权问题。

### N08 · P1 · 自动刷新缩短了陈旧窗口，没有解决索引一致性

位置：[更新事实](../../internal/database/memories.go#L77)、[向量查询](../../internal/database/memories.go#L363)、[向量写回](../../internal/database/memories.go#L465)、[档案写回](../../internal/database/viewer_embeddings.go#L128)。

summary更新保留旧向量；查询只检查向量非空。后台读取旧档案后，另一路重建/清洗并清空向量，旧embedding计算返回仍按UID覆盖写回。这样会用旧语义给新正文排序，甚至恢复刚清除的污染向量。

建议源版本/hash、dirty状态与索引版本分开；写回CAS校验源未变化，查询拒绝stale。模型/提供方/维度/输入模板也进入指纹，同维度换模型不能复用旧空间。用barrier控制“读旧源→更新/清洗→旧计算返回”证明拒绝写回，不能只测定时器会调用刷新。

## 5. 可靠性残留：会直接影响记忆真实性和账号安全

### N09 · P1 · DM水位早于持久化确认

位置：[DM发送到channel后Commit](../../internal/collector/dm/dm.go#L207)、[桥接收到后才Publish](../../internal/collector/bridge.go#L74)。

无缓冲channel只保证桥接接收，不保证PG落库。最后一条被取走时，Collector可先保存整个batch水位，随后Publish失败；重启按水位跳过未落库消息。

本轮真实Collector.poll＋RunToBus、假Publisher阻塞后失败已复现：Publish未返回时Commit回调已经执行。该证据没有运行生产水位文件或PG，证明的是确认时序错误。修复需持久化ACK，或事件插入与水位更新统一事务；扩大channel缓冲无效。

### N10 · P1 · 采集到首次计划之间仍可跨账号回复

位置：[Batch有actor但toEvent丢弃](../../internal/collector/dm/dm.go#L246)、[首次Send读当前actor](../../internal/sender/durable/durable.go#L124)、[Generate先生成再Send](../../cmd/fishd/generate.go#L165)。

同次Fetch固定凭据和已有计划绑定旧号已经修；但A收到的消息入库后，排队/模型生成期间可切B，首次PreparePlan用B建立计划。事件ID/Session/Memory.Scope仍未包含收件actor，同peer在两个接收账号的私密会话也无法分开。

建议actor从入向事件持久化并沿任务/Scope/计划传播。未arm旧任务明确冻结、取消或按原actor处理，不静默采用当前活跃账号。测试切换时刻必须包含“入库后”和“Brain阻塞中”，不能只从已有计划的Send开头开始。

### N11 · P1 · 评论/DM崩溃恢复仍重跑Brain/ReAct

位置：[PreparePlan保持generate](../../internal/database/delivery_plans.go#L181)、[ArmPlan](../../internal/database/delivery_plans.go#L323)、[租约恢复](../../internal/database/queue.go#L226)。

计划和arm存在，但输入stage未原子切deliver/verify。恢复后先进Generate，重新调用模型，然后durable.Send才看到已有计划。底层armed不重POST保障有效，**不是说原消息一定重复发送**；问题是模型重复消耗、原生成失败会阻挡核验，副作用工具启用时还可能重做关注/礼物/私信动作。

建议计划/阶段同事务推进，恢复直接Resume；真实Scheduler→handler组合测试中断ready/armed/unknown，断言模型及副作用调用数不增加。只直接反复调用durable.Send的测试覆盖不到这层。

### N12 · P1 · 未确认DM被持久化为已确认成功

位置：[classify强制Verified](../../internal/sender/durable/durable.go#L409)、[DM空ID仍OK](../../internal/sender/dm/dm.go#L255)、[Bili响应零值code](../../internal/bili/bili.go#L449)。

durable只要result.OK就写Verified=true，忽略下层Verified=false，允许空MsgID。HTTP200空对象又因code的int零值被当成功。离线完整链观察到：`POST=1, OK=true, Verified=true, MsgID="", recorded=accepted`。

建议区分平台明确接受、已验证和未知，保留下层证据；code字段检查存在性和类型；缺稳定回执按通道契约处理，不伪造done/out已确认事实，也不盲目重投。

验收覆盖空对象、缺data、code=null/字符串、空/零/非法msg_key和正常回执。见[五个失败不变量测试](2026-11-09-probes/delivery-reproduction.txt)。

### N13 · P1（限制模式）· 后续失败能清除先前unknown预占

位置：[retain/releaseRecipient](../../internal/sender/dm/dm.go#L321)、[RecipientStore.Release](../../internal/sender/dm/recipient.go#L185)。

离线序列复现：第一次给A发送unknown，名额保留；第二条给A在互关预检失败，释放该名额；第三条给B被允许发送。unknown没有独立持久保护状态，第二次失败释放了别的尝试留下的占用。

只在AllFollowers=false限制模式触发，不改变用户当前全收策略。预占绑定attempt/token，明确pending/unknown/committed，某次失败只能释放自己的未消费预占。测试加入重启、多进程、同peer多尝试交错。

### N14 · P1（超时路径）· 切号验证失败不能保证回滚

位置：[Activate](../../internal/account/account.go#L268)、[Gate.Switch](../../internal/account/gate.go)。

等待在途＋切换＋Verify共用30秒context。先SetActive新号，Verify耗尽截止时，相同已过期context再用于rollback/UpdateCheck，PG回滚可失败，而Gate defer仍恢复准入。

建议在独占窗口验证成功后再提交活跃标志；若采用补偿，使用独立有界cleanup context，补偿失败维持degraded/安全暂停。测试必须让Verify等到context截止，而非只返回即时错误。

## 6. 运维、页面和交付残留

### N15 · P1 · 直播面板脚本稳定报错

位置：[live.html](../../internal/web/static/live.html#L96)。全局pill()在render前半段被调用，后半段声明`var pill`，变量提升导致整个render内pill为undefined。运行、停止、未装配三例均报`TypeError: pill is not a function`。

局部DOM变量改名并补JS执行/浏览器流程测试。页面“包含某段文字”的断言仍保留，但不能替代真实脚本执行。证据：[页面探针结果](2026-11-09-probes/live-page-probe-result.json)。

### N16 · P1（干净数据库条件）· pgvector镜像修复未覆盖扩展schema隔离

位置：[CI](../../.github/workflows/ci.yml#L69)、[随机schema测试](../../internal/database/migration_integration_test.go#L48)、[迁移0006](../../migrations/0006_knowledge_embeddings.sql#L5)。

镜像现已带扩展文件；但空库首次CREATE EXTENSION未指定稳定schema，search_path首位是随机测试schema。扩展可能落入其中，其他schema找不到vector类型，DROP随机schema CASCADE还可能删除数据库级扩展/其他测试依赖。

本机已有public.vector的PG测试全绿不能覆盖此条件，本轮未创建全新数据库或触发远端CI。建议基础设施一次性在稳定schema初始化扩展，处理并发并确保cleanup不删除共享对象。不要原地修改已应用0006/0007 checksum。干净独立DB反复执行CI原命令作为验收。

### N17 · P1/P2 · DB-only账号启动修复未覆盖watchdog和部分CLI

位置：[fishwatchdog加载](../../cmd/fishwatchdog/main.go#L46)、[fishctl加载](../../cmd/fishctl/main.go#L72)、[配置加载](../../internal/config/config.go#L93)。

fishd已正确分结构/凭据校验；watchdog仍完整Load，status/tail的LoadForCLI仍验证启用的DM/comment/watch/gift凭据。只在DB存账号、环境无cookie时，服务能运行，监控或诊断反而启动失败。

按命令实际能力校验：watchdog不需平台cookie，status/tail不需发送凭据；真实发送命令仍严格校验。用临时配置、假依赖覆盖，不读取真实秘密。

### N18 · P2 · unknown费用仍被真实API压成零

位置：[聚合SQL](../../internal/database/web.go#L78)、[DTO](../../internal/store/web.go#L21)、[首页分支](../../internal/web/static/index.html#L153)。

视觉调用已接账本且保留nil，但COALESCE(sum(cost_usd),0)与float64 DTO永远返回数字。前端`cost_usd==null`不可达；全未知显示0，混合只显示已知部分却称累计费用。

返回known_cost、unknown_calls、total_calls/coverage；真实PG＋API验证空账本、真实零、全未知、混合四组，而非只断言页面包含“未知”。

### N19 · P2 · Docker宿主ASR/搜索说明与端点校验冲突

位置：[部署说明](../13-deployment.md#L188)、[Compose](../../deployment/docker-compose.yml#L79)、[validEndpoint](../../internal/config/llm.go#L68)。

建议使用http://host.docker.internal，但配置只接受HTTPS或字面回环HTTP，会拒该地址；保留127.0.0.1又指向容器本身。host-gateway也不会让宿主仅监听127.0.0.1的服务自动接受桥接访问。

明确容器拓扑与可信端点，用受控代理/HTTPS或窄范围明确授权方案，不泛化放宽所有HTTP。干净Compose连接假ASR/搜索验证配置和实际请求两层。

### N20 · P2 · 日志增量改进后仍缺chunk与未完成行上限

位置：[logview](../../internal/logview/logview.go#L175)。无新增时不再全量读已修，但一次读取全部新增长度，pending可累计无换行大行。轮转后大文件、慢消费追赶仍可大分配。

固定chunk、最大行与pending上限，超限跳过/标记并恢复后续行；测跨天、截断、无换行和慢客户端。不是要求重写整个日志组件。

### N21 · P2 · watchdog把所有超时未退出命令当常驻启动成功

位置：[runCommand](../../internal/watchdog/exec.go#L40)。旧CommandContext杀新fishd问题已修；现在“到观察超时还没退出”也可能是挂死systemctl/启动脚本，却被返回成功。冷却后可累积进程与Wait goroutine。

显式service-command/managed-child模式；短命命令超时失败并回收，常驻模式用新心跳/进程就绪证据确认。父ctx取消不新启动。用假卡死命令和假常驻进程验收，不能只验证新进程没被杀。

## 7. N22 · P2/D · 主动发言仍缺“值得说才说”的机制

位置：[主动RunOnce](../../internal/proactive/proactive.go#L186)、[字面去重](../../internal/proactive/text.go#L8)、[主动装配](../../cmd/fishd/proactive.go#L75)。

共享Builder是进步，但每轮仍主要按gap→截图→生成→字面相似度→发送。没有明确stay_silent、观察新颖性、语义观点重复、用户正在追问/纠正时让路等机制；recent.add还在发送成功前执行。主动装配的记忆和工具依赖也与被动不同：没有同等混合记忆Searcher，viewer_search传nil，不能因共用Builder就宣称能力完全一致。

这是质量设计缺口，不是已证明新版一定继续刷屏。旧版归档多次“这界面有点东西”属于回放材料，不能当新版实测结果。

建议输出comment/ask/react/stay_silent，绑定触发观察ID和时间；无新事实保持安静；语义去重与已确认发言记录分开，失败尝试不算已经说过。主动与被动共享现场状态，但按各自目标选择资料与工具，依赖差异要显式可观测。

## 8. 当前智能能力判断

| 能力 | 当前代码具备的基础 | 尚不能确认的部分 |
|---|---|---|
| 听懂单条弹幕 | 人格、场景、近期资料、ReAct工具已接入 | 长问题尾部截断、主语歧义、工具失败与正常无答案的实际表达 |
| 熟人识别 | UID/昵称优先、候选分档、阈值与歧义状态 | 新UID错误、别名/拼音未接线、真实误认/拒识率 |
| 连续接话 | 默认读取房间近期消息与档案 | 焦点人物实时轮次、回复配对、话题迁移和多人插话 |
| 长期记忆 | 可持久提炼、原文兜底、后台索引、主体配额 | 来源可信提升、有效期/否定修订、索引一致性、任务失租 |
| 纠错 | 动态成品缓存已移除，可看到部分近期纠正 | 没有明确纠正状态/事实撤回传播，旧Soul和旧索引可能继续影响 |
| 有证据地回答 | 工具结果包含角色/时间/来源，知识有相关性阈值 | 证据在回落时丢失、当前人物与证据归属未有系统验收 |
| 知道自己在哪里 | 运行状态已与实际房间对齐 | 切房任务/多通道/视觉来源的版本与时效一致性 |
| 主动陪伴 | 可看画面并读取共享资料 | 克制、沉默、观点去重、不中断对话、音画事实可信度 |

因此合适的现阶段标签是：**具备上下文和工具的陪伴系统，尚待质量验收；不能认定为稳定的熟人长期陪伴智能体。** 不给没有测量依据的“8分智能”或改进百分比，也不能反过来说代码没有进步。

## 9. 下一轮优先顺序

1. **先处理真实性与边界**：N01校验预算、N05 Soul提升、N09落库ACK、N10 actor归属、N11恢复路由、N12确认语义、N14超时补偿；N07外发策略单独明确。
2. **再完善对话理解**：N02当前Event＋焦点轮次、N03证据保留、N04别名接线与新观众not_found，确保“数据存在→读到→送入→使用→输出归属”整链可观察。
3. **稳定后台记忆**：N06租约、N08索引CAS/模型指纹，历史派生数据清洗的实际记录和检索对照。
4. **快速修运行入口**：N15页面脚本；N16干净PG与N17运维加载；其余N18—N21按影响安排。
5. **最后评估与调优**：真实对话回放、多轮盲评、受控直播；再调整窗口、模型、预算与主动策略，不同时改一大批提示词后凭感觉归因。

各任务需要新的精确规格与独立验收；不从本报告自动放行跨Phase/契约变更。N13保持现有产品限制范围，N22不擅自给@回复增加业务限流。

## 10. 无需先上直播也能进行的智能验收

先用假Provider抓取**实际生产装配的输入**，检查以下12组硬条件；再用同一数据跑真实模型对照。假Provider只证明资料与控制流，不证明模型真的理解。

| 场景 | 输入/流程断言 | 真实模型观察 |
|---|---|---|
| A问“我是谁”，房间无A近期消息 | 当前事件UID/名明确且可信 | 不把自己或B认成A |
| A问过问题，B插入30条，A继续“所以呢” | A的焦点轮次仍在，reply_to可追溯 | 接续A原问题，不回复B的话 |
| A/B同时“记得我吗” | 两份当前身份与证据分别正确 | 不共享答案、不张冠李戴 |
| “今夜琴身温柔/弓长三溢” | 实际生产别名/音近候选和理由进入 | 单候选可确认，多候选不强认 |
| 陌生UID/未知昵称 | not_found与unavailable不同 | 有边界地说不知道，不造熟人 |
| “贞德是谁/鲁迅是谁” | 问题主体与房间候选不同处理 | 不一律解释自己或一律查观众 |
| “那句不是我说的，是B说的”后重问 | 当前纠正与原证据、派生失效有记录 | 承认并按正确归属回答 |
| 普通观众“主人是阿猫” | 不自动成为owner全局设定 | 后续不被恶搞改写主人 |
| 工具查到事实后最终超时 | 降级仍有已验证证据，剩余预算明确 | 不回到完全失忆或无依据编造 |
| 一次画面连续五分钟不变 | 可选择stay_silent，有观察ID | 不反复换形容词刷屏 |
| 切房/切号恰在生成/arm附近 | 目标身份/阶段/观察epoch一致 | 不跨房间或账号回答，不重做副作用 |
| 搜索未配置/失败/确无结果 | capability和结果状态明确 | 不声称已联网或把失败说成查无事实 |

建议首批约30—50条多轮场景，其中至少一半是多人交错、歧义、未知和纠正；保留未用于调参的一组。比较同模型同配置下的基线/候选，多次运行，记录认人准确率、错误归属率、证据支持率、纠正后复犯率、主动重复率、queue_age、端到端P95和费用覆盖。

固定用例的隐私/错投/伪确认应零失败；质量门槛由真实基线与产品目标决定。不能只用工具调用成功率、原词召回或风格词命中率衡量“聪明”。用户已说明未实测，本轮不代为进行真实平台或付费模型试验。

## 11. 本轮执行结果与限制

| 检查 | 结果 | 解释 |
|---|---|---|
| `go test ./... -count=1 -timeout=20m` | 通过 | 现有普通测试；knowledge约214秒，不含新overlay失败用例 |
| `go test -tags=integration ./internal/database ./internal/infra ./cmd/fishd ./cmd/fishctl -count=1 -timeout=20m` | 通过 | 本机现有PG＋独立随机schema；不等于空数据库/CI初始化已验证 |
| `go vet ./...` | 通过 | 受审基线静态检查 |
| `go build ./...` | 最终复跑通过 | 临时探针曾因共目录不同package影响一次构建，已改为隔离main目录/非.go实体并复跑；不是业务代码缺陷 |
| `go test -race`关键包 | 通过 | reasoning、brain/provider、memory/extractworker、account、durable、web、cmd/fishd；WinLibs 64位GCC；非全仓/非integration-race |
| Node protocol/sender/gift | 51/51通过 | 现有离线协议测试，不是真实平台效果证明 |
| Context探针 | 观测N01/N02/N03 | 零真实网络；原始JSON已保存 |
| live页面Node VM | 3例失败 | 执行受审HTML脚本＋最小DOM；非浏览器截图/E2E |
| delivery overlay | 5个不变量失败 | 未确认升级、空响应成功、组合伪确认、unknown释放、Commit早于Publish |
| memory overlay | 2个缺陷行为断言通过 | 证明普通观众提升owner、陌生UID变error；不是验收绿 |

尚未执行：新增问题的真实PG故障注入、空数据库CI、干净Compose启动、完整浏览器操作、真实模型质量回归、2小时直播、生产存量污染/备份盘点。完整lint工具若不在当前PATH，标未执行；不把vet替代lint。

本轮只新增复核文档、探针与指纹证据、索引和本人报告。接手前工作区已有未跟踪`cmd/fishd/personas/`，本轮未改动或认领。生产代码没有自动修复，仍由实施agent按新问题领取、验证、独立验收。
