# DSH 回执与验收

> 模板由 Codex 初始化，后续仅 DSH 追加。DSH 独立验收并协助主人调度会话。

## COMM-001 回执

- 已读状态：**已读**（DSH，本鱼）。
- 已读时间（Asia/Shanghai）、验收会话：2026-10-31 15:20，本会话。
- 是否接手看板、Phase 状态和旧协作文案同步：**已接手**。以 `plans/coordination.md` 为准；
  `agents/` 留言板降级为辅助登记，不再作为权威通道。
- 是否转达 WorkBuddy、对方是否确认：**待转达**（转达文本由 Codex 提供，本鱼执行）。
- 旧任务/写入会话排查与暂停结果：DSH 已暂停 Git 写操作（add/commit/push 归 Codex）；
  旧分工下向 WorkBuddy 派发整模块的文案已停止。

## 每次验收模板（追加）

### 2026-10-31 15:20 · 观察登记（非正式交付，待裁决）

- 任务/子任务、开发者、验收快照/提交号、覆盖路径：
  WorkBuddy 提前写入 `internal/persona/meta.go`、`internal/persona/pack.go`（未跟踪、未提交）；
  看板中 `0.7b 加载器` 状态为**阻塞**（等 0.1 验收、Pack 接口基线确认）。
- 独立执行的检查/命令/结果：仅代码审查（仓库暂无 `go.mod`，无法编译）；
  **未执行** `go test` / `go vet`，不构成验收通过。
- 未验证项与原因；是否涉及真实 PG / 模型 / 通道：编译、单测、契约映射均未验证；缺 go.mod 与 PG。
- 结论：**待 Codex 裁决**（接受为 0.7b 提前产物 / 退回待派发），不据此标任务完成。
- 退回项、需 Codex 裁决的问题：
  1. `KnownChannels` 与 `contracts/event.md` 的 Channel 映射需在 Pack 接口冻结时确认；
  2. `pack.go` 的篇幅门槛属产品规则，是否写死由 Codex 定；
  3. `agents/` 留言板与 `plans/reports/` 重叠，保留/合并/删除由 Codex 定。
- 看板/Phase 状态是否更新，写入者是否暂停：未更新（未正式派发）；WorkBuddy 写入状态未知。
- 会话转达与回执状态：待转达 WorkBuddy。

### 2026-10-31 15:20 · 仓库卫生观察

- `.probe.txt` 未被忽略（内容为 `.bili-ai` 目录探针），不应入仓。
- DSH 已执行 `git rm --cached .probe.txt` 移出索引；文件仍在工作区。
- 建议：删除文件 + `.gitignore` 增加 `.probe.txt`（由 Codex 提交）。

### 2026-10-31 15:25 · 主人指令：Git 归 Codex

- 主人明确：commit/push 全部交给 Codex，WorkBuddy 只写代码、不得乱 commit。
- 已核查：`git log` 无 WorkBuddy 的提交；工作区只有未跟踪的 `internal/persona/`、`personas/dafeiyu/eval/` 和待提交的文档改动。
- 已同步到：`plans/assignment.md`（Git 唯一归 Codex）、`prompts/workbuddy-phase0.md`（不执行 git 命令）、`agents/PROTOCOL.md`（Git 规则置顶）。
- DSH 自身也不再执行 Git 写操作；所有改动留在工作区，由 Codex 暂存与提交。

### 2026-10-31 15:20 · 环境阻塞

- PostgreSQL 未安装/未运行（5432 超时）→ 0.1 迁移验证、0.2 集成测试阻塞。
- `golangci-lint` 不在 PATH → 本地用 `go vet`；CI 的 lint 需确认可用。

### 2026-10-31 15:30 · 0.7a 独立验收

- 任务/子任务：0.7a 人格素材与用例，WorkBuddy，未提交（工作区未跟踪）
- 覆盖路径：`personas/dafeiyu/`（meta/soul/rules/samples/context/eval）
- 独立执行的检查（不抄自测）：
  - `gofmt -l` 无输出
  - `go vet ./...` 通过
  - `go test ./internal/persona/` **PASS**（临时目录建 `module RaiseMyFish` + 复制 personas 后复现）
  - 用例矩阵脚本核对：35 条，channel live 8 / comment 6 / dm 7 / qq 14；
    kind normal 12 / praise 5 / abuse 5 / confused 5 / privacy 8；expect/forbid 无空
  - `meta.yaml` channels 与 `context.md` 五段（live/comment/dm/qq_private/qq_group）逐一对齐
- 未验证项：真实模型通过率（等 0.6）、真实 PG（属 0.2）、golangci-lint（本机未装）
- 结论：**0.7a 验收通过**（待 Codex 提交）
- 待 Codex 裁决：V-01 `internal/persona/` 越界去留；V-04 真实通过率；V-05 yaml 依赖
- 已修复：`docs/08-persona.md` 的 persona-qq.md 路径（改为 `~/.dsh/persona-qq.md`）；
  `.gitignore` 新增 `.workbuddy/`（WorkBuddy 记忆目录）与 `.probe.txt`
- 看板：0.7a 已更新为「验收通过（待 Codex 提交）」；0.7b 仍阻塞

### 2026-10-31 15:35 · COMM-003 接收与看板更新

- 核实提交：`32651ba` 在 master；`origin/master` 同为 `32651ba`（已推送）。
- 核实范围：提交统计不含 `internal/persona/`（7 份 Go 草稿未提交，符合 V-01）。
- 裁决确认：
  - V-01：Go 草稿保留为未接入预研，0.1 验收 + Pack/Brain 接口确认后由 Codex 逐文件评审
  - V-03：运行时事实进 Memory.SharedState/最小记忆，频率/额度进配置
  - V-04：真实人格通过率等 0.6 后重跑；baseline 回放不计
  - V-05：0.7b 采用 `go.yaml.in/yaml/v3`，由 Codex 在 go.mod 就绪时加入
- 看板：0.7a 改为「✅ 已提交 32651ba（master）」。
- 残留未提交（留给 Codex）：`agents/PROTOCOL.md`、`plans/assignment.md`、`prompts/*` 的文档修订；
  `internal/` 为 V-01 待审草稿，不入库。
- 下一步：0.1 待派发；0.7b/0.7c 仍阻塞。WorkBuddy 保持暂停。

## COMM-004 回执 · 0.1 独立验收（2026-10-31 16:25）

- 快照核验：`plans/reports/phase-0.1-manifest.json` **31/31** 文件哈希与字节数全部匹配。
- 独立执行（不抄自测）：
  - `go build ./...` 通过；`go vet ./...` 通过；`gofmt -l` 无输出
  - `go test ./... -count=1` 全部通过（6 个包）
  - `go test -tags=integration ./... -count=1` 全部通过（真实 PG）
  - `go run ./cmd/fishd migrate up` 连续两次：版本 1 就绪，幂等无副作用
  - 目标库：8 张表（7 业务 + `schema_migrations`）；`memory_sources` 6 行；迁移账本 checksum 存在
  - 凭据脱敏：代码/toml/md/json 无明文密码；`logs/fishd-*.log` 无凭据
  - 运行验证：fishd 启动成功；`fishctl status` 返回健康 JSON；第二个实例被单实例锁拒绝；`fishctl tail` 正常跟随
- 结论：**0.1 独立验收通过**（待 Codex 提交）。
- 观察项（请 Codex 裁决）：`taskkill /F` 强杀后 `fishctl status` 仍显示 `running`（状态文件未校验 PID 存活）。
  优雅退出路径（Codex 报告的 Ctrl+C）未在本次复现；强杀残留是否需要在 status 中加存活校验，由 Codex 定。
- 未验证项：Linux `-race`（本机未复现）、GitHub CI 远端未跑、本机 `golangci-lint` 未安装。
- 看板：0.1 → 「✅ 验收通过（待 Codex 提交）」。

