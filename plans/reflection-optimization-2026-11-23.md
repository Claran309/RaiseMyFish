# RaiseMyFish Reflection 改进计划（R 系列 · 2026-11-23）

> 依据：`D:/CodeStudy/growing/项目/Reflection补充优化清单-20261123.md`。
> 范围：**只做 RaiseMyFish 仓库的 R1/R2/R3/R7**；ClaranAIM 的 R4/R5/R6 属于另一个仓库/另一个项目，本计划不越界、不修改。
> 关系：与 [b-optimization-2026-11-23.md](b-optimization-2026-11-23.md) 的 B1（原生 tool calling）和 B2（trace）衔接；Reflection 的 span/指标并入 B2，不建第二套观测。
> 性质：增量改进，不换技术栈，不改变现有文本 ReAct 的默认行为。

---

## 0. 现状结论

| 项 | 现状 | 限制 |
|---|---|---|
| Reflection 有没有 | ✅ 有：`reasoning.Agent.finish → reflect` 一次自检 | 只在直播弹幕；一次；同模型 |
| 预算 | `reflect()` 要求 `steps < MaxSteps`；默认 MaxSteps=3 | 3 步查证后 Reflection 被饿死 |
| 渠道 | `cmd/fishd/react.go` 的 `Reflect` 仅 `event.ChLive` 为 true | 评论/私信完全不反思 |
| 归因 | 只知道跑没跑，不知道改没改、改好改坏 | 无 `ReflectCalled/Changed/SkippedReason` |
| 原生路径 | 本轮 B1 的 `RunNative` 还没有调用 `reflect` | 开启 `native_tools=on` 后 Reflection 会进一步缺失 |

> 代码位置（当前工作区）：`internal/reasoning/agent.go` 的 `finish/reflect/reflectPrompt`；`cmd/fishd/react.go` 的 `reactRuntime` 与 `runReact`；`internal/reasoning/native.go` 的 `RunNative`。

---

## 1. 目标

把「有没有 Reflection」变成「**可触发、可观测、可评测、可回退**」：

- 触发条件明确：不是每条都反思，按长度/工具/渠道/任务类型；
- 预算独立：不抢工具步数，不为反思把 8 秒拖成 16 秒；
- 可归因：`called/changed/skipped_reason/latency/tokens`；
- 可评测：50 条对照集，证明提升率/伤害率/成本；
- 可回退：配置开关，默认行为不变；失败保留原稿。

---

## 2. 工作包

### R1 让 Reflection 不再和工具步数抢预算（P0，0.5–1 天）

#### 问题
`reflect()` 的 `steps` 是 ReAct 总步数（工具调用 + 降级调用都算）；`MaxSteps=3` 时，3 步查证的复杂回复反而永远不反思。

#### 设计
- `Options` 增加：
  ```go
  MaxToolSteps int // 工具步数上限（默认沿用 MaxSteps）
  ReflectBudget int // 0/1，默认 1；ReflectBudget=1 表示允许额外一次自检
  ```
- `Agent.Run` 区分 `toolSteps` 与 `reflectSteps`：
  - 工具循环用 `toolSteps < MaxToolSteps`；
  - `finish()` 调 `reflect` 时只看 `ReflectBudget` 与剩余时间，不看 `toolSteps`；
  - 总调用上限 = `MaxToolSteps + ReflectBudget`（默认 4）；
  - 总超时仍由 `Options.Timeout` 控制，默认 8 秒；剩余时间不足 1.5 秒时跳过并记录 `reflect_skip_reason=timeout`。
- `Result` 增加：
  ```go
  ReflectCalled bool
  ReflectChanged bool
  ReflectSkippedReason string // budget/timeout/prompt/error/disabled
  ReflectPromptBytes int
  ```
- `RunNative`（B1）也接同一 Reflection：
  - 原生路径在得到最终正文后调用同一 `reflect` 逻辑；
  - 如果 `toolLLM` 不支持二次调用，`ReflectSkippedReason=disabled`，不阻塞主回复。

#### 改动文件
`internal/reasoning/agent.go`、`internal/reasoning/agent_test.go`、`internal/reasoning/native.go`、`internal/reasoning/native_test.go`、`internal/config/reasoning.go`（新增 `reflect_budget`、`reflect_comment/dm` 留 R2）。

#### 测试
- `steps=3 + Reflect=true + 时间充足` → `ReflectCalled=true`，最终文本可变化；
- `steps=3 + Reflect=true + 时间不足` → 跳过，原稿保留，`ReflectSkippedReason=timeout`；
- `Reflect=false` → 不调用，`ReflectCalled=false`；
- `ReflectBudget=0` → 不调用；
- 预算上限不超 `MaxToolSteps+ReflectBudget` 次模型调用；
- 原生路径 `RunNative` 同样覆盖上述两条。

#### 验收
- [x] 3 步工具查证后 Reflection 仍能执行（时间允许时）
- [x] `ReflectSkippedReason` 可查（disabled/timeout/prompt/error）
- [x] 总超时不超过配置
- [x] 旧文本 ReAct 用例不回归

#### R1 执行记录（2026-11-23）

