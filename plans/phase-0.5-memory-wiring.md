# P0.5 补丁 · 记忆接线派发单（extractworker 接入 + fishctl memory）

> 背景：0.9 的提炼 worker（extractworker）与 MemoryRepo 都有，但 fishd 没启动提炼、fishctl 没有记忆查看命令——记忆表是空的，召回读不到东西。
> 本批补两件事，让记忆「真正活起来」。

## 任务 1：extractworker 接入 fishd

### 现状

`internal/memory/extractworker/`（worker.go、watermark.go）已验收（0.9b），但 `cmd/fishd` 从未启动它。

### 要求

1. `cmd/fishd/main.go`（或 serve.go 装配处）：fishd 启动时创建并运行 extractworker
   - `DoneSource`：用 `internal/database` 的 DoneSourceRepo（0.9c）
   - `MemoryRepo`：`internal/database` 的 MemoryRepo
   - 水位：`FileWatermark`（`logs/memory-watermark.json` 或 `state/` 下，避开 logs）
   - 每小时/每 N 分钟扫描一次（配置 `[memory] interval_seconds`，默认 300）
2. 配置 `[memory]`（`internal/config/memory.go`）：
   - `enabled`（默认 true——记忆是增强，不阻塞）
   - `interval_seconds`（默认 300）
   - `allow_private`（默认 false，0.9a 裁决）
3. 启动失败：worker 初始化失败记日志但不阻塞 fishd（降级）；`enabled=false` 时不启动
4. 日志：`action="memory_extract"`、`inserted=N`

### 测试

- 装配测试：mock DoneSource/MemoryRepo，断言 worker 被启动/水位推进
- 配置校验：interval 范围、enabled 开关
- 不连真实 PG（mock）

## 任务 2：fishctl memory 命令

### 要求

新增 `cmd/fishctl/memory.go`：

- `fishctl memory list --limit N`：列出最近记忆记录（id/kind/title/importance/at）
- `fishctl memory search --q <关键词>`：按 title/summary 模糊搜索
- `fishctl memory stats`：按 kind 统计数量
- 输出 JSON（与 status 一致风格）

### 依赖

需要 `store.MemoryRepo` 增加**只读查询方法**（不破坏现有接口）：
- `ListRecords(ctx, limit) ([]MemoryRecord, error)`
- `SearchRecords(ctx, q, limit) ([]MemoryRecord, error)`
- `CountByKind(ctx) (map[string]int, error)`

在 `internal/store/memory.go` 接口里加（DSH 已确认可加）；`internal/database/memories.go` 实现。

### 测试

- fishctl 命令：mock 仓储注入、空数据、有数据
- 仓储查询：真实 PG 集成（list/search/count）

## 禁止

- 不改 contracts、不碰调度/发送/人格/知识库
- 不真实调模型；不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、未做项、是否暂停写入
