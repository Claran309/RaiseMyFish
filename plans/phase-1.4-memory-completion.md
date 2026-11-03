# 记忆补全派发单（上下文喂入 + fact from_id + 时效更新）

> 目标：把之前推迟的三项提上来现在做——记忆 LLM 提炼喂上下文、fact 加 from_id、fact 时效更新。

## 任务 1：记忆 LLM 提炼喂「最近 N 条上下文」

### 现状
`LLMExtractor` 有 `ContextLimit` 参数（默认 5），但 `extractworker` 只传单条事件 + 昵称——模型看不到上下文。

### 要求
- `extractworker` 在调 `Extractor.Extract` 前，把「该事件之前的最近 N 条事件」（同 session、按 at 排序）放进 `ExtractOptions.Context`（新字段，[]event.Event 或 []ContextMessage）
- `LLMExtractor` 的 prompt 用 ContextLimit 截断后拼进 user 段（如「最近聊天：\n- A: ...\n- B: ...」）
- worker 从哪拿上下文：`DoneSource` 加方法？或复用 `history` 工具的数据源（database/events_recent.go 已有查询）——**优先复用 events_recent**
- 上下文只读、不写库；取失败降级为无上下文（不阻塞）

### 测试
- mock 上下文 → 断言 LLM prompt 含上下文
- 上下文取失败 → 无上下文照常提炼

## 任务 2：fact 加 from_id 列（按 uid 查事实）

### 迁移
```sql
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS from_id TEXT;
ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS from_name TEXT;
CREATE INDEX IF NOT EXISTS idx_memory_records_from_id ON memory_records(from_id) WHERE from_id IS NOT NULL;
```

### 代码
- `store.MemoryRecord` 加 `FromID/FromName`
- `internal/memory/extract.go`：`Candidate` 加 `FromID/FromName`（fact 规则填 ev.FromID/ev.FromName）
- `extractworker`：写 memory_records 时带 from_id/from_name
- `internal/database/memories.go`：写入列 + `FactsByViewer(ctx, fromID, limit)` 查询
- `fishctl memory` 加 `--uid` 过滤（可选）

### 测试
- 提炼 fact → 表里 from_id 正确
- 按 uid 查事实 → 只返回该人的

## 任务 3：fact 时效更新

### 现状
`fact:<from_id>:<sha256(title)>` 主键去重——同人同事实**永远保留最先那条**（「我今年大二」明年还在）。

### 要求
- fact 去重（ON CONFLICT）时**比对 summary**：
  - 相同 → 不动（幂等）
  - 不同（事实变化）→ **更新 summary/valid_from**，旧版本归档（memory_records 加 `superseded_by` 或直接覆盖 + 记 history？）
- 简化方案（P1 可接受）：**覆盖更新**（summary 变化就更新 + valid_from=now），不做版本链
- 测试：同人同事实不同 summary → 更新；相同 → 幂等

## 禁止

- 不改 contracts 语义（列可加）、不碰调度/发送、不执行 git
- 测试 mock，不调真实模型

## 交付格式

- 文件列表、迁移、测试结果、未做项、是否暂停写入