- `Options` 增加 `MaxToolSteps` 与 `ReflectBudget`；工具循环只看 `MaxToolSteps`，反思不再被工具步数饿死。
- `Result` 增加 `ReflectCalled/ReflectChanged/ReflectSkippedReason/ReflectPromptBytes`；正常收尾、降级收尾与原生收尾共用同一套归因。
- 配置 `[reasoning] reflect_budget`：默认 1，`-1` 关闭，越界拒绝；生产装配把工具步与反思预算分开传。
- 日志与 trace 只记「是否调用/是否改写/跳过原因/prompt 字节」，不记草稿或改写全文。
- 验证：`go test ./internal/reasoning/... ./internal/config/... ./cmd/fishd/...`；新增 `TestReflectRunsAfterToolStepsExhausted` 证明工具步用满后仍反思。

---

### R2 评论/私信按需开启 Reflection（P1，1–2 天）

#### 设计
- 配置：
  ```toml
  [reasoning]
  reflect_comment = false
  reflect_dm = false
  reflect_min_runes = 60
  reflect_on_tools = true
  reflect_critic_route = "" # 空=同模型；指向便宜模型路由
  ```
- 触发条件（满足任一）：
  - 草稿长度 > `reflect_min_runes`；
  - 本轮用过工具（`len(Evidence)>0`）；
  - `ThinkDeep` / 多轮上下文；
  - 评论/私信为问答/请求类（规则或小模型判定，先规则）。
- 不触发：寒暄、短句、纯表情、已 `Reflect=false` 的渠道。
- 便宜 critic：若 `reflect_critic_route` 配置了路由，用该路由；否则同模型（并在报告标注）。
- 指标：`reflect_called`、`reflect_changed`、`reflect_latency_ms`、`reflect_tokens`，进 B2 trace。

#### 测试
- 短寒暄不触发；60 字以上触发；带工具触发；
- 评论/私信配置开/关；
- critic 路由不可用 → 回退同模型/跳过，不报错；
- P95 延迟增加 <1.5 秒（用 mock，不测真实模型）。

#### 验收
- [ ] 评论/私信开关可控
- [ ] 触发条件可解释
- [ ] 50 条对照有量化结果
- [ ] 提升 <3 个点则默认关闭，只保留开关

---

### R3 给 Reflection 加效果归因与评测（P0/P1，1–1.5 天）

#### 设计
- `Result` 的 R1 字段已覆盖基础归因；日志增加：
  - `reflect_diff`：只记长度变化/是否变化（不记全文）；
  - `reflect_changed_rate`、`reflect_improve_rate`、`reflect_harm_rate`、`reflect_cost_per_reply`（日报/面板）。
- 50 条 live 用例：同一输入分别 `Reflect=false/true`，人工或 LLM judge 评「答非所问/编造/语气/长度」；用例放 `personas/dafeiyu/eval/reflection-cases.jsonl`（不传给模型）。
- 指标：
  - `reflect_changed_rate = changed / called`
  - `reflect_improve_rate = improved / changed`
  - `reflect_harm_rate = harmed / changed`
  - `reflect_skip_rate`
  - `latency_delta`、`cost_delta`
- 接入 B3 `make eval` 报告，新增 `reflection` 段；不另建评测框架。

#### 验收
- [ ] 能回答「Reflection 让多少条变好/变坏」
- [ ] `reflect_harm_rate >5%` 时有告警/报告
- [ ] 50 条用例可重复跑
- [ ] 报告进 CI artifact

---

### R7 统一 Reflection 评测协议（可选，1–2 天）

本项目只做 RaiseMyFish 侧：
- 四类用例：该改 / 不该改 / 预算边界 / 安全边界；
- 指标：`improve_rate/harm_rate/changed_rate/skip_rate/latency_delta/cost_delta`；
- 结论表：`Reflect=off/on` 对比；没有提升就不默认开启；
- 与 ClaranAIM 的 R7 共用格式即可，不在本仓库实现 ClaranAIM 的 critic。

---

## 3. 实施顺序

| 顺序 | 事项 | 依赖 | 预计 |
|---|---|---|---|
| 1 | R1 预算独立（文本 + 原生） | B1 代码（已落地） | 0.5–1 天 |
| 2 | R3 归因 + 50 条评测 | R1 | 1–1.5 天 |
| 3 | R2 评论/私信灰度 | R1/R3 | 1–2 天 |
| 4 | R7 统一报告（RaiseMyFish 部分） | R3 | 1 天 |

**如果只有 3 天**：只做 R1 + R3，先把「经常跳过」变成「可测、可解释」。

---

## 4. 测试与验收门禁

- `go test ./internal/reasoning/... ./cmd/fishd/...` 全绿；
- 新增 `reflect_budget` 配置校验；
- mock 覆盖：时间充足/不足、步数用满、原生/文本路径、失败保留原稿；
- 不调用真实模型；真实 50 条对照需主人授权，单独标记费用。

---

## 5. 不做什么

- ❌ 不做多轮 Reflection loop（critic→改写→再 critic→再改写）；最多 1 轮；
- ❌ 不把 Reflection 当准确性保证；
- ❌ 不在每条短弹幕上开启；
- ❌ 不用 Reflection 掩盖「工具没调用/任务没完成」；
- ❌ 不修改 ClaranAIM 仓库的 R4/R5/R6（不在本仓库范围）。

---

## 6. 变更记录

- 2026-11-23：初版，依据 Reflection 补充清单编制；只覆盖 RaiseMyFish R1/R2/R3/R7。
