# RaiseMyFish 改进开发计划（B 系列 · 2026-11-23）

> 依据：`D:/CodeStudy/growing/项目/项目优化清单-20261123.md` 第二章「RaiseMyFish」B1–B11。
> 性质：**改进工作包**，不是新 Phase，不改变现有 Phase 0–8 的边界与退出标准，不改变技术栈。
> 编制：DSH（本鱼）。状态：**待主人确认后按批次派发/执行**。
> 原则：先修可信度（B1/B2/B3），再做评测与记忆闭环（B4/B5），最后做叙事与清理（B6/B7/B8–B11）；每批必须有测试、可回滚、可验收。
> 补充：延迟/死信口径见 [latency-optimization-2026-11-23.md](latency-optimization-2026-11-23.md)（L 系列）；B2 trace 与 L4.5 是同一能力，只做一套。
> 补充：长期记忆评审见 [memory-review-2026-11-23.md](memory-review-2026-11-23.md)（M 系列）；B5 与 M6 合并，M1–M5 是 B5 的细化。
> 补充：Reflection 改进见 [reflection-optimization-2026-11-23.md](reflection-optimization-2026-11-23.md)（R 系列）；R1/R2/R3/R7 只做本仓库，ClaranAIM R4–R6 不在范围内。

---

## 0. 结论摘要

1. 清单里 **B1、B2、B3 是 P0**，也是面试最容易被问穿的三项；当前代码只完成了「文本 ReAct + zap 日志 + 两套离线评测」，**原生 tool calling、per-event trace、`make eval` 都还没有**。
2. **B7（代码归属与讲解）不是代码任务**，但成本最低、收益最高，应最早排期，和 B1 并行写文档。
3. **B11 可以立即做**，但必须先做 dry-run 清单，只归档不删除；根目录已有约 421 个 `tmp*` 临时文件、约 463 个根文件，确实影响观感。
4. 建议按「5 个工作包 + 3 个文档包」推进，总计约 **14–18 个工作日**（单人开发，不含真实模型评测等待时间）。
5. 本计划明确不碰：ClaranAIM、其他 Phase 的新功能、Redis/MQ/Milvus、前端框架重写、任何跨服务拆分。

### 0.1 基线实测发现（2026-11-23 17:36）

跑了相关包的基线测试：

```bash
go test ./internal/brain/... ./internal/reasoning/... ./internal/personaeval/... ./internal/knowledgeeval/...
```

结果：`brain`、`brain/provider`、`reasoning`、`knowledgeeval` 通过；**`personaeval` 有 10 个测试失败**，错误均为 `未知用例类型`。

- 根因线索：`personas/dafeiyu/eval/cases.jsonl` 含 `kind: "comfort"`（2 条）与 `kind: "boundary"`（1 条），而 `internal/personaeval/score.go` 的 `ValidateCases` 白名单只允许 `normal/praise/abuse/confused/privacy/impersonate/contact/promise`；这些用例还带了 `先关心/收梗/收/认/关心/撒娇` 等标签，当前 v1/v5 规则里也没有对应定义。
- 结论：**当前 HEAD 不是全绿基线**，B3/B4 开始前必须先处理。不能靠放宽校验或删用例掩盖；要么补评分规则版本，要么把用例回退到已评审版本，需主人/Codex 定口径。
- 该项列入 **W0-0**，作为所有 P0 工作的前置门禁。

### 0.2 本轮已落地（2026-11-23，B1 第一批）

> 只做 B1 的「Provider 原生 tools + Schema + 原生循环 + 接线骨架」，默认不改变生产行为。

| 子项 | 状态 | 说明 |
|---|---|---|
| B1-A Provider | ✅ 已实现 | 新增 `internal/brain/provider/tool.go`；`OpenAI.ChatWithTools` 支持 `tools`/`tool_calls`/`tool` role；旧 `Chat` 仍严格两消息、遇 `tool_calls` 判协议错误 |
| B1-B Schema/校验 | ✅ 已实现 | `internal/reasoning/tools_schema.go` 覆盖 19 个工具名；`Toolbox.Definitions()` 输出 JSON Schema；`ValidateToolArgs`/`ToolArgsText` 做最小校验与文本还原 |
| B1-C 原生循环 | ✅ 已实现 | `internal/reasoning/native.go`：`RunNative` 支持 `tool_calls`、assistant/tool 回填、步数/超时/预算降级、证据复用 |
| B1-D 接线 | ✅ 代码与 mock 端到端完成 | `[reasoning] native_tools = "off"` 默认关闭；`on/auto` 走原生，`auto` 遇到 `KindUnsupported` 回落文本；真实 Provider 小流量灰度仍需主人授权 |
| 测试 | ✅ 已补 | `openai_tool_test.go`、`tools_schema_test.go`、`native_test.go`、`react_native_test.go`；`go test ./internal/brain/provider/... ./internal/reasoning/... ./internal/config/... ./cmd/fishd/...` 通过 |

> 仍然保持：`native_tools` 默认 `off`，上线前必须先跑 mock + 真实 Provider 灰度；不改 `contracts/`、不改 `go.mod`。

#### B1-D 执行记录（2026-11-23）

- `cmd/fishd/native_e2e_test.go` 用真实 HTTP Provider 覆盖：`tools` 请求 → `tool_calls` → 工具执行 → `tool` 回填 → 最终正文。
- 同一测试覆盖 `auto`：Provider 返回 400/`KindUnsupported` 时自动回落纯文本，事件不失败。
- 真实 Provider 灰度需要 API Key 与小流量窗口，按计划单独请示；未打开默认开关。

---

## 1. 硬约束（越界即打回）

