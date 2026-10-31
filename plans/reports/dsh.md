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
