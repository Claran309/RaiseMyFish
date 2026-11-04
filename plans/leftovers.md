# 遗留问题清单（后续改进，2026-11-05 更新）

> 汇总 2026-11-02 ~ 11-04 所有派发单交付报告里报过的「未做项/遗留/边界」。
> 状态：✅ 已修 / ⏳ 待做 / 📄 文档级（不需要代码）。

## 🔴 影响运行 / 待验证

| # | 项 | 出处 | 状态 | 说明 | 建议 |
|---|---|---|---|---|---|
| 1 | **2 小时正式验收** | P0 退出标准 | ⏳ | 需开播（真实直播 + 定时 + 异常注入） | 开播后跑 |
| 2 | **点赞路径不确定** | 0.22 | ⏳ | 用 `room/v1/Room/like`；B站还有 `xlive/web-ucenter/v1/roomAdmin/like`，看 code≠0 就改常量 | 验收时确认 |
| 3 | ~~当日收信人锁定进程内~~ | 1.11/1.12 | ✅ | 已落库（`state/dm-recipient.json` 跨重启持久化） | — |
| 4 | **私信只取首屏 20 会话** | 1.12 | ⏳ | `get_sessions` 不翻页；消息多的会话要 `get_session_msgs` 拉详情 | 下一批补 |
| 5 | **评论 root/parent 未用于多级回复** | 1.10 | ⏳ | 采集的真实 root/parent 仅存 Meta；回复统一 root=parent=rpid | 需要多级回复时改 |

## 🟠 影响能力

| # | 项 | 出处 | 状态 | 说明 | 建议 |
|---|---|---|---|---|---|
| 6 | ~~知识 Reader 启动快照~~ | 1.7 | ✅ | 已做热重载（原子快照 30s 轮询） | — |
| 7 | ~~Channel 无 Resume~~ | 1.9 | ✅ | 已收敛（发送也走 Channel，保留 sender 兜底） | — |
| 8 | **soul_path 固定 dafeiyu** | 1.13 | ⏳ | 多 persona 时应按 pack 派生 | 多 persona 时改 |
| 9 | ~~手工改 soul-memories.md 会被覆盖~~ | 1.13 | ✅ | 已做 Manual 标记 + MergeSoul（手写全留） | — |
| 10 | **执行器缺配置返回「未配置」** | 0.21 | 📄 | 功能可用性依赖配置（凭据/URL） | 文档说明 |
| 11 | **评论不配 scheduler 不回复** | 1.10 | 📄 | `[scheduler.channels.comment]` 未配则只采集不回复 | 文档/默认值 |

## 🟡 优化

| # | 项 | 出处 | 状态 | 说明 | 建议 |
|---|---|---|---|---|---|
| 12 | ~~web_search search_key 字面~~ | 1.6 | ✅ | 已支持 `search_key_env`（env 优先） | — |
| 13 | **web_search 无重试/白名单/内容过滤** | 1.6 | ⏳ | 失败由 ReAct 降级处理 | 按需加 |
| 14 | **web_search 未接 Serper/Bing 专用协议** | 1.6 | ⏳ | 用通用协议（POST {query}） | 按需接 |
| 15 | ~~always 触发多花钱~~ | 1.5 | ✅ | 已加 <10 字单步门槛 | — |
| 16 | ~~每轮读 soul 文件~~ | 1.13 | ✅ | 已加 mtime 缓存（没变不读盘） | — |
| 17 | ~~梗白学习扫描固定 live~~ | 1.7 | ✅ | 已暴露 window_hours/min_occurrences | — |
| 18 | ~~learned.md 只增不清理~~ | 1.7 | ✅ | 已加 trimMemeDoc（200 条/256KB 清理） | — |
| 19 | ~~常驻点赞只支持环境变量 cookie~~ | 0.13c | ✅ | 已支持 CookieFile（_FILE） | — |
| 20 | ~~gift 既有 4 条 lint~~ | 0.21 | ✅ | 已清零（cmd/fishd 旧账也清了） | — |

## ⚪ 远期 / 阶段任务

| # | 项 | 阶段 | 状态 | 说明 |
|---|---|---|---|---|
| 21 | 知识库 RAG（K2） | P2 | ⏳ | 无 pgvector 轻量版已做：title/keywords/tags/summary 多字段 + 档位打分；pgvector/嵌入/引用链仍待 P2 |
| 22 | Watchdog（自愈） | P4 | ⏳ | 检测异常（冻结/崩溃）→ 重启 |
| 23 | Web 面板（可视化） | P5 | ⏳ | status/记忆编辑/活动查看 |
| 24 | 常驻 Edge 截图 | 0.13c | ⏳ | 浏览器自动化（大工程） |
| 25 | MCP 工具层 / 编排 | P6 | ⏳ | Router/Worker、多会话并行 |
| 26 | 退款/补偿 | 0.8 | ✅ | 确定失败退额度；结果不确定保留额度并标记 pending_review |
| 27 | 记忆冻结 | 0.10 | ✅ | 配置 frozen + state/memory.frozen + fishctl memory freeze/unfreeze |
| 28 | 多实例/编排 | P6.5 | ⏳ | 多主体并行 |

## 已修（历史登记）

| 项 | 修复 |
|---|---|
| 租约失效（sender 已结清二次结清） | 0.18：sender.Handled 跳过 |
| budget 错误崩循环 | 0.18：Mark(dead) 后返回 nil |
| 知识召回超预算 | 0.18：去知识重试一次 |
| @ 自己（reply_to_name=自己） | 0.18：isSelfName 排除→FromName |
| Resume budget 假需求 | 核实：Resume 不调 brain |
| extractworker 没接档案重建 | 1.1：装配补 |
| extractworker 没接 LLM 提炼 | 1.3：Extractor 注入 |
| 记忆上下文没喂 | 1.4：Context 喂入 |
| fact 无 from_id/时效 | 1.4：迁移 0005 + IS DISTINCT FROM |
| 收信人锁定进程内 | 1.14：落库（state/dm-recipient.json） |
| 知识启动快照 | 1.15：热重载 |
| Channel 无 Resume | 1.16：收敛 |
| dm 测试桩 race | 1.16：fakeBus 加锁 |
| 记忆冻结（#27） | 本批：配置/状态文件双开关 + fishctl freeze/unfreeze |
| 退款/补偿（#26） | 本批：确定失败 Release；uncertain 保留额度 + pending_review |
| 知识召回多字段（#21 轻量） | 本批：title/keywords/tags/summary 档位打分（无 pgvector） |

## 使用说明

- 新遗留加进对应分组（写清出处/说明/建议）
- 修复后改状态为 ✅ 并在「已修」登记
- 验收（2 小时）时优先检查 🔴 组