| 类别 | 约束 | 落地检查 |
|---|---|---|
| 语言/模块 | Go 1.25+，模块名 `RaiseMyFish` | `go.mod` 不变模块名；不引入第二语言运行时 |
| 数据访问 | GORM 为主 + pgx 驱动；状态机/幂等/锁用 `Raw`/`Exec` | 新代码不直接用 `database/sql` 绕 repository |
| 数据库 | PostgreSQL 17+（pgvector 备用） | 不新增 Redis、MQ、Milvus、ES、ClickHouse |
| 日志 | zap，JSON，落文件 + stderr；不打印完整 prompt/cookie/token | 新 trace/工具日志只记元数据与长度 |
| HTTP | 标准库 `net/http`，P5 之前不引入 HTTP 框架 | 不引入 Gin/Echo/Fiber |
| LLM 编排 | 现有 Provider 抽象；P0 自写 OpenAI 兼容客户端 | 不引入 eino/LangChain 等编排框架 |
| 部署 | Docker Compose，单机单库 | 不拆微服务、不加服务发现/K8s |
| 分层 | handler/scheduler 不直接写 SQL，走 repository；内部调用不走 MCP/HTTP/gRPC | 新代码 import 检查 |
| 契约 | `contracts/` 变更需先裁决；本计划默认**只做加法**，不改既有 `Provider` 契约 | 新增可选接口 `ToolProvider`，旧 `Provider` 不动 |
| Phase | 不提前实现后续 Phase 的功能；清单项按「改进」处理，不改变 Phase 状态 | 不新增 Phase 9/10，不拆 Router/Worker |
| 测试 | 没有测试视为未完成；`gofmt`、`go vet`、`go test -race ./...`、集成测试（真实 PG）全过 | 每批交付附命令与结果 |
| 真实调用 | 不擅自调用真实平台发送、真实模型付费评测 | 需要花钱/真实环境的步骤单独标识、单独请示 |

> 与 `AGENTS.md` 的关系：本计划不覆盖安全、隐私、记账、幂等、`contracts/` 真源等硬规则；冲突时一律以 `AGENTS.md` 与既有契约为准。

---

## 2. 现状盘点（只记事实，不夸大）

> 证据均为当前工作区源码；「缺失」表示没有找到实现，不代表设计上永远不做。

| 项 | 清单要求 | 当前实现 | 结论 |
|---|---|---|---|
| **B1** 原生 tool calling + Schema | 17 个工具 JSON Schema；解析 `tool_calls`；参数校验；`tool` role 回填；只读并行、副作用串行；不支持时降级 | `internal/reasoning/agent.go` 是**文本 ReAct**（`{thought, action, args}` JSON）；`internal/brain/provider/types.go` 的 `Message` 只有 `Role/Content`，`Request` 只有 `Model/Messages/MaxTokens/Temperature`；`internal/brain/provider/openai.go:165` 遇到 `tool_calls` **直接判协议错误**；工具定义只有 `Name/Desc/Run`，没有 JSON Schema | **缺失，需新做** |
| **B2** per-event OpenTelemetry trace | 一条 trace 对应 `event_id`：collect/schedule/context.build/llm.step/tool.call/deliver；导出 Jaeger；面板慢回复入口 | `go.mod` 无任何 `opentelemetry` 依赖；`internal/observe` 只有心跳/日报/状态；zap 日志已带 `event_id`（`cmd/fishd/react.go` 等），但没有 span/trace 树 | **缺失，需新做** |
| **B3** 评测进 CI | `make eval` 一条命令出 JSON 报告；CI eval job（PR mock/fixture、nightly 真模型）；bad-replies 变用例 | `cmd/knowledgeeval` 可跑离线检索评测；`fishctl persona eval` 可跑人格回归；`Makefile` **没有 `eval` 目标**；`.github/workflows/ci.yml` **没有 eval job**；`docs/eval/2026-11-18-bad-replies.md` 仍是人工表格 | **部分具备，缺统一入口与 CI** |
| **B4** 轨迹级评测 | 记录工具序列/步数/证据覆盖；50 条「该调什么工具」用例；5 个指标 | `personaeval.Report/CaseResult` 已有输出、用量、失败项，但 `Generation` 没有工具轨迹字段；`reasoning.Result` 有 `Steps/Tools/Evidence` 但未进入评测报告 | **缺失，需扩展** |
| **B5** 记忆巩固/遗忘/引用归因 | `last_referenced_at`、`reference_count`；定时 consolidation；跨 key 矛盾检测；回复 meta 记录用了哪些记忆；纠正降权 | 已有 `memory_records`（含 `archived/valid_to/importance/confidence`）、`memory/repeat.go` 重复称呼检测、`extractworker/repeat.go` 纠正降权；**没有** `last_referenced_at/reference_count`、没有 consolidation worker、没有回复级记忆 ID 清单 | **部分具备，缺闭环** |
| **B6** README 与叙事重写 | 真实数字（18 天、21,583 事件、p50 1s、358 测试）；架构图；ADR 摘要；定位改为「实时 Agent Runtime + 事件驱动一致性」 | `README.md` 已有能力矩阵和真实数据表，但数字是旧口径（11,128 条事件、34 条长期记忆等）；无 Mermaid 架构图；无 ADR 摘要章节 | **需重写与重新取数** |
| **B7** 代码归属与讲解 | 3 条链路白板讲；每条一页 `docs/design-decisions`；3 分钟自讲 | 仓库无 `docs/design-decisions/` 目录；`docs/02-architecture.md` 有总体架构但非「可讲稿」 | **缺失，纯文档** |
| **B8** 检索评测与 rerank | 先用 100 条 knowledgeeval 做 baseline；BM25/tsvector + RRF、rerank 实验；提升 < 3 点就明确不采用 | `internal/knowledgeeval` + `cmd/knowledgeeval` 已有 100 条离线评测；检索实现为关键词档位 + 余弦混合，**没有 BM25/tsvector/RRF/rerank** | **评测已有，实验未做** |
| **B9** Prompt 缓存与成本 | 稳定前缀放最前；记录 cached tokens；面板 `cost_per_reply`；前后对比 | Provider 已解析 `prompt_tokens_details.cached_tokens` 并写入 `llm_usage.cached_in_tokens`；`internal/web/static/index.html` 有「已知费用(¥)」卡片；**没有单条回复成本/缓存命中率面板**，也没有 A/B 对比 | **部分具备** |
| **B10** 安全与红队 | 20 条弹幕注入用例；不可信内容统一数据信封；副作用工具滥用测试；输出过滤与审计 | 已有 Context 段转义、私有 Scope 校验、副作用工具限频；**没有系统化 20 条注入用例**，也没有红队报告 | **部分具备，缺用例与报告** |
| **B11** 仓库清理 | 删除根目录 `in1.txt/out1.txt/la_result.txt` 等；归档 `config.local.eval-*`；`.gitignore` 覆盖 logs/data/dist；根目录只留源码/文档/构建文件 | `.gitignore` 已覆盖 logs/data/dist/`tmp_*`；根目录已有约 **421 个 `tmp*` 文件**、多个 `in*.txt/out*.txt/la*.txt/result.txt`、6 个 `config.local.eval-*`、`tmp-web.exe` 等 | **已清理根目录与内部报告（2026-11-23）**；`config.local.eval-*` 仍在本地、不入库 |