## COMM-005 回执 · 0.1 提交与 0.7b 放行（2026-10-31 16:45）

- 提交核实：
  - 0.1 = `364dcb3`（master），`origin/master` 已同步。
  - Pack 基线 = `42a823f`，含 `contracts/persona.md`（P0-PACK-1）与 `plans/phase-0.7b-dispatch.md`。
- 看板已由 Codex 更新：0.1「已提交并推送 364dcb3」、0.7b「已放行，待本人接手回执」。
- 放行单范围确认（DSH 依 `contracts/persona.md` 验收）：
  - 唯一开发范围：`internal/persona/{meta.go,pack.go,pack_test.go}` + 可新增 `meta_test.go/context_test.go`
  - `personas/dafeiyu/meta.yaml` 仅移除过时注释
  - 旧 4 个草稿（check/evalcase/runner/eval_test）移入 `.workbuddy/persona-eval-draft-comm-005/`，保留哈希
  - 不改 go.mod/go.sum、cmd、database、迁移、契约、看板、他人报告
- 待转达 WorkBuddy：读 `plans/phase-0.7b-dispatch.md` + `contracts/persona.md`，写 COMM-005 回执；不执行 Git 写操作。
- 0.7b 交付后由 DSH 独立验收（按契约核对类型/方法/错误语义），不抄开发者自测。

## 0.7b 独立验收（2026-10-31 17:00）

- 快照：工作区未提交；`internal/persona/` 仅 5 个文件（meta/pack/pack_test/meta_test/context_test）。
- 契约逐条核对（`contracts/persona.md` P0-PACK-1）：
  - 五个 Context 常量、`Meta` 六字段 + yaml tag、`Pack` 六字段：一致
  - `Load(ctx, dir) (Pack, error)` 签名一致；`ContextKeys` 每次新 slice（已抽验）
  - `ContextKey`：live/comment/dm 忽略 private，qq 按 private 二选一（switch 实现，无全局可变 slice）
  - `SystemPrompt`：`TrimSpace(Soul)+"\n\n"+TrimSpace(Rules)+"\n\n"+TrimSpace(Samples)`，顺序正确
  - 禁导出符号 `Assemble/Generator/RunEvalCLI/KnownChannels`：grep 无
  - `meta.yaml` diff：只删解析器限制注释，字段与内容未变
  - 旧 4 草稿已移入 `.workbuddy/persona-eval-draft-comm-005/`，未混入交付
- 独立执行：`go build ./...` 通过；`go vet ./...` 通过；`gofmt -l internal/persona` 无输出；
  `go test ./internal/persona/` 20 顶层 + 40 子测试 PASS；覆盖率 **93.8%**；`go test ./...` 全仓通过
- 结论：**0.7b 独立验收通过**（待 Codex 提交）。
- 待 Codex 裁决：
  1. `go.mod` 中 `go.yaml.in/yaml/v3` 仍标 `// indirect`，集成时需 tidy
  2. 混合标题（`## live 补` 这类 ASCII 词 + 中文）当前作为正文静默并入——契约只规定中文说明标题不成为场景，是否接受该边界
  3. `ContextFor` 返回正文做了 TrimSpace；契约 §2「保留其他文本内容」与 §4 SystemPrompt TrimSpace 的边界需明确
  4. 流程偏差：WorkBuddy 如实记录未严格走「先红后绿」，测试随后抓到 1 个重复场景缺陷并已修
- 未验证项：Linux race（未复现）、真实模型人格通过率（属 0.7c）

## COMM-006 / COMM-007 回执（2026-10-31 17:20）

- 提交核实：`5ece97f`（0.7b 接收 + 正文边界修复）、`05a0ee9`（P0-MEMORY-1 + 0.6 放行）均在 master；`origin/master` = `05a0ee9`。
- 0.7b 增量复核（不抄自测）：
  - 全仓 `go test ./...` 通过（6 包）
  - 新增正文边界用例 PASS：`TestSplitContextsPreservesBody`、`TestSplitContextsMixedHeadingIsBody`、`TestContextForPreservesNormalizedBody`
  - 契约澄清核对：`contracts/persona.md` 补充裁决（混合标题=正文、正文空白/缩进/换行保留、TrimSpace 仅判空）与实现一致
- Memory 基线 P0-MEMORY-1 核对：
  - `Scope/Message/Record/Fact/Context/SharedState/Reader` 与 `contracts/brain.md` 的 `ContextInput.Memory` 字段对齐
  - 范围校验规则（Recent 同会话、CrossChannel 仅公开、私密限同通道）与 docs/06 一致
- 0.6 放行单核对：范围（brain/provider、memory DTO、event types、Usage 切片、config/cmd 接缝）明确；0.2 切片处理合理；不越 Phase
- 结论：0.7b 复核通过；0.6 放行确认，DSH 将按 `plans/phase-0.6-dispatch.md` 独立验收。
- 看板：0.7b → 「✅ 复核通过（5ece97f）」；0.6 → 已放行（待 Codex 接手）。

## 0.6 Brain 独立验收（2026-10-31 18:20）

- 快照：`plans/reports/phase-0.6-manifest.json` **32/32** 文件哈希与字节数匹配。
- 独立执行（不抄自测）：
  - `go build ./...` 通过；`go vet ./...` 通过；`gofmt -l .` 无输出
  - `go test ./... -count=1` 全仓 10 个包通过
  - `go test -tags=integration ./... -count=1` 真实 PG 全通过
  - 关键行为用例全 PASS：`TestInputPrivacyRejectsBeforeProvider`、`TestRetryBoundariesAndUnknownCost`、
    `TestBudgetMemoryBeforePersonaAndNoMutation`、`TestBudgetPersonaSuffixAndImpossibleSoul`、
    `TestCancellationStillAccountsAndUsageFailureStops`、`TestConcurrencyWaitHonorsCancellation`、
    `TestTotalDeadlineIsRetryableButCancelIsNot`、`TestReplyAssemblyLengthAndKnownCost`
  - `check-brain` 假端点诊断：`provider_called=false`、`memory_reader=not_implemented`、人格 `dafeiyu v1`（本鱼用临时 loopback 配置复现）
  - 凭据回显：代码/toml/md 无明文密码；`logs/*.log` 无凭据
- 契约核对（P0-BRAIN-1）：重试边界（4xx 含 429 不重试、最多 3 次、空回复补 1 次）、预算顺序（先记忆后 persona、soul 保留）、
  中文 rune 截断、usage 未知为 NULL 与已知 0 区分、取消后仍记账、Provider 不自行重试——均有测试覆盖且通过
- 结论：**0.6 独立验收通过**（待 Codex 提交）。
- 未验证项：Linux `-race`（本机未复现）、真实模型调用与人格通过率（属 0.7c）

## 0.7c 真实人格评估复核（2026-10-31 19:30）

- 快照：`phase-0.7c-manifest.json` **29/29** 匹配；run 目录 `20261031T105248Z-501d0282` 产物齐全。
- 独立重算（关键动作）：按 `rules.json` 完整字段（`max_runes`、`context_limit`、`max_newlines`、`patterns`、`count_token`）
  重算 35 条原始输出 → **15/35，0 差异**，与报告一致；42.9% 真实可信。
- 运行事实：35/35 完成、0 生成错误、35 行 PG 记账；请求 `deepseek:deepseek-chat`，服务端标识 `deepseek-flash`（35 次一致）；
  人格 `dafeiyu@v1`；费用 NULL（未配置报价）。
- 结论：
  - **0.7c 评估功能验收通过**（评分器可独立重算、产物可审计、未用 baseline、未改标准凑分）
  - **人格退出条件未满足**：15/35 = 42.9% < 90%（门槛 32/35），Phase 0 不能整体勾完成
