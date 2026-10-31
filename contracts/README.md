# 接口契约（Contracts）

> 契约是模块之间的「法律」。实现必须符合契约；改契约必须先改文档并说明理由。

## 原则

1. **依赖接口，不依赖实现**：上层只 import 契约类型，不 import 具体实现包。
2. **契约少而稳**：一个契约一个职责，不加「顺便」的方法。
3. **失败显式**：错误必须返回，不允许吞掉。
4. **上下文传递**：所有方法第一个参数是 `context.Context`。
5. **幂等优先**：写操作必须说明幂等键。

## 契约列表

| 文件 | 契约 | 使用方 |
|---|---|---|
| [event.md](event.md) | Event 结构与事件总线 | 所有模块 |
| [collector.md](collector.md) | 采集器接口 | 通道实现 |
| [sender.md](sender.md) | 发送器接口 | 通道实现 |
| [brain.md](brain.md) | 大脑接口 | P0 核心 |
| [persona.md](persona.md) | Persona Pack静态数据、加载与场景映射（P0-PACK-1） | 0.7b、0.6 |
| [memory.md](memory.md) | Memory最小上下文、Scope与读取边界（P0-MEMORY-1） | 0.6、0.9 |
| [knowledge.md](knowledge.md) | 知识上下文、Scope隔离、文件召回与版本（P05-KNOWLEDGE-1） | P0.5 K0 |

## 变更流程

1. 先改契约文档
2. 更新受影响的实现与测试
3. 在 Phase 计划里记录变更
4. 破坏性变更需要同步更新调用方

## 禁止

- ❌ 在契约包里塞业务逻辑
- ❌ 让契约依赖具体数据库/HTTP 客户端类型
- ❌ 用 `interface{}` / `any` 逃避类型设计（确需用 JSONB 时写清 schema）