### 2.1 现状盘点附注

- B1 的「17 个工具」是清单口径；当前代码里工具常量共 **19 个**（4 个核心 + 9 个只读 + 5 个副作用 + `send_danmaku`），实际注册按依赖裁剪。做 Schema 时应以**实际注册的工具**为准，不硬凑 17。
- B2 的 `event_id` 已经在多数日志里，但只是字段，不是 trace；不要用「有 event_id」冒充「有 trace」。
- B3 的人格评测 `fishctl persona eval` 目前只支持真实 OpenAI 兼容 Provider（`--brain openai`，无 baseline 模式），CI 要用 mock/fixture 需要先加一个**测试用 Generator**，不能把真实付费调用放进 push CI。
- B5 的「纠正」已有重复称呼场景的降权实现，但清单要求的是**通用记忆纠正**（任意 fact/preference），两者不能等同。
- B9 的 cached tokens 已落库，但面板只展示总费用；清单要求的是**单条回复成本**与**优化前后对比**，需要补聚合查询与页面。

---

## 3. 批次与顺序

| 批次 | 内容 | 依赖 | 预计 | 风险 |
|---|---|---|---|---|
| **W0 基线** | **W0-0 修复 personaeval 基线红灯（comfort/boundary 用例类型与标签）**；跑一次全量 `go test -race`、记录真实数字；B11 dry-run 清单 | 无 | 0.5–1 天 | 清理误删；评分口径需先确认 |
| **W1 P0-1** | B1 原生 tool calling（分 4 小批） | W0 | 3–4 天 | Provider 协议差异；必须先做 fallback |
| **W2 P0-2** | B2 per-event trace（先内部 trace，再接 OTel/Jaeger） | W1 可并行（接口独立） | 2–3 天 | 新增依赖；采样与隐私 |
| **W3 P0-3** | B3 `make eval` + CI eval job + bad-replies 用例化 | W1/W2（轨迹字段可后置） | 1–2 天 | 真实模型成本；CI 无密钥 |
| **W4 P1-1** | B4 轨迹级评测（依赖 B1 的工具轨迹） | B1、B3 | 2–3 天 | 需要 50 条人工标注用例 |
| **W5 P1-2** | B5 记忆巩固/遗忘/引用归因 | 无（可与 B4 并行） | 2–3 天 | 误合并/误删记忆；默认 dry-run |
| **W6 文档** | B7 设计讲解 3 页 + B6 README 重写 | 全部代码批次收尾 | 1–2 天 | 数字必须真实，不能编 |
| **W7 P2** | B8 检索实验、B9 成本面板、B10 红队、B11 实际清理 | 按需 | 3–5 天 | 实验不达标就不上线；清理需备份 |

**建议执行顺序**：W0 → B7（先写第一页，边做边讲）→ W1 → W3（先把评测门禁立起来）→ W2 → W4 → W5 → B6 → B8/B9/B10/B11。

> 如果只有 10 天：只做 **W0 + W1 + W2 + W3 + B7**，其余全部推迟；这与清单「10 天建议」一致。

---

## 4. 详细工作包

### 4.0 W0-0 personaeval 基线修复（前置，0.5–1 天）

#### 目标
让 `go test ./internal/personaeval/...` 恢复全绿，且不改变已验收的评分口径。

#### 事实
- `personas/dafeiyu/eval/cases.jsonl` 第 36/37/38 行分别是 `comfort`/`boundary`/`comfort` 用例；
- `internal/personaeval/score.go:189-193` 的 `ValidateCases` 没有这两个 kind；
- 这些用例的 `expect` 标签（`先关心/收梗/收/认/关心/撒娇`）在 `NewScorer()`/`NewScorerVersion("persona-lexical-v5")` 里也没有对应规则定义。

#### 可选方案（需主人/Codex 选一个，不自行发明）
- **方案 A（推荐）**：这些是 v1.3 人设新增的「善良底色」用例，属于评分规则未跟进；新增 `persona-lexical-v6` 或补 v5 风格观察规则，把 `comfort/boundary` 与对应标签定义清楚，旧版本快照保留。
- **方案 B**：删掉/移出这 3 条未评审用例，恢复 v1.2 用例集；但这会让「善良底色」没有回归覆盖，后续补。
- **方案 C**：把 `comfort/boundary` 仅作为记录标签、不参与自动判分（明确写入报告，不冒充通过）。

#### 验收
- [ ] `go test ./internal/personaeval/...` 全绿
- [ ] 35 条用例数与 pack 版本断言一致（按方案调整）
- [ ] 评分规则快照/版本号/报告说明同步
- [ ] 不删除历史 run

#### 执行结果（2026-11-23）