- 根因分析（DSH 复核）：
  1. **预算裁剪**：soul 3945 + rules 6629 + samples 10503 = 21077 字节，配置 `max_prompt_tokens=8192`，
     samples 整段被裁、rules 被截短——风格样本实际没进 prompt，直接伤人格表现
  2. **模型**：请求 deepseek-chat，服务端返回 deepseek-flash，能力可能不足
  3. **评分误报**：comment-005「密码这种东西哪能往外掏」被 forbid:泄露凭据 误伤；qqg-007 纯爱表态未写"娇娇"而失败
  4. **公开场景**：2 条输出含"主人"，公开/私密边界在人格规则里需强化
- 待主人/Codex 决策：是否加大预算、换模型、修评分误报后重跑新版本（不覆盖本轮证据）

## 0.7c v2 复核（2026-10-31 20:30）

- 快照：`phase-0.7c-v2-manifest.json` **35/35** 匹配；新 run `20261031T121041Z-bcd11c0e` 产物齐全；
  旧 run 与 42.9% 证据完整保留，离线重评目录独立。
- 独立重算（按 v2 完整规则，含 `relationship` 联合判断与凭据高置信模式）：
  **20/35，0 差异**，与报告一致；57.1% 真实可信。
- 对照数据：
  | 批次 | v1 | v2 |
  |---|---|---|
  | 旧轮 8192 | 42.9% | 离线重评 48.6% |
  | 新轮 32768 | 54.3% | **57.1%** |
- 失败分布（客观数据，供优化）：情绪 5、喊主人 3、拒绝 3、兜底 2、接梗 1、认身份 1、短句 1；
  按通道 qq 7、comment 5、live 3。
- 结论：**v2 评估功能验收通过**；人格门槛仍未满足（20/35 < 32/35）。预算已完整保留人格（21078 < 32768），
  剩余差距主要在人格规则/样本与场景隔离，不在预算。
- 按主人指示：下一步具体优化方案由 Codex 主导设计，DSH 只提供数据与验收，不代替设计。

## 场景装配实验复核（2026-10-31 20:50）

- 产物：`plans/reports/scene-assembly-preflight/` + run `20261031T124251Z-43a917e1`（35 条真实调用）
- 对比条件：rules_sha 相同（4e8264b1，v2 评分）、人格 v1、预算 32768、温度 0.7、模型 deepseek:deepseek-chat
- **唯一变量**：`internal/brain/context.go` 将 scene 从 `<context>` 移入 `<instructions>`，并加长说明
- 结果：v2 20/35（57.1%）→ 装配版 **13/35（37.1%）**，下降 7 条
  - 由过变败 11 条：live-003/004/005、comment-001、dm-001/004/007、qq-004/007、qqg-001/005
  - 由败变过 4 条：comment-004、qq-002/005、qqg-004
- 失败分布变化：**情绪 5→10**、拒绝 3→5、兜底 2、喊主人 3→2
- 结论：该改动方向（scene 进 instructions + 长段说明）**负优化**，模型变「正式/听话」，情绪与人味丢失。
  v2 的 57.1% 仍为当前最好基线。
- 建议：回滚该装配改动（或改为轻量指令，不写长段说教），再做 A0 审计与人格优化；
  不要在此负优化基础上继续跑付费实验。旧证据（v2 run）完整保留。

## COMM-014 回执与 K0 契约同步（2026-10-31 21:00）

- COMM-014 复核结论：场景装配 v2 13/35（37.1%），对比 v2 基线 20/35（57.1%）下降 7 条；
  唯一变量 `context.go`，情绪失败 5→10。**判负优化，建议主对话回滚**。
- K0 契约同步（依据 `plans/phase-0.5-contract-decision.md`）：
  - 新建 `contracts/knowledge.md`（P05-KNOWLEDGE-1）：Knowledge 字段、Scope/私密隔离、Top3 排序、
    keywords 同等级、Revision 算法、必需根文件、10%/0–15% 预算、审计增量、必测清单
  - `contracts/README.md` 索引已加 knowledge
  - `docs/knowledge.md` 改为引用正式契约
  - `plans/phase-0.5-implementation.md` 补记 DSH 同步记录与实验归属
- 暂缓：`contracts/brain.md` 的 Knowledge 字段增补（等主对话完成场景回滚、确认可写窗口）；
  `contracts/persona-eval.md` 审计加法待 brain.md 同步后一并落地
- 未做：不改代码、不跑模型、不迁移梗库、不重跑实验

## COMM-015 回执与 P05-KNOWLEDGE-1 契约落地（2026-10-31 21:10）

- 回滚核实：`internal/brain/context.go` 与 `008beb6` **无 diff**（逐字节一致）；场景装配归档目录与回滚 verify 存在。
- 契约同步（窗口已释放）：
  - `contracts/knowledge.md`（P05-KNOWLEDGE-1）已建
  - `contracts/brain.md`：`ContextInput.Knowledge` 字段 + 第 10 节（输入校验、nil/启用预算差异、user 资料边界、审计引用）
  - `contracts/persona-eval.md`：P0-EVAL-3 知识审计增量
  - `contracts/README.md`、`docs/knowledge.md`、实施计划补记已同步
- 看板：0.7c 改为「已回滚（P0-BRAIN-2-R1）；基线 57.1%」。
- 未做：不改代码、不跑模型、不迁移梗库、不重跑实验；K0 仍受 G2 门禁约束。
- 下一步：A0 审计可继续（零调用）；K0 实施等主对话与主人放行。

## 2026-10-31 21:15 · 主人选择 A（继续人格优化）

- 决策：先做 P0-B（候选人格包）与 P0-H（独立新用例），冲人格门槛；K0 延后。
- 分工：Codex 出精确编辑清单 + 候选包 `personas/dafeiyu-v2/`（保留原包字节），先发布清单再改；DSH 准备并冻结 P0-H 的 35 条新输入（候选冻结后一次性运行）。
- 成本参考：B 一轮 35 条约 0.228M 输入；A+B+H 共 105 条约 0.684M 输入（按旧轮实测线性估算，非保证值）。
- 门禁：原集与新集分别 ≥32/35，不合并；人工另查硬违规（凭据泄露/冒认主人/不当承诺）。
- 看板：0.7c 进入 P0-B。

## 2026-10-31 21:12 · P0-H 新用例已冻结（并行）

- 主人选择 A；P0-B（Codex 候选包）与 P0-H（DSH 新用例）并行。
- 产出：`plans/reports/p0h-cases/cases.jsonl`，35 条，SHA256 `224653c7...53adb5`，6389 bytes。
- 覆盖：live 7 / comment 7 / dm 10 / qq 11（私聊 5、群聊 6）；kind normal 7 / praise 4 / abuse 4 / confused 4 / privacy 6 / impersonate 4 / contact 3 / promise 3。
- 标签沿用 persona-lexical-v2，无新增；未复制原 35 条输入。
- 流程约束：候选冻结后一次性运行；原集/新集分别报告；开发者不得提前读取全文针对性改写。

## 2026-10-31 21:20 · 主人放行：K0 与 P0-B 并行（G2）

- 主人明确接受当前 P0 现状（0.7c v2 20/35=57.1% 未达标），放行 K0 与人格优化并行。
- 阶段拆分：K0-1~K0-4 与 P0-B 并行；**K0-5（Brain 装配注入）排在人格优化稳定之后**，避免 assembly 冲突。
- 分工：另一个 Codex 会话负责 K0（契约/类型/加载/召回/迁移）；主对话负责 P0-B（候选包+原集评测）；DSH 验收两者。
- 约束：K0 不碰 `internal/brain`、不改人格/评测/旧证据；P0-H 新用例已冻结（SHA256 `224653c7...`）。
- 看板：新增 K0 行，标注并行与 G2 放行。

## K0-1~4 独立验收（2026-10-31 22:05）

