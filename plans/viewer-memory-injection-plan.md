# 观众记忆全注入改造计划（2026-11-10）

> 主人要求：**观众记忆不应该是关键词召回，而是主动注入直接召回**（「都改」）。
> 原则：当前对话相关的资料**默认进 prompt**，工具保留作为补充（查别人/深挖时用）。

## 改造范围（4 处）

| # | 资料 | 现状 | 目标 |
|---|---|---|---|
| 1 | **当前观众历史记忆**（喂饭/聊天/承诺/偏好） | 工具召回（memory_search） | 注入 ContextInput.Viewer（≤N 条） |
| 2 | **当前观众时间窗对话**（昨天/昨晚/上次） | 工具召回 | 注入「最近 7 天对话摘要」到 Viewer 段 |
| 3 | **甲问乙时乙的档案** | 工具（R04 补了 X是谁） | 识别乙 UID 后**注入乙档案**（姓名/统计/侧写/少量原话） |
| 4 | **观众名单**（房间常客） | 没注入 | 注入「最近活跃观众 Top N」（可选，预算允许时） |

## 预算控制

- ContextInput 已有 400 字预算（formatViewerProfile）
- 注入内容按优先级：**当前观众档案 > 历史记忆 > 时间窗 > 乙档案 > 名单**
- 超预算时从低优先级裁

## 实现要点

1. **`cmd/fishd/context.go`（buildPassiveInput）**：
   - 当前观众：查 `memory_records`（from_id=观众 或 subject=viewer&asserted_by=观众）→ 注入
   - 时间窗：查最近 7 天 memory_records（valid_from）→ 注入摘要
2. **乙档案**：question.go 识别 room_candidate（乙）后，查乙的 viewer_profiles + 侧写 → 注入
3. **观众名单**：查最近活跃观众（ActiveViewers/最近弹幕）Top 5-10 → 注入
4. **工具保留**：memory_search/viewer_search 仍可用（查更深的/别人的）

## 验收

- 娇娇测试 7 案例：
  - 「记得今夜琴声温柔吗」→ 若 X 是当前观众 → prompt 里有 X 的记忆
  - 「记得昨天聊天」→ 时间窗摘要可见
  - 「喂过饭」→ 观众记忆可见
  - 甲问乙 → 乙档案可见
- fake Provider 断言注入内容
- 预算超限裁剪测试

## 风险

- prompt 变大 → 用预算裁剪（≤400 字 Viewer 段 + 预算系统）
- 隐私：只注入公开记忆（viewer 可见性条件）

## 完成状态（2026-11-10）

- [x] **P01 问题主体预取**：`brain.ResolveQuestionTarget` 复用 `buildQuestionSubject`，
  只做纯解析（不查库），供 `cmd/fishd` 在组装快照时定位「甲问乙」的乙。
- [x] **P02 Viewer 段优先级注入（≤400 字）**：`contextBuilder.viewerText` 按
  「当前观众档案（≤220）> 相关记忆（≤6 条）> 最近 7 天（≤4 条）> 乙档案（≤200）> 房间名单（≤5 条）」
  逐段填充；放不下的低优先级整段丢弃，乙档案在剩余 ≥40 字时按剩余预算截断。
  `buildConsumeWorkers` 增加可选依赖 `withViewerFinder`，生产装配传入 `viewerSearch`。
- [x] **P03 注入测试与 7 案例验收**：
  - `cmd/fishd/viewer_inject_test.go`：乙按 UID/昵称/歧义/自身/无证据/查不到、名单直播限定与去重、400 字裁剪；
  - `cmd/fishd/viewer_memory_test.go`：7 案例走真实 `brain.New(fake Provider)` + `buildConsumeWorkers`，
    断言最终 prompt 里的档案/记忆/乙档案/名单；相关段与时间窗段并存的口径单独覆盖；
  - `cmd/fishd/context_test.go`：保留陌生人 vs 读取失败的区分。
- [x] **P04 门禁**：`gofmt`、`go build ./...`、`go vet ./...`、`go test ./...`、
  `go test -race ./cmd/fishd ./internal/brain` 全绿。

### 未做项 / 边界

- 名单只按 `TopViewers`（发言次数倒序，`hits DESC, last_seen DESC`）；没有新增
  「仅最近 7 天活跃」的仓储查询，名单抬头如实标注「按发言次数」。
- 乙只在 `room_candidate` 时预取；`unresolved`（如「鲁迅是谁」）不查观众库，
  避免把公众人物/虚构角色误当观众（工具仍可手动 viewer_search）。
- 乙按昵称找回只接受 `viewer.StatusFound`；歧义/查不到一律不注入。
- 主动弹幕路径没有 finder（无「提问者」），但同样享受当前档案/记忆/名单注入。
- 本批不改 contracts、不碰调度/发送；未执行 git。