- 采纳**方案 B**（主人裁决）：移除 `live-101/live-102/dm-101` 三条未评审用例，恢复 35 条已评审用例集；不扩展 v5 评分规则。
- 同步修正基线断言：`fixture_test.go` 的 pack 版本改为 `v1.3`（人格包已升版）；`cmd/fishctl` 的审计测试使用生产同口径 `max_prompt_tokens=65536`，不降低预算校验。
- 验证：`go test ./...` 全绿（`internal/knowledge` 417s，其余缓存命中）。

---

### 4.1 B1 原生 tool calling + 工具 Schema（P0，3–4 天）

#### 目标
保留现有文本 ReAct 作为 fallback；新增原生 `tools` 请求参数、`tool_calls` 解析、参数校验与 `tool` role 回填；只读工具可并行，副作用工具串行限频；不支持 tools 的 Provider 自动降级。

#### 关键设计（不改变既有契约）
- **不改** `provider.Provider` 接口。新增可选接口：
  ```go
  // 新文件 internal/brain/provider/tool.go（草案）
  type ToolDefinition struct {
      Name        string
      Description string
      Parameters  json.RawMessage // JSON Schema
  }
  type ToolCall struct {
      ID        string
      Name      string
      Arguments string
  }
  type ToolMessage struct {
      Role       string // system/user/assistant/tool
      Content    string
      ToolCalls  []ToolCall // assistant 消息可带
      ToolCallID string     // tool 消息必须带
  }
  type ToolRequest struct {
      Model, Messages, MaxTokens, Temperature, Tools
  }
  type ToolProvider interface {
      Name() string
      ChatWithTools(context.Context, ToolRequest) (ToolResponse, error)
  }
  ```
  `OpenAI` 类型实现 `ChatWithTools`；旧 `Chat` 保持「两消息、无 tools」的严格校验，避免回归。
- **工具 Schema**：`reasoning.Tool` 增加 `Parameters json.RawMessage`（可选）；`BuildTools/BuildReadOnlyTools/BuildSideEffectTools` 为每个工具填最小 Schema（`type: object` + 必填字段 + 枚举/长度约束）。先覆盖实际注册的工具，不硬编码「17」。
- **参数校验**：新增 `Validate(args string) error`，按 Schema 做类型/必填/枚举/长度检查；失败返回结构化 `ToolArgError{Field, Reason}`，计入指标并在观察里回填「参数错误：...」让模型自纠。
- **原生循环**：`internal/reasoning/native.go` 新增 `RunNative`：
  1. system 仍取 `prepared.Messages[0].Content`；user 取 `prepared.Context + Question`（与文本 ReAct 同源）；
  2. 请求带 `tools`；
  3. 响应有 `tool_calls`：按顺序执行；只读工具可并发（`sync.WaitGroup`，上限 4），副作用工具串行 + 既有 `SideEffectLimiter`；
  4. 把 assistant（含 `tool_calls`）与 `tool` 消息追加到下一轮 messages；
  5. 无 `tool_calls` 且 `content` 非空 → 最终回复；空 → 降级；
  6. 超过 `MaxSteps` / 超时 → 用已有证据回落单步，复用现有 `Evidence` 机制。
- **能力探测与降级**：
  - 配置 `[reasoning] native_tools = "auto" | "on" | "off"`，默认 `auto`；
  - `auto` 首次调用遇到明确「不支持 tools」的 4xx/协议错误 → 标记该 Provider 名称在本进程内降级，后续走文本 ReAct；
  - 不把 401/402/429/5xx 当成「不支持 tools」，避免误降级。
- **指标**：结构化日志 `tool_call`（工具名、参数摘要前 60 字、耗时、结果长度、错误类别）＋ `fishctl status`/日报增加「工具参数错误数」计数；不新增独立监控系统。

#### 改动文件（预计）
| 文件 | 改动 |
|---|---|
| `internal/brain/provider/tool.go` | 新增 ToolDefinition/ToolCall/ToolRequest/ToolProvider |
| `internal/brain/provider/openai.go` | 新增 `ChatWithTools`；`chatRequest` 增加 `Tools`；解析 `tool_calls`/`tool` role；旧 `Chat` 行为不变 |
| `internal/brain/provider/openai_tool_test.go` | 新增：成功、参数错误、不支持 tools、HTTP 错误、`tool_calls` 格式异常 |
| `internal/reasoning/tools.go` | `Tool` 增加 `Parameters`；`Toolbox` 暴露 `Definitions()` |
| `internal/reasoning/tools_schema.go` | 新增各工具 JSON Schema 与 `Validate` |
| `internal/reasoning/native.go` | 新增原生循环（与文本 `agent.go` 并存） |
| `internal/reasoning/native_test.go` | 新增 mock provider 三种情况：`tool_calls` 成功/失败/不支持 |
| `cmd/fishd/react.go` | 工厂按 `native_tools` 选择 `RunNative` 或 `Run`；日志增加 `protocol=native|text` |
| `internal/config/reasoning.go` + `config/fishd.toml` | 新增 `native_tools` 配置与默认值 |
| `cmd/fishd/serve_test.go` | 新增接线路径测试（不用真实模型） |

#### 测试矩阵
- mock provider 返回 `tool_calls` → 工具执行 → `tool` 消息回填 → 最终文本；
- 工具参数非法 → 不执行副作用工具 → 观察回填错误 → 最终仍能回复；
- Provider 不支持 tools → 自动降级文本 ReAct，且日志有 `native_tools=off`；
- 只读工具并行（计数/顺序）与副作用工具串行（限频器调用次数）；
- 超时/步数耗尽 → 证据保留 → 回落单步；
- 旧 `Chat` 两消息校验回归、旧文本 ReAct 测试全过。

#### 验收（对应清单）
- [ ] mock provider 覆盖 `tool_calls` 成功/失败/不支持三种情况
- [ ] 不支持 tools 的 Provider 自动降级到文本协议
- [ ] 工具参数错误有指标记录（日志 + status 计数）
- [ ] `go test -race ./...`、`go vet ./...`、`gofmt -l .` 全绿