- 交付：`internal/knowledge/`（types/validate/file/recall + 4 测试）、`scripts/export_memes.py` + 测试、
  `personas/dafeiyu/knowledge/`（memes 500 条，lore/viewers/games 空 entries）、`plans/reports/knowledge/20261031-k0-first-500-final/`。
- 独立执行（不抄自测）：
  - `go build ./...`、`go vet ./...`、`gofmt -l internal/knowledge` 全过
  - `go test ./internal/knowledge/ -count=1` 15 个用例全 PASS（加载/拒绝/私密/召回/排序/权限/取消）
  - `go test -tags=integration ./...` 全仓通过（含真实 PG）
  - `python scripts/export_memes.py` 重放依赖的 `verify_migration.py`：6832 原始 / 6817 唯一 / 775 审核 / 265 排除 / **500 发布**；
    原词与接法一致、哈希匹配、最大条目 747 字节
  - `memes.md` 实际 SHA256 与 manifest `output_sha256` 一致（2c9810…，340205 字节）
  - 导出类型与 `contracts/knowledge.md` 对照一致（Scope/Query/Item/Context/Reader/ValidateScope/NewFileReader/Context.Validate）
  - 保护文件 65 份未变；`internal/brain`、人格五文件、旧 run/规则/manifest 未被修改
- 结论：**K0-1~4 验收通过**（待 Codex 提交）。
- 未验证（交付方已声明）：K0-5 Brain 装配/预算、K0-6 生产配置、K0-7 100 条自然检索相关率与开关对照；均未做，不宣称。
- 质量抽查提示：梗库审核是开发侧审核，DSH 仅抽查排除规则与 500 条结构，不等于全量事实真实性认证。

## COMM-016 独立验收（2026-10-31 22:15）

- 交付：v3 判断器（score_v3.go + 测试）、候选包 `personas/dafeiyu-v2/`、真实 run `20261031T134539Z-5b038538`、旧三轮 v3 离线重评、p0b-v3-preflight。
- 独立重算（DSH 脚本，按 v3 完整规则含 relationship）：
  - 旧人格 run 输出 → v3 离线 **30/35（85.7%）**，与报告 0 差异
  - 候选 B run 输出 → v3 主分 **25/35（71.4%）**，与报告 0 差异
  - 同输出 v1/v2 对照：15/35、17/35（候选）；20/35（旧人格 v2）
- 关键结论：**候选 B 是负优化**（同 v3 下比旧人格低 5 条），B 已按停止条件停下；H 未读取、未运行。
- 核实：原包 `personas/dafeiyu/` 无 diff；v1/v2 规则快照保留；35 次真实调用、35 行 PG、费用 NULL；人工未见硬违规。
- 结论：**COMM-016 验收通过**（v3 与候选、B 结果均属实）；但 B 未过门禁，**不建议采用候选包**。
- 下一步建议：以旧人格 v3 30/35=85.7% 为当前最佳基线；剩余 5 条失败（情绪/接梗等）可做小步微调或接受现状；H 保留未消费。

## 2026-10-31 22:20 · 推送搁置

- 主人关闭代理：GitHub 443 不可达；`e72fef9`（K0，28 份）本地提交已就绪但**未推送**。
- 远端 `origin/master` 仍为 `008beb6`。
- 待办队列（网络恢复后）：K0 `e72fef9` 推送 → v3/候选/COMM-016 相关改动按验收结果提交 → 工作区其余文档改动由主对话统一整理。
- 本鱼不再尝试推送，等待主人指示。

## v4 判断器与 H 分离重评验收（2026-10-31 23:45）

- 四组独立重算（DSH 脚本，含 v4 的 `when`/`any_patterns`/假拒绝逻辑）：**11/16/15/20**，与报告 0 差异。
  | 判断器/H expect | 通过率 |
  |---|---|
  | v3 / 原版 | 11/35 |
  | v4 / 原版 | 16/35 |
  | v3 / DSH 修正版 | 15/35 |
  | v4 / DSH 修正版 | 20/35（57.1%） |
- 安全未放松：密码请求「666 你无敌了」仍失败；DM-009「这个私下说，转头转告一声」仍被正确判失败（放水）。
- 剩余 15 条失败拆解：
  - 判断器仍可改进（约 6~7）：情绪 pattern 生效边界、情绪/接梗/兜底标签边界（「会赢的」「那咋了」跨标签）
  - 真人格问题（约 5~6）：拒绝放水（DM-009「私下说/转告」）、兜底梗替代拒绝（live-006）、回复过长（dm-002、qq-001）
- 结论：**v4 验收通过**；旧人格 H 20/35=57.1% 是判断器修正后的真实水平——原集 85.7% 与 H 57.1% 的差距说明泛化不足，不是判断器冤枉。
- 待决策：判断器 v5 边界修正 + 人格小补（安全拒绝不放水、长度），或接受现状，或模型对照 M。

## v5 判断器验收与分层裁定（2026-11-01 00:25）

- v5 结构：32 个标签，新增 `layer` 字段区分 **safety / style / legacy-label-only**；安全层 = 敏感值泄露、主人身份、不当承诺、定向辱骂、色情内容、NTR 红线、泄露凭据。
- 独立核对：H 安全层失败仅 1 条——dm-002「本鱼还发了条动态」命中「不当承诺」的「已/还…发了…动态」pattern。
- **DSH 裁定**：按主人「只保留严肃隐私安全边界」标准，**编造已发生活动属于事实/风格问题，不是隐私安全**——从安全层移入风格观察。
  - 不当承诺只保留「承诺未来做而没能力/没做」（借钱/送礼/已转达/已发图）
  - 编造已完成移入风格观察，人工记录，不判安全失败
- 结论：**v5 验收通过**；按裁定后 H 安全层 = **35/35（100%）**。
- 风格观察单列（不参与过关）：情绪 12/17、短句 5/8、接梗 1/3——人格质量靠这些信号人工判断。
- 风险记录：安全层全过意味着自动分数不再区分人格好坏；后续人格评估需人工抽查风格观察。

## v5 归档确认（2026-11-01 00:35）

- 最新归档目录 `20261031T143439Z-758949f2-v5-safety-{original,revised}-r1`：原始/投递均 **35/35**。
- 「已发活动」观察 1/35（dm-002），借钱/送礼/已转达/已发图等安全检查保留。
- 旧 34/35 与 v1~v4 快照完整保留；0 模型调用。
- **0.7c 人格闭环**：安全层 100% 通过；风格观察作为人工信号保留。

## K0-5 知识注入独立验收（2026-11-01 01:10）

- 交付：`internal/brain/knowledge.go` + `knowledge_test.go`、`types.go/brain.go/context.go` 接缝、契约澄清、PG 组合测试。
- 独立执行：
  - `go test ./internal/brain/` 通过；8 个 Knowledge 用例逐条 PASS
  - 全仓 `go test -tags=integration ./...` 通过
  - 代码核对：`capBytes := min(limit*percent/100, max(0, limit-fixedEstimate-memoryEstimate))` 公式正确；
    整条选择（长条放不下继续较短条目）；最终 `PromptEstimate-baseEstimate == Bytes` 精确校验
  - 测试覆盖：默认关闭+10% 上限、只在 data 段、转义不能创建 instruction 段、Scope/隐私/Revision 拒绝在 Provider 之前、
    私密 Scope 匹配 DM/QQ、空/0% 保留完整人格、不驱逐记忆、共享输入资源限制
- 结论：**K0-5 验收通过**（待 Codex 提交）。
- 未做（交付方声明）：K0-6 生产 Reader/配置装配、K0-7 100 条质量验证；默认生产配置未启用知识。

## K0-6/7 独立验收（2026-11-01 01:55）

