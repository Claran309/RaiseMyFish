# 架构重构计划（P0 完全结束后讨论）

> 决策（2026-11-02 主人定）：**架构重构在 P0 完全结束后再讨论**，不在 P0 收尾期做。
> 当前认知：项目结构偏扁平（internal/ 19 包按领域分包），依赖方向乱、serve.go 逻辑堆叠、config 横向耦合。
> 本文档只记录「问题清单 + 候选方案」，不实施。

## 一、当前问题清单

| # | 问题 | 严重度 | 说明 |
|---|---|---|---|
| 1 | **依赖方向乱** | 🔴 | cmd 直接 import internal 各包；包间无层次约束（比如 database 层被上层反向依赖） |
| 2 | **serve.go 逻辑堆叠** | 🟠 | 一个文件管 Generate/Deliver/Verify/记忆召回/知识召回/状态注入 |
| 3 | **config 包横向耦合** | 🟠 | 所有模块的配置都在 `internal/config/`，一个包知道全部 |
| 4 | **scheduler 包过大** | 🟠 | 五道闸 + 状态机 + 租约 + 令牌桶都在一个包 |
| 5 | **接口风格不统一** | 🟡 | 有的模块用 interface（store/event），有的用 struct（brain 部分） |
| 6 | **规则版记忆/判断** | 🟡 | 妥协实现（LLM 版见下文「记忆 LLM 提炼」） |

## 二、候选方案（不实施，待讨论）

### A. 依赖方向约束（低成本）

```
cmd（组合根）→ application（用例）→ domain（实体/接口）→ infra（PG/HTTP/子进程）
```

- Go 生态不流行「三层」；建议**按领域分包 + 依赖方向 lint**（不允许 infra 反依赖 domain）
- 可先写「import 图检查」脚本，不重构目录

### B. serve.go 拆分（低成本）

```
cmd/fishd/
├── generate.go   （Generate handler：reasoning/召回/brain/sender/结清）
├── deliver.go    （Deliver handler：resume）
├── verify.go     （Verify handler）
├── recall.go     （recallMemory/recallKnowledge）
└── presence.go   （状态注入）
```

### C. config 拆包（中成本）

每个模块自己的 config（`internal/brain/config.go`、`internal/scheduler/config.go`…），`internal/config` 只做加载与聚合。

### D. 通道抽象（大成本，P1 接评论/私信时做）

```go
type Channel interface {
    Name() string
    Collect(ctx) (<-chan Event, error)   // 或由 collector 统一
    Send(ctx, Reply) (Result, error)
}
// 注册表：channels.Register(liveChannel{}), commentChannel{}...
```

### E. 工具注册表（ReAct 前置，P6 前）

```go
type Tool interface {
    Name() string
    Desc() string
    Run(ctx, args) (string, error)
}
// Registry：Register(memorySearch), Register(knowledgeSearch), Register(presence), Register(gift)...
```

## 三、时机

- **P0 完全结束**（2 小时验收 + push）→ 讨论架构重构方案
- 重构不重写：按「阶段 1 依赖约束 → 阶段 2 拆文件/拆 config → 阶段 3 通道抽象」渐进
- 每个阶段独立验收，不破坏现有测试