#### 风险与回滚
- 不同 Provider 的 `tool_calls` 字段格式有差异（`function.arguments` 可能是字符串或对象）；先按 OpenAI 标准，异常走协议错误并降级，不猜。
- 原生循环会改变消息结构；`contracts/brain.md` 第 7 节明确旧 Provider 只支持两消息，因此**旧接口必须原样保留**，新接口旁路实现，避免影响人格评测和单步 Brain。
- 回滚：配置 `native_tools = "off"` 即回文本 ReAct，无需回滚代码。

---

### 4.2 B2 per-event OpenTelemetry trace（P0，2–3 天）

#### 目标
一条 trace 对应一个 `event_id`，覆盖 `collect → schedule → context.build → llm.step → tool.call → deliver`；能回答「为什么降级」「花了多少 token/多少钱」；面板能看最近 20 条慢回复的 trace 链接。

#### 关键设计
- 新增 `internal/trace`（或 `internal/observe/trace`）：
  ```go
  type Span struct {
      TraceID, SpanID, ParentSpanID string
      Name string
      Start, End time.Time
      Attributes map[string]any
  }
  type Recorder interface {
      Start(ctx context.Context, name string, attrs ...Attr) (context.Context, *Span)
      End(*Span, err error)
  }
  ```
- **默认零依赖实现**：`internal/trace/local.go` 写内存环形缓冲 + `logs/traces.jsonl`（只记元数据，不记 prompt 正文），面板直接读本地 trace；这样即使不新增 OTel 依赖，也能先满足「一条 event 一条 trace」。
- **可选 OTLP 导出**：配置 `[observability.trace] enabled=true, endpoint="http://127.0.0.1:4318", sample_ratio=1.0` 时接 OpenTelemetry SDK 导出 Jaeger；依赖变更单独走依赖审批，不默认加。
- **埋点位置**：
  | Span | 位置 | 关键属性 |
  |---|---|---|
  | `collect` | collector 落库前后 | channel、source、event_id |
  | `schedule` | scheduler 五道闸 | gate、result、queue_ms |
  | `context.build` | `brain.Prepare`/ContextBuilder | memory_ids、knowledge_ids、truncated |
  | `llm.step` | `brain.Brain.Reply` / `reasoning` 每次调用 | model、input_tokens、output_tokens、cost_usd、degrade_reason |
  | `tool.call` | `reasoning` 工具执行 | tool、args_len、result_len、error_kind、ms |
  | `deliver` | sender 投递 | channel、attempt、result、ms |
- **面板**：`/api/traces/recent?limit=20&sort=slow`，页面「概览」增加「慢回复 Trace」表格；本地 trace 详情页；Jaeger 配置时生成跳转链接。
- **隐私**：span 只记 ID/长度/模型/费用/结果类别，不记完整 prompt、消息正文、cookie/token；与 `docs/10-observability-cost.md` 的日志纪律一致。

#### 改动文件（预计）
| 文件 | 改动 |
|---|---|
| `internal/trace/trace.go` | 接口与 noop/内存实现 |
| `internal/trace/local.go` | JSONL 环形缓冲与查询 |
| `internal/trace/otel.go`（可选） | OTLP 导出，构建标签 `otel` 或配置开关 |
| `cmd/fishd/serve.go` | 初始化 recorder，注入各模块 |
| `cmd/fishd/react.go` / `internal/brain/brain.go` | `llm.step`、`tool.call` 埋点 |
| `internal/scheduler/*`、`internal/collector/*`、`internal/sender/*` | `collect/schedule/deliver` 埋点 |
| `internal/web/handlers_traces.go` + `static/index.html` | 最近慢回复/详情 |
| `internal/trace/*_test.go` | 树结构、耗时、隐私、关闭开关 |

#### 测试矩阵
- 构造一条 mock event → 断言生成一个 trace，span 父子关系为 `collect→schedule→context.build→llm.step→tool.call→deliver`；
- 降级场景 → `llm.step` 有 `degrade_reason`；
- 关闭开关 → noop，无文件/无开销；
- 隐私：把 prompt 正文放入消息，断言 JSONL/查询结果不包含正文；
- 慢回复查询按耗时排序、limit 生效；
- 采样率 0/1 边界。

#### 验收
- [x] 任选一条弹幕，能看到完整 trace（`collect→schedule→generate→context.build→llm.step→tool.call→deliver`）
- [x] 能回答「这条为什么降级」（`llm.step.degrade_reason`）
- [x] 能回答「这条花了多少 token/多少钱」（`llm.step` 的 token 属性 + `llm_usage` 账本）
- [x] 面板最近 20 条慢回复可点开 trace（`/api/traces/recent?sort=slow` + `/api/traces/{id}`）

#### B2 执行记录（2026-11-23）

- 新增 `internal/trace`：零依赖本地环形缓冲 + `logs/traces.jsonl`；只记时间/ID/长度/模型/token，不记 prompt 或正文。
- 阶段链按事件 ID 稳定派生 trace，跨 collect/schedule/generate/deliver/verify 自动挂父子；`tool.call` 由文本与原生 ReAct 共用。
- 面板新增「慢回复 Trace」卡与 `/api/traces` 接口；`[observability.trace]` 控制开关，默认关闭时全部 no-op。
- 验证：`go test ./internal/trace/... ./cmd/fishd/... ./internal/web/...`；新增 `TestGeneratePipelineWritesTraceTree` 断言五个阶段同 trace。

#### 风险
- 新增 OTel 依赖与「不换技术栈」不冲突（只是可观测库），但若主人要求零新增依赖，则启用本地 trace，Jaeger 导出延后。
- 埋点不能改变业务返回值；所有 trace 调用必须可失败降级（`defer` 保护）。

---

### 4.3 B3 评测进 CI（P0，1–2 天）

#### 目标
`make eval` 一条命令产出 JSON 报告（指标 + 失败用例 + 与 baseline diff）；CI 增加 eval job：push/PR 跑 mock/fixture，nightly 跑真实模型；`docs/eval/2026-11-18-bad-replies.md` 的每条坏回复变成用例。