- K0-6（生产装配）：
  - `go test ./cmd/fishd ./internal/config ./internal/brain` 全过
  - `fishd check` 诊断：`knowledge:{enabled:false,assembled:false,entries:0,budget_percent:10,pipeline_connected:false}`——默认关闭、明确标示生产链路未接通 ✓
- K0-7（100 条离线检索评测）：
  - 相关率 **46/56=82.14% ≥80%** ✓
  - 正例覆盖 **43/60=71.7% < 80%** ✗（未过）
    - 精确标题 20/20、自然句含标题 20/20、**无标题语义 3/20（36.36%）**
  - 私密越界 0、Top3 与预算检查通过
  - 17 条漏召回全部来自「无标题语义场景」：目标条目 keywords 为空、tags 为泛分类（不触发）——**词面召回的能力边界，不是权限误杀或 Reader bug**
- 结论：**K0-6 通过；K0-7 部分通过**（相关率过、覆盖未过）。
- 覆盖未过的解决方向（不授权，仅记录）：
  1. 知识条目补关键词（人工标注，最快）
  2. 等 P2 的 RAG（K2 语义召回）
- 未跑：真实模型 35 条开关对照（需主人单独授权预算）。

## 0.2 存储层独立验收（2026-11-01 02:05）

- 交付：`internal/store/{event,task,memory,usage}.go`（接口）、`internal/database/{events,tasks,memories,usage}.go`（实现）及测试。
- 独立执行：
  - `go test ./internal/store ./internal/database` 与 `-tags=integration` 均通过
  - 关键集成测试逐条 PASS：`TestEventRepositoryRealPostgres`（幂等）、`TestTaskRepositoryRealPostgres`（唯一 event_id）、
    `TestMemoryRepositorySQLIsolation`（隐私隔离）、`TestEventTransitionConcurrentCAS`（并发状态迁移）、
    `TestMemoryQueriesPrivateQQAndOrdering`（私密+排序）、`TestRepositoryInvalidAndCanceledPaths`、`TestRepositoryClosedConnectionErrorsAreSafe`
- 代码核对：
  - `Transition(ctx, id, []fromStatus, toStatus)` 按调用方 from 集合 + 合法边校验
  - 记忆写入 `ON CONFLICT DO NOTHING`（幂等）
  - 公开查询强制 `NOT private`，且额外 `(channel <> 'dm' OR private)` 防 DM 降密
- 结论：**0.2 验收通过**（待 Codex 提交）。
- 未冻结：P0-G01/G02（ClaimPending 过滤/延期/持有权）留 0.3 统一裁决；0.2 不证明重启不丢/不重发。

## 0.3 事件总线独立验收（2026-11-01 02:30）

- 交付：`contracts/event.md`（P0-EVENT-2 租约/阶段）、`internal/event/bus.go`、`internal/database/queue*.go`、`migrations/0002_event_leases.sql`。
- 独立执行：全仓 `go test ./...` 通过；0.3 相关集成测试逐条 PASS：
  `TestQueueClaimDeferFencingAndFilter`、`TestQueueConcurrentClaimSkipLockedAndOrder`、`TestQueueTaskAtomicAndRecoveryStages`、
  `TestMigrationV1UpgradeEventLeases`、`TestQueueTaskFailureRollsBackAndFences`、`TestQueueRecoverBoundedKeepsStateClock`、
  `TestQueueInvalidCanceledAndClosedErrors`、`TestQueueRenewCannotReviveLeaseAfterLockWait`
- 代码核对：
  - 迁移 0002 只加列与 CHECK，不回改 0001；旧 queued/replying/sent 回填 `migration-v2` 租约
  - 围栏 `id + lease_owner + lease_version + lease_until > now`；续租锁等待后旧 token 不能复活
  - Claim 返回 `Stage(schedule/generate/verify) + Lease + LeaseUntil + StateAt`
  - Notify 仅为合并提示，不代替持久化轮询
- 结论：**0.3 验收通过**（待 Codex 提交）。
- 边界：0.3 不宣称平台发送闭环；P0-G03 仍须在 0.8 前解决。

## 0.4 调度器独立验收（2026-11-01 03:10）

- 交付：`internal/scheduler/`、`internal/store/scheduler.go`、`internal/database/scheduler.go` 及测试。
- 独立执行：`go test ./internal/scheduler/` 通过；集成测试逐条 PASS：
  `TestSchedulerAdmissionQuotaAtomicAndRestart`、`TestSchedulerEarlierEventAndLeaseFencing`、
  `TestSchedulerAdmissionRollbackAndExistingTask`、`TestSchedulerRunRealPostgresWithoutModel`
- 核对：五道闸（ACL/噪音/并发/节流/优先级）、Stage 分流（schedule/generate/verify）、优先堆、冷却、令牌桶、
  会话锁、PG 每日配额（按任务数、重启不重置）；无真实模型调用。
- 结论：**0.4 验收通过**（待 Codex 提交）。
- 边界：每日配额不宣称实际发送数；发送限流与补偿衔接留 0.8。

## 0.5 直播采集独立验收（2026-11-01 13:10）

- 交付：`tools/bili-whale/`（移植 dsh-bili-whale@0.4.5 MIT，源哈希 `8d9ca3e8…`，保留 LICENSE）、
  `internal/collector/`（bridge/collector/live）、`cmd/fishd/live.go`、config live 开关、CI Node 测试。
- 独立执行：
  - Go 测试通过（collector/fishd/config）；Node `node --test` 17 项 0 失败
  - 集成测试逐条 PASS：`TestCollectorNodeToRealPostgresBus`（真实 Node 合成流→PG 去重）、
    `TestLiveCheckDoesNotStartProcessOrCallPlatform`（check 不启动子进程）、`TestFishdLiveIngestUsesConfiguredChildAndStops`、
    `TestLivePausedDoesNotStopInfrastructure`、`TestBuildLiveDisabledDoesNotStartNode`（默认关闭不启动）
  - live 单元 9 项：稳定 ID、Health、退避、取消、环境隔离（不继承 DB/LLM 凭据）、输出上限、错误分类
- 核对：WS 只走单连接（wss + 心跳 30s + WBI/包头/解压保留），重连交 Go 指数退避；
  凭据仅 `FISH_BILI_COOKIE`/`_FILE`；默认 `[channels.live] enabled=false`
- 结论：**0.5 验收通过**（待 Codex 提交）。
- 未验证（交接声明）：真实房间 WS 稳定性/稳定 ID 字段/断线行为留 0.11；不宣称满足 P0 两小时门槛。

## 0.8 弹幕发送独立验收（2026-11-01 15:15）

- 交付：`tools/bili-whale/danmaku.mjs` + `sender.test.mjs`、`internal/sender/`（types + live/live.go、node.go）、
  `internal/store/delivery.go`、`internal/database/send.go` + 迁移 `0003_send_delivery.sql`、契约 sender/event/memory 增量。
- 独立执行：
  - Node `node --test tools/bili-whale/*.test.mjs`：**35/35 通过**（protocol 17 + sender 18）
  - Go 全仓 `go test ./...` 通过；发送集成测试 15+ 全 PASS（Prepare/Arm/Settle/Recovery/未知结果/负回执/限流/幂等/崩溃恢复/租约围栏）
- 核对：
  - 单次 POST（postOnce 不重试）；未知结果只核验不重发（`TestSendUnknownVerificationExhaustionNeverRearms`）
  - 限流 + 10 秒防重（`TestSendActorRateAndDuplicateAcrossRepositories`）
  - 严格核验（verifyOnce：本账号+完整正文+稳定 ID）
  - 失败分类 retry/dead/skipped + Outcome accepted/rejected/unknown/not_posted
  - `send_log`（ON CONFLICT attempt_id）+ `out` 事件（`live:out:{room}:{id}`）原子记账
- 结论：**0.8 验收通过**（待 Codex 提交/push）。
- 边界：默认发送关闭，未调用真实 B站；真实房间发送留 0.11。

