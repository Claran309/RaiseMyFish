# 测试记忆失败修复派发单（2026-11-10 · 娇娇直播间）

> 来源：娇娇直播间测试的 7 个失败案例。
> 范围：观众查询意图、时间类记忆召回、侧写分批回填。
> 禁止：不改 contracts、不碰调度/发送、不执行 git；交付后暂停写入等验收。

## M01 · 观众查询意图扩展

**问题**：只有「X是谁」触发 viewer_search；「记得XX吗」「认识XX」「XX喂过饭」「跟XX聊过」被当成普通闲聊，不去查观众档案。

**改法**：`question.go` 增加 `check=true` 回忆核查信号（记得/认识/喂过/聊过/打过招呼 + 人名），主体归为 room_candidate；`context.go` 把核查名并入候选；`reasoning/agent.go` 提示先 viewer_search 再 memory_search。

**验收**：「记得今夜琴声温柔吗」→ room_candidate + check。单测 13 用例。

**状态**：[x] 完成

## M02 · 时间类记忆召回

**问题**：「昨天聊天 / 昨晚的事 / 上次说过」语义检索匹配不到时间词。

**改法**：`memory_search` 支持 `time` 过滤。工具层解析 昨天/昨日/前天/昨晚/上次 → 半开窗口 `[After, Before)`；昨晚=[昨日18:00, 今日06:00) 且截断到当前时刻；上次=最近 7 天。关键词清洗去时间词、去过/了变体（最多 3 个），仅剩时间词时关键词可为空只查窗口。`Search` 有时间窗时跳过语义向量路，改走 `TimeSearchRecordSource`（消息源可选）；不支持时明确报错。

**验收**：
- 「记得昨天聊天」→ 关键词「聊天」+ 昨日窗口
- 「有没有喂过饭」→ 变体「喂饭」
- 「昨晚顶号的事」→ 昨晚窗口；空关键词时回退只查窗口

**状态**：[x] 完成

## M03 · 侧写分批回填

**问题**：645 位观众里只有 2 条侧写（自动回填每轮仅 BackfillLimit=2）。

**改法**：
- `store/database`：新增 `BackfillViewers`（从未侧写、退避未到期、按 hits 倒序、严格截断）。
- `viewer`：`Summarizer.RunBackfill(ctx, limit)`（1..500），逐人前台让路，不动撤回/过期/投影、不扣日预算。
- `cmd/fishd`：worker 增加 `RunBackfill`；控制面新增队列（容量 1，pending/running 拒绝并发）、`TriggerViewerBackfill`；冻结优先、忽略暂停。
- `internal/web`：`/api/viewer/summary` 新增 `backfill` 动作（默认 50，上限 500），状态返回 `backfill_pending/backfill_running`；memory 页面加入回填人数输入与按钮。

**验收**：7001/7002 命中、7003（退避）与 7004（已有侧写）排除、Limit 截断；面板 API 与页面断言齐全。

**状态**：[x] 完成

## M04 · 门禁与验收探针

- `TestViewerSummaryPanelOps` 修复既有时间依赖：`RequeueSummary` 用 `clock_timestamp()` 写重试时间，测试原用固定历史 `Now` 导致「已重建」被误过滤。最小改动：查询 `Now` 与 6102 退避时间锚定真实墙钟（`time.Now().UTC()`），断言（只命中 6101、6102 不受影响）不变。
- 门禁：`gofmt` 改动文件全绿、`go build ./...`、`go vet ./...`、全仓 `go test ./...`、改动包 `-race`、集成 `TestViewerSummary|TestMemory` 全部通过。

**状态**：[x] 完成

## 未做项（明确记录，不自行扩展）

- 时间窗检索有意跳过语义向量路（窗口内按关键词/SQL 过滤，避免跨时间噪声）。
- 手动回填忽略日预算与暂停，但遵守记忆冻结（显式操作与自动调度的差异）。
- 645 人全量回填需多次操作（单次上限 500，建议 500+145 两批）。
- 「话题变化检测」仍为 R06 遗留未做项。
- 昨晚/上次窗口为固定定义：昨晚 [昨日18:00, 今日06:00)，上次最近 7 天。