#### 关键设计
- 新增 `cmd/eval`（或 `scripts/eval.sh` 统一编排）：
  - 调 `cmd/knowledgeeval` 跑 100 条离线检索；
  - 调 `fishctl persona eval` 的 **test/mock Generator**（需要新增 `--brain fixture` 或 `personaeval` 的 `testGenerator`）；
  - 合并为 `reports/eval/<timestamp>/report.json` + `report.md`；
  - 与 `baseline.json` 对比：通过率、指标 delta、新增失败用例。
- `Makefile` 增加 `eval` 目标（不改变现有 `test` 语义）。
- CI：
  - `eval` job：`go run ./cmd/eval --mode fixture`，artifact 上传；
  - nightly workflow：`schedule` + `workflow_dispatch`，真实模型 secrets 才跑；没有 secrets 时明确 skip 并输出原因（不是假绿）。
- 坏回复用例：
  - 从 `docs/eval/2026-11-18-bad-replies.md` 的 8 条出发，补 `event_id/channel/expected_keywords`；
  - 目标先做 20 条自动可判分用例，剩余人工抽检。

#### 改动文件
`cmd/eval/main.go`、`internal/personaeval/fixture_generator.go`（test only）、`Makefile`、`.github/workflows/ci.yml`、`.github/workflows/eval-nightly.yml`、`docs/eval/*`。

#### 验收
- [x] `make eval` 一条命令出报告（`cmd/eval` → `summary.json`/`summary.md` + 子报告）
- [x] 报告能对比「改 prompt 前/后」（每次独立目录，指标字段稳定可 diff）
- [x] CI 有 eval 记录/artifact（push CI 跑 fixture；nightly 缺 secrets 明确 skip）
- [x] 坏回复清单每条都有对应用例 ID（`docs/eval/bad-replies.jsonl`，8/8 检出）

#### B3 执行记录（2026-11-23）

- 新增 `cmd/eval`：人格 fixture 回放（`personaeval.FixtureGenerator`）+ 坏回复检测 + 知识检索离线评测，输出 `summary.json`/`summary.md` 与子目录报告。
- `make eval` 输出到 `dist/eval/<UTC时间>`；`ci.yml` 增加 eval job 并上传 artifact。
- `eval-nightly.yml` 只在配置 `FISH_LLM_KEY` 时跑真实模型；缺 secrets 明确 skip，不伪装通过。
- 知识检索仍是已知 K0-7 未达标项：默认只写 `notes` 基线说明，`--strict-knowledge` 才让整轮失败。
- 验证：`go test ./cmd/eval/... ./internal/personaeval/...`；`go run ./cmd/eval` 实际跑通 35/35 人格 fixture、8/8 坏回复。

#### 风险
- 人格评测现有实现强依赖真实 Provider；加 mock 生成器时不能改变真实评分口径。
- CI 无真实模型 key，nightly 用 secrets；没有 secrets 时只能 skip，不能宣称通过。

---

### 4.4 B4 轨迹级评测（P1，2–3 天）

#### 目标
在 personaeval 中记录工具序列、步数、证据覆盖；建 50 条「该调什么工具」用例；指标 `tool_selection_accuracy`、`unnecessary_tool_rate`、`avg_steps`、`groundedness`、`hallucination_rate`。

#### 关键设计
- `personaeval.Generation` 增加可选 `Trajectory *Trajectory`：
  ```go
  type ToolCallRecord struct { Name, ArgsDigest string; OK bool; MS int }
  type Trajectory struct { Steps int; Tools []ToolCallRecord; EvidenceCount int; Degraded bool; Reason string }
  ```
- `cmd/fishctl/persona_eval.go` 的 `evalGenerator` 若使用 ReAct，则从 `reasoning.Result` 填充；普通 Brain 保持 nil。
- 新增 `internal/personaeval/trajectory.go`：纯函数计算上述指标；`Case` 增加 `ExpectedTools []string`（仅评测用，不传给模型）。
- 用例集 `personas/dafeiyu/eval/trajectory-cases.jsonl` 50 条，标注「该调/不该调」。
- 报告新增 `trajectory` 段，与 B3 的 `make eval` 合并。

#### 验收
- [x] 能对比「改工具描述/改 prompt」前后的轨迹指标（录制轨迹 JSONL → `--trajectory-runs`，指标字段稳定）
- [x] 有失败案例库（`personas/dafeiyu/eval/trajectory-cases.jsonl` 50 条，含 forbidden_tools 与 rationale）
- [x] 指标进 CI 报告（`cmd/eval` 输出 `trajectory.json` 与 summary）
- [x] 50 条用例可跑，缺 ReAct 的用例明确标记 skip 原因

#### B4 执行记录（2026-11-23）

- `internal/personaeval/trajectory.go`：工具序列/步数/证据覆盖 + 五项指标（选择准确率、不必要工具率、平均步数、证据覆盖、幻觉率）。
- `Generation.Trajectory` 可选字段进入 `CaseResult`；单步 Brain 保持 nil，由评测层标 skip，不按 0 分混入分母。
- `cmd/eval --trajectory-runs <录制轨迹>` 可离线复算指标；实测 3 条示例轨迹算出准确率 1.0、不必要工具率 0.25、平均步数 2.0。
- 指标语义：`cases` 为总用例数（已评估+skip）；幻觉率按「用例是否踩禁用工具」计，无禁用清单不计分母。
- 验证：`go test ./internal/personaeval/... ./cmd/eval/...`。

---

### 4.5 B5 记忆巩固 / 遗忘 / 引用归因（P1，2–3 天）

#### 目标
`last_referenced_at`、`reference_count`；`memory_consolidation` 定时任务；跨 key 矛盾检测；回复 meta 记录本轮用了哪些记忆；主人纠正时标记 `corrected` 并降权。