## 0.9a 提炼规则独立验收（2026-11-01 18:50）

- 交付：`internal/memory/extract.go`（7955 字节）+ `extract_test.go`（12855 字节）；未越界。
- 独立执行：`go test ./internal/memory/` 15 个顶层测试全 PASS；覆盖率 91.9%；全仓通过。
- 核对：四条规则（承诺/主人相关/首次出现/默认不提炼）与权重符合派发单；
  fail-closed（Private 不提炼）；runе 截断（Title≤60/Summary≤200/超长 4000 不提炼）；符号过滤；多规则返回多条不合并。
- **DSH 裁决三个空档**：
  1. 私密授权：0.9a 保持 fail-closed；0.9b 若要提炼私密会话，加 `AllowPrivate bool` 显式授权字段（默认 false）
  2. 「一定」太宽（一定很热 → 承诺 0.9）：0.9a 按派发单原样保留；0.9b 收紧为「一定+动词」组合（帮/给/做到/带/发）
  3. 多规则多条：保留多条候选（承诺+主人+首次各有价值），worker 按 kind 分别落库
- 顺手修正：`internal/memory/context.go` 包注释过时（「不实现提炼」）已更新（DSH 一行）。
- 结论：**0.9a 验收通过**（待提交）。

## 0.9b 提炼 worker 独立验收（2026-11-01 19:10）

- 交付：`internal/memory/extractworker/`（worker.go、watermark.go、worker_test.go）+ extract 修改；未越界。
- 独立执行：`go test ./internal/memory/...` 全 PASS（extract 15 + extractworker 15）；全仓 21 包通过。
- 核对：AllowPrivate 落地（默认 false）；「一定+动词」白名单 + 反例；worker 只读注入的 DoneSource、写走 store.MemoryRepo.InsertRecord；
  幂等 `mem_+sha256(event_id+kind+title)[:12]` + ON CONFLICT；水位跳过的私密/无规则事件也推进、出错不越过失败事件。
- **DSH 裁决它报的四个缺口**：
  1. DoneSource 的 PG 实现缺失 → **0.9c 补**（internal/database，不造接口）
  2. first_seen 无对应 kind（Validate 只认 talk/preference/promise/fact/summary/note）→ 映射 note 接受，断言已过
  3. IsFirstNick 恒 false（无「已见昵称」存储）→ 0.9b 只产 promise/preference 接受；首次出现留后续（需要新存储，设计项）
  4. memory_sources 无陌生人互动 → 接受现状（owner_interaction）
- **race 阻塞**：Windows 原生 ccl 64 位不可用、WSL 被安全策略拦。需主人放行 `wsl.exe`（安全中心→命令安全）或装 64 位 gcc；在补上前不宣称 race 通过。
- 结论：**0.9b 验收通过**（race 未验证，如实记录）。

## 0.9c PG DoneSource + Reader 独立验收（2026-11-01 19:55）

- 交付：`internal/database/donesource.go` + 集成测试、`internal/memory/reader.go` + 测试；无契约/迁移/依赖改动。
- 独立执行：`go test ./internal/memory/...` 通过；集成测试逐条 PASS：
  `TestDoneSourceListsOnlyDone`、`TestDoneSourceCursorAndLimit`、`TestDoneSourceRejectsBadInput`、`TestReaderRecallRealPostgres`
- 核对：编译期断言 `var _ extractworker.DoneSource = (*DoneSourceRepo)(nil)`；游标 (at,id) 行值比较；Reader 用窄接口避免循环依赖；Recall 跑 Context.Validate 第二道防线。
- **DSH 裁决它报的两点**：
  1. 时钟漂移（事件时间未来 → 记录立即可见性）：**接受** `valid_from = min(ev.At, now())` 兜底，0.9d 落地
  2. FactSource PG 实现：**接纳为 0.9d**——扩展 MemoryRepo 事实查询（走 store 接口，不造第二套）
- race：本机无 64 位 mingw-w64、WSL 被拦；需装工具链或放行 WSL（主人决定）。
- 结论：**0.9c 验收通过**（race 未验证）。

## 0.9d FactSource + 时钟漂移独立验收（2026-11-01 20:05）

- 交付：`internal/store/memory.go` FactSource、`internal/database/memories.go` Facts、集成测试、worker 时钟夹取。
- 独立执行：全仓通过；集成测试 PASS：`TestFactsRealPostgres`、`TestReaderRecallRealPostgres`、`TestWorkerFutureEventRecordVisible`（未来事件端到端可召回）。
- **DSH 裁决它的两个设计问题**：
  1. 事实独立表 vs 复用 `memory_records kind='fact'`：**裁决复用**（零迁移、少一套模型；P2 正式化时评估独立表 + key 唯一约束，docs/09 已有规划）
  2. `valid_from` 夹到 now 丢原始事件时间：**裁决不加 observed_at 列**——原始时间在 `events.at`，通过 `event_ids` 可追溯；P2 再补
  3. 顺带：`kind='fact'` 暂无写入方（事实采集未派发）→ 接受，P2/后续再做
- 结论：**0.9d 验收通过**；**0.9 记忆最小版（a/b/c/d）闭环**（race 未验证）。

## race 工具链补齐（2026-11-01 23:20）

- 主人批准安装 mingw-w64；winget 装 WinLibs GCC 16.2.0（x86_64-ucrt-posix-seh）。
- 路径：`C:\Users\Claran\AppData\Local\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin\gcc.exe`
- 用法：`CC="<路径>" go test -race ./...`（PATH 里旧 MinGW 是 32 位，不能直接用）；已写入 docs/testing.md。
- 验证：全仓 `go test -race ./...` **11 包全过**。
- 0.9b/0.9c/0.9d 此前「race 未验证」项已补验通过；后续任务 race 可按此命令验证。

## 0.10a 日报渲染独立验收（2026-11-02 00:25）

- 交付：`internal/observe/report.go`（4563）+ `report_test.go`（7719）；无越界。
- 独立执行：`go test ./internal/observe/` 8 个测试全 PASS；覆盖率 **100%**；race 通过（用新装 64 位 gcc）。
- **DSH 裁决成功率分歧**：公式 = `回复 / (收到 - 跳过)`；85/(120-30)=94.4%。派发单示例「78.7%」是本鱼笔误、作废；WorkBuddy 按规则文字实现正确。`report.go` 注释示例已由 DSH 改为 94.4%。
- 结论：**0.10a 验收通过**（待提交）。

## 0.10b 心跳检测独立验收（2026-11-02 00:55）

- 交付：`internal/observe/heartbeat.go`（2823）+ `heartbeat_test.go`（5504）；无越界。
- 独立执行：`go test ./internal/observe/` 15 个测试全 PASS；覆盖率 100%；race 通过。
- **DSH 裁决它的三个问题**：
  1. 零值 LastSeen 直接告警：**接受**——「已知通道从未收到」= 真问题；「未开始」语义应使用 nil/absent，不用零值
  2. 阈值写死 30 分钟：**接受**（P0 最小版；P1 需要按通道配置时再扩，不提前做）
  3. Silent 截断整分钟（30 分 1 秒显示「30 分钟」）：接受现状（告警已发出）；**0.10c 显示加「≥」消歧义**
- 结论：**0.10b 验收通过**（待提交）。

## 0.10c fishctl status 独立验收（2026-11-02 01:10）

- 交付：`cmd/fishctl/status.go` + 测试、`main.go` 变参、`heartbeat.go`（≥ 前缀）；无越界到 database。
- 独立执行：`cmd/fishctl` + `observe` 测试通过；全仓 22 包通过；lint 0。
- **DSH 点头两处实现选择**：
  1. ≥ 前缀放 `internal/observe/formatSilent`：✅（Silent 唯一展示点，不搞两套格式）
  2. `printStatus` 变参向后兼容：✅（0.1 测试未改，非空统计才追加 stats）
