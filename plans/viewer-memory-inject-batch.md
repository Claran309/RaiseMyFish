# 观众记忆主动注入派发单（2026-11-10）

> 来源：主人要求「不应该是关键词召回，而是主动注入直接召回」。
> 问题：「记得XX吗」「喂过饭」依赖 viewer_search/memory_search 工具 + 关键词召回，
> 工具不一定被调用、关键词召不回 → 鱼「不记得」。
> 范围：当前发言者的相关记忆随档案直接进 prompt；工具保留（查别人时用）。
> 禁止：不改 contracts、不碰调度/发送、不执行 git；交付后暂停写入等验收。

## N01 · store/database 观众相关记忆查询

**改法**：
- `internal/memory`：新增可选能力 `ViewerRecordSource`（`ViewerRecordQuery{Scope, UID, Limit}`），
  `AssembledReader` 做 ctx/Scope/UID 校验、limit 夹取、DTO 二次校验后转发；
  `HybridSearcher` 原样透传；底层未实现时返回 `nil, nil`（缺失不等于故障）。
- `internal/database`：`MemoryRepo.ViewerRecords`（编译期断言满足可选能力）。
  关系口径：`from_id=观众 OR (subject='viewer' AND asserted_by_id=观众)`；
  排除 `kind='viewer_profile'`（侧写由档案段注入，避免重复）；
  可见性沿用 `memoryScopeQuery`：`valid_to IS NULL AND NOT archived AND valid_from <= now()`，
  公开视角只读公开、私密视角=公开+本会话私密、DM 行不进入公开视角。

**验收**：关系三态（自述 / asserted_by / 他人）、归档/失效/未来记录排除、
`viewer_profile` 排除、跨 DM 会话隔离、排序（valid_from DESC）与 limit、
空/超长 UID 与非法范围拒绝。

**状态**：[x] 完成

## N02 · contextBuilder 主动注入当前发言者记忆

**改法**：`cmd/fishd/context.go`
- `viewerText`：有档案时在档案后追加记忆段（`appendViewerMemories`）；陌生人（ErrNotFound）保持不查不注入；
  档案读取失败仍注入显式状态。
- 读取：`ViewerRecordSource.ViewerRecords`（最多 20 条，UID 取可信事件 `FromID`）。
- 渲染（`formatViewerMemories`）：
  - 「最近 7 天聊过的」：`ValidFrom >= now-7d`，最多 4 条，新的在前（支撑「昨天聊天/昨晚顶号」）；
  - 「跟他相关的记忆」：按 importance DESC、同分新的优先，最多 6 条（喂饭/承诺/偏好）；
  - 两段按 ID 去重；单条摘要截 60 字；整段（档案+记忆）≤ 760 字（< `brain.maxViewerRunes=800`）。
- 记忆查询失败只丢记忆段并记 Warn，档案照常注入；`now` 取事件时间（缺省墙钟）。

**验收**：注入内容断言、两段去重、条数与长度上限、失败降级、陌生人不查、记忆源无能力不注入。

**状态**：[x] 完成

## N03 · 注入测试与验收探针

- `internal/memory/viewer_records_test.go`：转发/夹取、可选能力为空、参数与越界数据拒绝、HybridSearcher 透传。
- `internal/database/viewer_memory_integration_test.go`（integration）：真实 PG 的关系口径与隐私过滤。
- `cmd/fishd/viewer_memory_test.go`：
  - 快照级：注入两段、去重、`brain.Prepare` 通过、失败降级、陌生人/无能力跳过、渲染上限与预算；
  - 假 Provider 验收：娇娇 4 个典型追问（记得我吗/有没有喂过饭/记得昨天聊天吗/昨晚顶号的事）
    在无任何工具调用的情况下，实际 prompt 的 `<viewer>` 段直接包含相关记忆。

**状态**：[x] 完成

## N04 · 门禁与计划状态

- 门禁：`gofmt` 改动文件全绿、`go build ./...`、`go vet ./...`、全仓 `go test ./...`、
  受影响包 `-race`（memory/brain/reasoning/viewer/fishd/database）、
  集成 `TestViewerRecords|TestMemory` 全部通过。

**状态**：[x] 完成

## 未做项（明确记录，不自行扩展）

- 不替代工具：工具仍在，查他人历史/纠正/时间窗深查继续走 `viewer_search`/`memory_search`。
- 注入只针对「当前发言者」，不做房间内其他观众的主动注入。
- 「最近 7 天」按记忆 `valid_from` 判定；跨 7 天但重要的记忆仍会出现在「跟他相关的记忆」段。
- 面板无新增可观测数据，本批不动 `internal/web`。
- 主动弹幕路径复用同一 builder，观众不在场时（无 FromID）不注入，未另开入口。