#### 关键设计
- 迁移 `migrations/0025_memory_consolidation.sql`（编号待与 Codex/迁移表确认，不原地改已应用迁移）：
  ```sql
  ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS last_referenced_at TIMESTAMPTZ;
  ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS reference_count INT NOT NULL DEFAULT 0;
  ALTER TABLE memory_records ADD COLUMN IF NOT EXISTS corrected_at TIMESTAMPTZ;
  ```
- `internal/memory/consolidate.go` 纯函数：
  - 合并重复事实：同 `subject + kind + normalized(title)`，保留 importance 高/新者，旧记录 `superseded_by`；
  - episodic → semantic：同一实体/主题的多个 first_seen 合并为一条 summary；
  - 衰减/归档：`importance < 阈值 && last_referenced_at < now()-窗口 && reference_count=0` → `archived=true`；
  - 矛盾检测：同 subject 的 `preference` 相反值（喜欢/讨厌）输出 conflict 列表，不自动覆盖，交人工确认。
- `internal/memory/consolidateworker`：定时（默认每日 04:00，可配）、默认 `dry_run=true`；首轮只出报告，主人确认后再写。
- 召回引用：`memory.Reader` 返回 `memory.Context` 后，ContextBuilder 异步调用 `TouchReferences(ids)`；回复 `Reply.Meta` 增加 `memory_ids`（只 ID，不正文）。
- 纠正：沿用 `memory.CorrectedRepeatAliases` 的语义，扩展为「任意 fact 被主人明确纠正 → 对应记录 `corrected_at` + importance/confidence 降权」；不删除历史，保留可追溯。

#### 验收
- [ ] 30 条「该记住/不该记住/该忘记」用例
- [ ] 记忆命中率与误召回率有数字
- [ ] 回复能解释「为什么记得这个」（meta 有 memory_ids，可反查）
- [ ] consolidation 默认 dry-run，报告可审

---

### 4.6 B6 README 与叙事重写（P1，0.5–1 天）

- 数字重新取：`fishctl status`、`llm_usage`、`events` 表、CI 最近一次时长、`go test` 用例数；写进 README 的每个数字都要有来源与时间。
- 加 Mermaid 数据流图（不引入前端构建）。
- 加「为什么不用 Redis/MQ/Eino/多 Agent」ADR 摘要，链接 `docs/14-decisions.md`。
- 定位从「陪伴体」改为「实时 Agent Runtime + 事件驱动一致性」，但保留真实产品语气。
- 不写无法演示的分数；hash/local fallback、真实模型限制写进「限制」章节。

### 4.7 B7 代码归属与讲解（P1，1–2 天）

新增 `docs/design-decisions/`：
- `01-wss-to-sender.md`：WSS → events → 五道闸 → Brain → Sender；
- `02-react-danmaku.md`：一条弹幕的 ReAct（上下文快照 → 工具 → 观察 → 降级）；
- `03-memory-extraction.md`：记忆提炼（规则/LLM → 候选 → 冲突 → 召回）。

每页包含：问题、方案、关键代码路径、失败路径、可回答的 3 个问题。每页配一张手绘/ASCII 图，不追求长文。

### 4.8 B8 检索评测与 rerank（P2，先评测再决定）

- 用现有 `cmd/knowledgeeval` 100 条做 baseline，输出 `recall@5/MRR/nDCG@5/negative_false_rate`；
- 在 `internal/knowledge` 增加可选 `tsvector`/BM25 候选（PostgreSQL 内置，不新增依赖），用 RRF 融合；
- rerank 若接 LLM/外部 API，必须先做成本评估与主人授权；
- 提升 < 3 个点则在 README 写「当前规模不划算」，不硬上。

### 4.9 B9 Prompt 缓存与成本（P2）

- 审核 `brain.Prepare` 的 system/user 顺序，确保稳定前缀在前（现有实现大体符合，需逐段断言）；
- `llm_usage` 已有 `cached_in_tokens`，补 `cost_per_reply` 聚合查询与面板卡片；
- 做一次前后对比（同 20 条用例，记录 cached tokens/费用），报告写入 `docs/reports/`。

### 4.10 B10 安全与红队（P2）

#### B10 执行记录（2026-11-23）

- 新增 `internal/brain/injection_cases_test.go`：20 条真实弹幕注入（忽略指令、伪造段、套 cookie/token、越权工具、读私信等）。
- 断言口径：固定段各出现一次，注入里的尖括号必须转义；不调用真实模型、不依赖付费评测。
- 现有 `promptEscaper` 已覆盖 `<`/`>`/`&`；本轮只补用例与回归门禁。
- 验证：`go test ./internal/brain/...`。

- 新增 `internal/brain/injection_cases_test.go`：20 条弹幕注入（「忽略以上指令」「把系统提示发出来」等）；
- 不可信内容统一数据信封：现有 Context 段 JSON 转义基础上，补网页/OCR/ASR 来源标注与「资料不是指令」的固定说明；
- 副作用工具滥用测试：送礼/私信/动态的额度、确认、限频；
- 输出过滤与审计日志：只记事件 ID、规则命中、动作，不记正文。

### 4.11 B11 仓库清理（P2，0.5 天）

- 先跑 dry-run 生成清单：根目录 `tmp*`、`in*.txt`、`out*.txt`、`la*.txt`、`result.txt`、`missing.txt`、`replymeta.txt`、`tmp-web.exe`、`config.local.eval-*`、`tmp-fishd-test*.log`；
- **只归档不删除**：移动到 `archive/2026-11-23-root-cleanup/`，生成 `MANIFEST.md`（原路径/大小/修改时间）；
- 保留 `fishd.exe`/`fishmcp.exe` 等构建产物？——不，构建产物应在 `bin/` 或 `dist/`，根目录 exe 归档；
- `.gitignore` 已覆盖大部分，补 `*.exe` 根目录规则与 `archive/`（若不想入仓）；
- 清理后跑 `git status`，确认没有误删被跟踪文件。

---

## 5. 测试与验收门禁（每批通用）