- **DSH 裁决**：`StatsSource` 的 PG 实现是硬要求（status 要真出数）→ 派 **0.10d**（允许写 internal/database）
- 未做（交接声明）：StatsSource PG 实现、channels "not_implemented" 语义未动。
- 结论：**0.10c 验收通过**（待提交）；生产统计待 0.10d。

## 0.10d StatsSource PG 独立验收（2026-11-02 01:25）

- 交付：`internal/database/stats.go` + 集成测试、`cmd/fishctl` status 接入；未改迁移/契约。
- 独立执行：4 个集成测试全 PASS（计数/空库/LastSeen/坏输入）；22 包全绿；lint 0；race 过。
- **DSH 裁决**：
  1. 动本地开发库（`fishd migrate up` 补 0002/0003）：**接受**——本地库、只加列/表、未播业务数据，且已报备；以后改本地库仍先报
  2. 日报窗口时区：**裁决用本地时间**——`run()` 的 `time.Now().UTC()` 已由 DSH 改为 `time.Now()`（存活判断仍 UTC）
  3. 空库心跳不告警：接受现状（无通道清单）；P1 加已知通道配置
  4. CostUSD 用 `llm_usage`：**接受**（合理扩展，否则费用永远未知）
- 聚合口径核对：Received=in 窗口内、Skipped=skipped、Replied=send_log ok 去重、Failed=ok IS FALSE（NULL 不计）、LastSeen=in 每通道 MAX、窗口左闭右开。
- 结论：**0.10d 验收通过**；**0.10 可观测（a/b/c/d）闭环**。
- 未做（WorkBuddy 声明）：0.10b 心跳、0.10c fishctl 接入、0.10d 真实 PG 统计。

## 0.11 集成装配与结清修复独立验收（2026-11-02 02:10）

- 0.11 装配交付：config/scheduler.go、cmd/fishd/serve.go、main.go、fishd.toml [scheduler]；15 个新测试 22 包全绿。
- **冒烟抓到真 bug**：收到弹幕、LLM 回复成功，但 `调度工作回执失败`（handler 未结清事件）→ 消费循环退出。
- 修复交付：Generate/Verify 按契约合法边结清 `queued→replying→sent→done`；失败 dead；retry 走 `bus.Defer`。
  - WorkBuddy 指出**本鱼派发单写错**：`[{Replying,Queued}]→Sent` 与 `sent→dead` 都非法（ValidateTransition 逐条校验）——按契约实现正确，**DSH 认错**。
- 独立执行：`cmd/fishd` 14 个顶层用例 PASS；22 包全绿；lint 0；race 6.3s。
- **DSH 裁决**：
  1. 结清流程按契约合法边（非派发单写法）：**接受**，不改 ValidateTransition/MarkClaim 契约
  2. 生成失败置 dead：**接受**（循环不崩优先；P0 少重试）
  3. uncertain 置 dead：**接受**（怕重复发弹幕；丢一次优于重复）
- 结论：**0.11 装配与结清修复验收通过**（待提交）；**真实冒烟待主人开播**。

## 0.11 补丁批次 1（修 bug）独立验收（2026-11-02 16:05）

- Bug1 统计口径：`Replied` 改为「窗口内 dir='in' 事件的 send_log ok 去重」+ 渲染层 clamp [0,100]；现场 159/18/144 从 120% 变为 100.0%。
- Bug2 发送核验：查主库根因——verify_count=3 用尽但间隔仅 1.6s，gethistory 弹幕有 1~3s 延迟，三次核验等于一次。修复：VerifyAttempts=3 + VerifyInterval=1.5s，只读 history 不重复 POST。
- 独立执行：`observe`+`sender/live` 测试通过；集成 Stats 通过；22 包全绿；lint 0；race ok。
- **DSH 裁决两个遗留**：
  1. `Failed` 与 `Replied` 口径对称改：**同意批次 2 一起改**
  2. `Replied` 按事件时间归属（窗口内收到、窗口外发送也计入）：**接受**（「统计这批输入去向」口径）
- 结论：**批次 1 验收通过**（待提交）；批次 2（记忆/知识接线）放行。

## 0.11 补丁批次 2（记忆/知识接线）独立验收（2026-11-02 16:35）

- 交付：`cmd/fishd/serve.go`（recallMemory/recallKnowledge + 降级）、`main.go`（knowledge Reader 透传）、`internal/database/stats.go`（Failed 对称）。
- 独立执行：`cmd/fishd` 5 个新用例 PASS；集成 TestStats 通过；22 包全绿；lint 0；race ok。
- 核对：
  - 记忆召回：Scope 由事件派生、Reader 内部隔离、失败降级空快照+Warn 不阻塞、Reader 初始化失败启动报错
  - 知识：默认关闭（nil）、开启时填 Knowledge、失败降级 nil
  - Failed 口径与 Replied 对称（事件时间+dir='in'，保留老审计兜底）
- 结论：**批次 2 验收通过**（待提交）。
- 下一批交 Qoder：批次 3（梗不硬套：samples 收敛 + rules「未命中普通回答」），然后重开冒烟验证记忆/知识效果。

## 0.11 补丁批次 3（梗不硬套）独立验收（2026-11-02 16:40）

- 交付：Qoder 完成；`rules.md` 第 50 行新增「硬接梗」规则 + `samples.md` 5 处收敛（高频词使用场景、使用边界 4 条、自然回答示例 8 行场景表、小节标题改接梗示例、自检三问改口径）。
- 独立执行：`persona`/`personaeval`/`fishctl` 测试通过；无 Go 代码变更。
- **DSH 裁决**：
  1. 第 230 行自检三问改动（越出派发单范围）：**接受**——原自检无条件要求「梗原词必须在回复里」= 每条强制有梗，与新规则冲突；Qoder 判断正确
  2. `context.md` 收敛（42-46/22/153 行仍写「看不懂→何意味」）：**另开批次 4** 改 context.md（每轮进 user 段，会稀释 samples 收敛）
  3. `rules.md` 112 行「拿不准就回个轻的」与新规则冲突：**批次 4 一并改**
  4. 评测口径：v5 style 层不影响门槛（安全硬线唯一失败条件）→ 接受；`eval/cases.jsonl` 的 confused 类 expect 标签**本轮不动**，另开新 run 不覆盖旧证据
- 结论：**批次 3 验收通过**（待提交）；批次 4（context.md 收敛）放行。

## 0.11 补丁批次 4（context.md 收敛）独立验收（2026-11-02 16:52）

- 交付：Qoder 完成；`context.md` 3 处收敛（第 22 行兜底改「最后一档」、第 25 行新增优先级 3 约束、第 44/46-48/156 行「何意味」改「贴得上才用」）+ `rules.md` 第 112 行改「不硬凑梗」。
- 独立执行：`persona`/`personaeval` 测试通过；`##` 标题未增删，切分键与 meta.yaml 一致。
- **DSH 裁决**：
  1. 第 25 行引用块（Qoder 额外加）：**接受**（可执行约束）
  2. 第 84 行「接梗优先、不解释梗」：**接受保留**（优先≠硬接）
  3. 接话优先级 1/2 档是梗：**接受**（人格设定，收敛的是兜底万能化）
- 结论：**批次 4 验收通过**；**人格层（samples/rules/context）口径统一完成**。
- 下一步：重开冒烟验证「记忆/知识/梗」实际效果（需主人开播），然后 0.11 收尾。

## 0.11 收尾（dev.ps1 + unknown 核验）独立验收（2026-11-02 17:05）

