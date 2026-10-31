# 给 DSH + flash 的 Phase 0 提示词

> 执行类任务：样板代码、迁移、日志与日报。

---

## 通用要求

- 先读 `plans/assignment.md` → `AGENTS.md` → 相关文档
- 只碰 `internal/memory/`、`internal/observability/`、`scripts/`、`migrations/`（补充迁移）
- 争议升级给 Codex；文档与验收由本鱼负责
- 完成后 `go test ./...`，commit，汇报

---

## 0.9 记忆最小版

```
任务：Phase 0.9 记忆最小版。

必读：docs/06-memory.md、docs/03-events.md、AGENTS.md。

范围：
- internal/memory：发送后写 out 事件（必写）
- 简单提炼 worker（规则版：主人相关/承诺/首次出现 → memory_records）
- 召回最小版（最近会话滑窗 + 共享事件；P0 不做语义）

要求：
- 写入不依赖模型自觉，由代码路径触发
- importance/confidence 字段填值
- 私密记录带 private
- 重启后未提炼事件可补

验收：out 事件落库测试、提炼 worker 测试、重启补提炼测试。
```

---

## 0.10 可观测（日志/心跳/日报）

```
任务：Phase 0.10 可观测最小版。

必读：docs/10-observability-cost.md、AGENTS.md。

范围：
- internal/observability：日志聚合、心跳检测、日报生成
- scripts/dev.ps1 的收尾完善（目录创建、优雅退出、错误处理）

要求：
- 每个事件链路记录 event_id/ch/action/result/cost_ms
- 单通道 30 分钟无事件 → warn
- 日报输出 logs/daily-YYYY-MM-DD.md（P0 不落库）
- 不打印完整 prompt / 凭据

验收：日报生成测试、静默失败告警测试、脚本可启动/停止/查状态。
```