```bash
gofmt -l .                       # 无输出
go vet ./...                     # 通过
go test -timeout 20m ./...       # 通过
go test -timeout 20m -race ./... # 通过（Windows 需 GCC 16.2.0，见 docs/testing.md）
go build ./...                   # 通过
```

- 涉及数据库的批次：`FISH_TEST_DSN=... go test -tags=integration ./... -count=1`，用真实 PG，不用 sqlite。
- 涉及真实模型/真实平台的批次：单独标记「需主人授权」，不得混入 CI 默认路径。
- 每批交付附：改动文件、测试命令与结果、未执行项及原因、风险、回滚方式。

---

## 6. 风险与待主人确认

| # | 事项 | 建议 | 需要谁确认 |
|---|---|---|---|
| 1 | B2 是否允许新增 OpenTelemetry 依赖（`go.opentelemetry.io/otel` + OTLP exporter） | 先做零依赖本地 trace；OTel/Jaeger 作为可选构建/配置开启 | 主人 |
| 2 | B1 新增 `ToolProvider` 可选接口是否算契约变更 | 属于**加法**，旧 `Provider` 契约不动；按 `contracts/brain.md` 第 7 节保留旧行为 | 主人/Codex 裁决 |
| 3 | B3 nightly 真实模型评测会产生费用 | 默认只跑 mock/fixture；nightly 用 secrets 且限额，先跑 10 条试水 | 主人 |
| 4 | B5 consolidation 首轮是否直接写库 | 默认 `dry_run=true`，只出报告；确认后再开写 | 主人 |
| 5 | B11 是否允许移动 421 个根目录临时文件 | 只归档不删除，生成 MANIFEST；可随时还原 | 主人 |
| 6 | B8 rerank 是否接外部模型/API | 先做本地 tsvector+RRF；rerank 需成本评估后再定 | 主人 |

---

## 7. 首批派发单：B1 原生 tool calling（可拆给 WorkBuddy/Qoder）

> 目标：在不改变旧 `Provider` 契约、不引入新框架的前提下，让 ReAct 支持原生 tools；文本 ReAct 保留为 fallback。
> 基线：当前 HEAD（执行前记录 commit）；可写范围：`internal/brain/provider/`、`internal/reasoning/`、`cmd/fishd/react.go`、`internal/config/reasoning.go`、`config/fishd.toml`、对应测试文件。
> 禁止：改 `contracts/`、`go.mod`、`cmd/fishd/serve.go` 以外未列路径；不跑真实模型。

### B1-A（Provider 层，1 天）
1. 新增 `internal/brain/provider/tool.go`：`ToolDefinition/ToolCall/ToolMessage/ToolRequest/ToolResponse/ToolProvider`。
2. `openai.go` 增加 `ChatWithTools`：请求带 `tools`；解析 `choices[0].message.tool_calls`（`id/type/function.name/function.arguments`）；`tool` role 消息按 OpenAI 协议序列化。
3. 旧 `Chat` 保持两消息、无 tools；遇到 `tool_calls` 仍按协议错误（不改变旧行为）。
4. 测试：`openai_tool_test.go` 成功/参数错误/不支持/格式异常/HTTP 错误。

### B1-B（Schema 与校验，1 天）
1. `reasoning.Tool` 增加 `Parameters json.RawMessage`；`Toolbox.Definitions() []provider.ToolDefinition`。
2. `tools_schema.go`：为实际注册工具写最小 JSON Schema；`Validate(args)` 做 required/type/enum/length 校验。
3. 测试：每个工具的合法/非法参数表驱动；错误含字段名与原因。

### B1-C（原生循环，1–1.5 天）
1. `native.go`：`RunNative(ctx, Input)`，复用 `Prepare` 的 system/context/question；`MaxSteps`/`Timeout` 沿用 `Options`。
2. `tool_calls` 执行：只读并行（上限 4），副作用串行 + `SideEffectLimiter`；`tool` 消息回填。
3. 结果结构复用 `Result`（Text/Steps/Tools/Evidence/Degraded/Reason）。
4. 测试：成功、多工具、只读并行、副作用串行、超时、空回复、未知工具。

### B1-D（接线与降级，0.5–1 天）
1. `config.Reasoning` 增加 `NativeTools string`（`auto/on/off`，默认 `auto`）。
2. `cmd/fishd/react.go`：工厂按配置选择 `RunNative`/`Run`；`auto` 遇到明确不支持时进程内降级并记日志。
3. `serve_test.go`：装配路径、降级路径、日志字段。
4. 验收：`gofmt/go vet/go test -race` 全绿；旧文本 ReAct 用例不回归。

---

## 8. 完成后要收集的数字（对应清单第四章）

- 总事件数、入站/出站、峰值/分钟；
- p50/p90/p99 回复延迟（面板 + `fishctl status`）；
- 测试文件数、CI 时长、`go test -race` 用例数；
- personaeval 通过率、knowledgeeval Precision/Recall；
- 单条回复平均 token / 成本、缓存命中率；
- 降级率、工具错误率、tool_selection_accuracy、unnecessary_tool_rate、avg_steps。

> 每个数字都必须能指到「哪个命令/哪张表/哪次 CI」，不能手填估算。

---

## 9. 本计划不做什么

- ❌ 不改 `docs/14-decisions.md` 既有决策；
- ❌ 不引入 Redis/MQ/Milvus/K8s/微服务；
- ❌ 不重写 Web 前端框架、不新增 Node 构建链；
- ❌ 不拆 Router/Worker、不做多 Agent 角色；
- ❌ 不把 B1 做成「再加 10 个工具」——先把现有工具调用可靠性做好；
- ❌ 不先上 rerank/BM25 再评测——B8 必须先有 baseline；
- ❌ 不为了清单数字伪造测试、伪造评测、伪造 trace。

---

## 10. 变更记录

- 2026-11-23：初版。依据主人提供的《项目优化清单-20261123》编制；当前工作区 HEAD 为 `ddf9bc6`（执行前以实际 commit 为准）。