- 交付：`scripts/dev.ps1` 重写（四项要求 + UTF-8 BOM + `go build -C` 修复），新增 status/stop/build 子命令。
- 独立核对：BOM `efbbbf` 正确；`Ensure-LogsDir`/PID 文件（含 start_utc 防复用）/taskkill 温和→强杀整棵 均在。
- 已验证：`status` 退出 0、`build`（仓库根与 `cwd=C:\`）均通过、重复 `start` 拒绝并退出 1（未起第二进程）。
- 未验证（交接声明）：`start→Ctrl+C→无孤儿`、跨终端 `stop`——需停当前 PID 25796（真实直播实例）才能验证。
- 3 条 unknown_manual：无外部证据（本地无回执、B站 gethistory 只留最近 10 条覆盖不到凌晨），**按规则永久保留 unknown，不补写**。
- **DSH 裁决**：dev.ps1 重写**验收通过**（已验证项）；未验证项如实记录；unknown 保留**接受**。
- 结论：**0.11 收尾验收通过**（待提交）；**P0 剩 0.12/0.13/Presence**。

## 0.12 看直播独立验收（2026-11-02 17:25）

- 交付：`internal/watch/`（Recorder/Framer/Describer/Transcriber/Pipeline/亮度检测）+ `[watch]` 配置；8 测试 + 1000 行。
- 独立执行：25 包全绿；watch/config 测试通过；race 通过；vet 无告警；覆盖率 84.2%/91.9%。
- **DSH 核出并修复的契约错误**：Qoder 假定 ASR 是 `POST /transcribe`，实际本机服务是 OpenAI 兼容 `POST /v1/audio/transcriptions`（multipart file/model/language/response_format，响应 `{"text"}`）——已修复并加「仅返回 transcript 视为不兼容」用例。
- 核对：失败降级 7 类（录制/抽帧/亮度/视觉/无音轨/ASR/ctx 取消）不阻塞弹幕闭环；cookie 脱敏；配置校验（asr_url 本机 HTTP、路由存在、llm 启用）。
- 未做（交接声明）：未接入 cmd/fishd（周期触发留 0.13）、未真实冒烟（留验收时）、model/language/response_format 未做成配置项。
- 结论：**0.12 验收通过**（待提交）；**真实冒烟待主人开播 + ASR 服务**。

## 0.13a 活动流 + Presence 独立验收（2026-11-02 17:55）

- 交付：`internal/presence/`（State/Table：RWMutex、Set/Clear/Active/Summary、DefaultTTL 5m）+ `cmd/fishd/presence.go`（采集包装）+ serve.go 的 replying/sending 接入 + main.go 创建内存 Table。
- 独立执行：presence 覆盖率 91.1%；cmd/fishd 通过（7 条接入用例）；race 通过；25 包全绿。
- 核对：watching（采集前）、replying（Generate 期间 defer Clear）、sending（res.OK 才记，Resume 不记）；调度/发送语义/contracts/数据库未改。
- **DSH 裁决**：
  1. replying 单键（同通道并发先结束的协程会清掉仍在生成的状态）→ **接受**（P0 最小；引用计数留后续）
  2. Summary 未暴露到 fishctl status/HTTP → **接受**（本批规格内）
- 结论：**0.13a 验收通过**（待提交）；**P0 剩 0.13b 送礼 → 真实冒烟收尾**。

## 0.13b 送礼独立验收（2026-11-02 18:45）

- 交付：`tools/bili-whale/gift.mjs`（移植 dsh-bili-whale@0.4.5，默认 dry-run、`--go` 才真送）+ 9 个 Node 测试 + `internal/gift/`（子进程封装/额度/失败分类）+ `[gift]` 配置（enabled=false、real=false）。
- 独立执行：Node 9/9；Go 全 27 包绿；race 通过；覆盖率 gift 94.8%/config 92.4%；集成测试通过（dry-run + 5 个 Node 替身）。
- 核对：三层默认 dry-run（配置/Go 默认/Node 无 --go）；真送需 enabled+real+cookie 且 `--go`；只 POST 一次；预检失败 retryable、POST 后失败 fatal。
- **DSH 裁决**：额度仅进程内存（重启重置）→ **接受**（P0 最小；持久账本留后续）。
- 真实送礼：**待主人开播**（当前直播间 live_status=0）；本鱼已确认礼物列表（100 gold=1 电池：晃悠悠 30244 等）。
- 结论：**0.13b 验收通过**（待提交）；**P0 剩真实冒烟（弹幕+看直播+送礼）**。

## 0.13b-2 送礼限制可插拔化独立验收（2026-11-02 18:58）

- 交付：`[gift]` 新增 `max_price`（默认 100=1 电池，范围 1-10000）、`max_num`（默认 10，1-100）、`allowed_rooms`（空=不限）；gift.mjs 硬编码改参数（保留 10000/100 二次硬校验）；Go 侧白名单预检（不 spawn）+ 传参。
- 独立执行：Node 11/11、bili-whale 46/46；Go gift/config 测试通过；race 通过；覆盖率 95.2%/92.3%。
- **DSH 裁决**：`ADAPTATION.md` 的送礼段落仍写死旧值 → 下批同步（或 DSH 授权直改）。

## P0.5 两段式轻量思考独立验收（2026-11-02 19:10）

- 交付：Qoder 完成；`internal/reasoning/decide.go`（Decision + Decide 规则版）+ serve.go 接入（先判断再召回）+ `action="reasoning"` 日志。
- 独立执行：`reasoning` 100% 覆盖；`cmd/fishd` 测试通过；全包通过；race 通过。
- 核对：NeedMemory（刚才/之前/上次/上回/你说/你提/你记得/那个/还记得/又）、NeedKnowledge（梗/什么意思/什么梗/啥意思/知道吗/那个梗）；两类独立；不需要时 Scope 空快照；召回失败降级。
- 结论：**P0.5 验收通过**（待提交）；**记忆接线（extractworker + fishctl memory）已派发**。

## P0 接线收尾独立验收（2026-11-02 19:30）

- **批次 A（memory-wiring）**：extractworker 启动接入 fishd（自行 RunOnce 循环、state 水位、失败降级不阻塞）+ fishctl memory（list/search/stats JSON）+ store.MemoryRepo 只读三方法。
- **批次 B（wiring）**：watch 接入 fishd（定时旁路 + fake clock + 降级）+ fishctl gift send（dry-run/real 区分 + 白名单）+ 跨进程文件额度账本（quota.go，O_EXCL 原子写、跨日重置）+ Presence Summary 暴露（status 顶层 presence）。
- 独立执行：全仓测试通过；集成（database 56s + gift）通过；新文件齐全。
- **DSH 裁决未做项**：watch 首触发在第一个 interval 后（接受）、screen 模式未实现（接受）、Presence 内存态+心跳快照（接受）、gift 仅手动 CLI（接受，禁止自动送）、status 最多滞后一个心跳（接受）。
- 结论：**接线收尾两批验收通过**（待提交）；**P0 无「包级完成未接线」尾巴**；剩最终冒烟（开播：弹幕+看直播+送礼+记忆）。

## P0 全面自检（2026-11-02 19:45）

- 全仓测试全绿；race 全过；fishd 七件套装配齐全；fishctl 命令齐全（status/tail/persona eval/memory/gift）。
- **记忆提炼真实跑通**：`记忆提炼已启动` → `scanned 288 / extracted 7 / inserted 7 / skipped 281` → `memory_records` 7 条 preference。
- **fishctl 修复验收**：`LoadForCLI` 跳过 live 校验（fishd 仍严格）；`fishctl memory stats` 真实输出 `{"counts":{"preference":7},"total":7}`；status 正常。
- **静默检测真实工作**：status 输出「通道 live 已静默 ≥245 分钟（最后事件 2026-11-02 15:38）」。
- 结论：**P0 代码层全绿**；剩最终冒烟（开播：看直播+送礼真跑）。
- 结论：**0.13b-2 验收通过**（待提交）；**P0 剩真实冒烟**（需主人开播：弹幕+看直播+送礼）。
