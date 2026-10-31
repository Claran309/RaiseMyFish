# WorkBuddy 回执与交接

> 模板由 Codex 初始化，后续仅 WorkBuddy 追加；不由其他 Agent 代确认。

## COMM-001 回执

- 已读状态：待 WorkBuddy 本人确认。
- 已读时间（Asia/Shanghai）、当前会话/任务：待填写。
- 当前写入文件、是否有旧任务需暂停：待填写。
- 接受范围：轻量开发，按 coordination 的具体派发；未派发不启动 Go 功能开发，不执行 Git 写操作。

## 每次交接模板（追加）

- 任务/子任务、基线/快照、获派与实际修改路径：
- 变更；需 Codex 接入的入口/依赖/接口：
- 测试命令与结果；未执行项及原因：
- 风险/阻塞/需裁决事项：
- 是否已暂停写入、交给 DSH 的验收范围：

---

## 回执 · COMM-001（追加于 2026-10-31，Asia/Shanghai）

**已读状态：WorkBuddy 本人已读并确认。**

- 已读文件：`plans/coordination.md` 全文（§1–§8，含 §6 看板与 §7 COMM-001 通知）。
- 已读时间：2026-10-31 15:10（Asia/Shanghai）。
- 收件方式：主人在本会话直接转达 §7 的转达文本 + 指定读 COMM-001 并写回执。
- 当前任务：**接手 0.7a 人格素材与用例**。0.7b 加载器、0.7c 完整回归按看板处于阻塞，不启动。
- 接受范围：轻量开发。不独立设计状态机 / 隐私策略 / 恢复与记账协议；不执行任何 Git 写操作（add / commit / pull / checkout / merge / push 一律不做）；不写 `contracts/`、`go.mod` / `go.sum`、`cmd/`、`internal/model/`、`config/`、`migrations/`、根目录工程配置与 CI。

### 旧任务暂停情况

- 上一轮按旧提示词「Phase 0.7 人格包迁移与回归（含 `internal/persona/` Go 实现）」开工，**现已暂停**，自本回执起不再向仓库写入任何路径。
- 0.5 采集器 / 0.8 发送器本轮未开工（仍需等 0.3 冻结），无暂停动作。
- 全程未执行 Git 写操作，未产生提交，未暂存；工作区改动保持未跟踪状态，是否认领由 Codex 决定。

### 本轮实际写入路径与快照

基线：无 Git 写操作，故以文件哈希作为版本标识（SHA256 前 16 位 / 字节数，2026-10-31 15:12 计算）。

| 路径 | SHA256(16) | 字节 | 是否在 0.7a 可写范围 |
|---|---|---|---|
| `personas/dafeiyu/meta.yaml` | 17916AD4DA918879 | 961 | 是 |
| `personas/dafeiyu/soul.md` | 4CC225DC840F2D9A | 3945 | 是 |
| `personas/dafeiyu/rules.md` | E6217CF1C35B221B | 6629 | 是 |
| `personas/dafeiyu/samples.md` | A986FADE06CDFD4E | 10503 | 是 |
| `personas/dafeiyu/context.md` | B0096CA16512695E | 7493 | 是 |
| `personas/dafeiyu/eval/cases.jsonl` | 963A61334B106EF5 | 6051 | 是（35 条） |
| `personas/dafeiyu/eval/baseline.md` | 9EA6EA813E9D9F64 | 4441 | 是 |
| `internal/persona/meta.go` | 7C410B9A77A7056D | 4951 | **否，越界，待裁决** |
| `internal/persona/pack.go` | 2E5D822AB673C42A | 9229 | **否，越界，待裁决** |
| `internal/persona/check.go` | B406047E3B7A72C9 | 9497 | **否，越界，待裁决** |
| `internal/persona/evalcase.go` | 31DAC5EBD35F710C | 4953 | **否，越界，待裁决** |
| `internal/persona/runner.go` | 93505D58401119C7 | 12341 | **否，越界，待裁决** |
| `internal/persona/pack_test.go` | FBB823A9AF4E48FB | 6907 | **否，越界，待裁决** |
| `internal/persona/eval_test.go` | 8051F0122991BC10 | 7400 | **否，越界，待裁决** |
| `plans/reports/workbuddy.md` | 本文件 | — | 由 COMM-001 授权 |

另：仓库根目录遗留一个自建临时探针文件 `.probe.txt`（本轮之前用于收集环境信息），已删除，不属于交付物。

### 变更说明（0.7a 部分）

- 素材来源与落点（先原样迁移，再压缩去重，未重写风格）：
  - `~/.bili-ai/persona.md` → 身份 / 玩梗 / 词库 / 咒术 / 人味铁律 / AI 味清单 / 示例，拆进 `soul.md` + `rules.md` + `samples.md`
  - `~/.bili-ai/persona-shared.md` → 娇娇关系、纯爱硬规则、默认兜底、语气底线，进 `soul.md` + `samples.md`
  - `~/.bili-ai/persona-live.md` → 直播口径，进 `context.md` 的 `## live`
  - `~/.dsh/persona-qq.md` → QQ 口径，进 `context.md` 的 `## qq_private` / `## qq_group`
  - `~/.dsh/AGENT.md` → 身份概要与语气底线，并入 `soul.md`
- `context.md` 按 `## live` / `## comment` / `## dm` / `## qq_private` / `## qq_group` 五段组织，与 `meta.yaml` 的 `channels` 严格对齐。
- `eval/cases.jsonl` 35 条，覆盖 5 个口径 × 5 类场景（normal / praise / abuse / confused / privacy）各至少 1 条；每条都带 `expect` 与 `forbid`。
- `eval/baseline.md` 固化基准回复并附评分标签词表，供人工 review。

### 需 Codex 接入的入口 / 依赖 / 接口

- `internal/persona/runner.go` 已导出 `RunEvalCLI(ctx, args, stdout, stderr) int`，`fishctl persona eval` 只需一行调用即可，**未修改 `cmd/`**；是否采用由 Codex 决定。
- 未改 `go.mod`，未引入任何第三方依赖：`meta.yaml` 走自写的扁平键值子集解析器（仅标准库）。
- 回归执行器通过 `Generator` 接口注入，不依赖具体 Brain 实现；Brain 落地后实现该接口即可跑真实回归。

### 测试命令与结果

在**系统临时目录**（非仓库）建同名模块验证，未在仓库内创建 `go.mod`：

| 命令 | 结果 |
|---|---|
| `gofmt -l internal` | 无输出（已格式化） |
| `go vet ./...` | 无输出（通过） |
| `go test ./internal/persona/ -count=1 -v` | PASS，15 个测试（含 7 个子测试），`ok RaiseMyFish/internal/persona 1.125s` |
| 回归（baseline 回放） | 用例 35 条，通过 35 条，通过率 100.0%（门槛 90%） |

### 未执行项及原因

| 未执行 | 原因 |
|---|---|
| 仓库内 `go test ./...` | 仓库尚无 `go.mod`（0.1 属 Codex），无法在仓库内编译 |
| 真实模型人格通过率 | 0.6 Brain 未就绪；`mock 不能当作真实人格验收`（§5） |
| 真实 PG 集成测试 | 与 0.7 无关，且属 0.2 范围 |
| `golangci-lint run` | 未确认本机可用（§5 亦指出 `go vet` 不能替代 lint） |
| 0.7b 加载器 / 0.7c 完整回归 | 看板阻塞：0.1 未验收、Pack 接口未基线化、0.6 未就绪 |

### 风险 / 阻塞 / 需裁决事项

- **V-01（最高优先）**：`internal/persona/` 全部 7 个 Go 文件超出 0.7a 可写范围（§2「不写 Go」）。请 Codex 裁决：**接受为 0.7b 预研草稿**，或**要求删除**。白酱不自行保留或删除，等你裁决。若保留，接口随时可按 Pack 基线与 `docs/testing.md` 门禁重写。
- **V-02**：素材路径与文档不一致。`docs/08-persona.md §2` 写「`~/.bili-ai/persona-qq.md`」，实际文件在 `~/.dsh/persona-qq.md`。白酱按实物迁移，请本鱼修正文档或确认。
- **V-03**：`persona-shared.md` 的「当前状态」段（累计约 90 块存粮、每 15 分钟一朵小花花、`gift-ledger.json` 为准）属**运行时状态**，按 `docs/08 §6` 未迁移进人格包。需确认落到记忆/配置（0.9）而非人格。
- **V-04**：基线回放 100% **不等于**真实模型 ≥90%。当前证据只证明「规则 ↔ 基准输出」自洽、执行器可跑；真实通过率必须等 0.6 后用真实模型重跑，`mock 不能当作真实人格验收`。
- **V-05**：若 Codex 在 0.7b 选定 `gopkg.in/yaml.v3` 做 Pack 解析，白酱自写的 `meta.yaml` 子集解析器可直接废弃，`meta.yaml` 改为块状写法也无需改内容语义。
- 其他：`persona.md` 里的 bili-cli 命令、巡检水位线、梗库 `meme.mjs` 等**工具流程**已刻意不迁进人格包（人格只管语气与写法），后续需要时由采集/发送侧实现，不在 0.7a 范围。

### 是否已暂停写入 / 交给 DSH 的验收范围

- **已暂停写入：是。** 自本回执起停止对仓库的所有写入，等看板派发 0.7a 的具体子任务后再继续；不自行 commit / push。
- 建议 DSH 验收范围：`personas/dafeiyu/` 全部（纯数据，可独立核对与 `docs/08-persona.md` 的结构一致性、用例覆盖率与 forbid 完整性）。
- `internal/persona/` 先由 Codex 裁决去留（V-01），DSH 验收暂不纳入，避免为可能被删除的文件耗验收成本。

---

## COMM-005 回执 · 0.7b 接手（2026-10-31，Asia/Shanghai）

**已读状态：WorkBuddy 本人已读并确认接手。**

- 已读文件：`contracts/persona.md`（P0-PACK-1）、`plans/phase-0.7b-dispatch.md`、`plans/coordination.md`、`AGENTS.md`、`plans/reports/codex.md` 的 COMM-002 / COMM-003 / COMM-004 / COMM-005。
- 基线提交号：**42a823f**（主人转达）。
  ⚠️ 本轮指令要求「不执行任何 git 命令」，因此该提交号**未用 git 复核**，以主人/DSH 转达为准；白酱改用文件内容核对：工作区同时存在 `contracts/persona.md`（P0-PACK-1）与 `plans/phase-0.7b-dispatch.md`，两者指针一致，`go.mod` 已含 `go.yaml.in/yaml/v3 v3.0.5`（indirect），与 COMM-005 描述相符。
- 接手时间：2026-10-31 16:45（Asia/Shanghai）。COMM-001 的「暂停写入」到此解除，仅在下列获派范围内恢复写入。
- 前序 V-01 已由 COMM-003 裁决：`internal/persona/` 7 份 Go 保留为**未接入预研草稿**，按重新派发修订。本轮即该重新派发。

### 获派文件（唯一可写范围）

| 路径 | 动作 |
|---|---|
| `internal/persona/meta.go` | 重作：`Meta` 类型 + 严格 YAML 结构化解码 + 元信息校验 |
| `internal/persona/pack.go` | 重作：`Pack` 类型、`Load(ctx,dir)`、`ContextKeys` / `ContextKey` / `ContextFor` / `SystemPrompt` |
| `internal/persona/pack_test.go` | 重作：按 P0-PACK-1 §5 验收项重写 |
| `internal/persona/meta_test.go` | 新增（可选范围内）：YAML 解码边界 |
| `internal/persona/context_test.go` | 新增（可选范围内）：context.md 切分边界 |
| `personas/dafeiyu/meta.yaml` | 仅删过时的「不自带解析器不引 YAML 库 / 只支持行内数组」注释；字段值一概不动 |
| `plans/reports/workbuddy.md` | 本回执 + 后续交接 |
| `.workbuddy/persona-eval-draft-comm-005/` | 旧草稿移位目标（新建目录） |

明确不碰：`go.mod` / `go.sum`、`cmd/`、`internal/infra/`、`internal/database/`、`migrations/`、`contracts/`、协作看板、其他 Agent 报告、人格正文（soul/rules/samples/context）与 `eval/` 数据。

### 旧草稿位置与状态（已按放行单第 4 条移动，原样留存）

目标目录：`D:\CodeStudy\RaiseMyFish\.workbuddy\persona-eval-draft-comm-005\`（移动前不存在）。

| 文件 | 移动前 SHA256 | 移动后 SHA256 | 字节 | 源已移除 |
|---|---|---|---|---|
| `check.go` | `B406047E3B7A72C91F35B05F7A891850D09E37C945EACF38FE09D35C09414F85` | 同左 | 9497 | 是 |
| `evalcase.go` | `31DAC5EBD35F710C732C6F3BB8FC629AF1F9B627CE734EF6F9118A01F2C1106A` | 同左 | 4953 | 是 |
| `runner.go` | `93505D58401119C7973C75A31E544BACDC43F5B2F618F6FE1ADF74936B344881` | 同左 | 12341 | 是 |
| `eval_test.go` | `8051F0122991BC10927DDB8395DED463470D5C156748E1CEF3C61E1EBD0FB3B7` | 同左 | 7400 | 是 |

- 4 份全部为**未跟踪草稿**；移动不改内容，前后哈希逐字节一致。
- 冲突检查：移动前目标目录与 4 个同名文件均不存在，未发生任何覆盖（放行单要求的「有冲突就停止」未触发）。
- 状态说明：`.workbuddy/` 已被忽略且目录名以 `.` 开头，`go build ./...`、`go test ./...`、`go vet ./...` 会跳过该目录，未放行的回归/LLM 草稿不会进入 0.7b 构建。
- 这些草稿属 **0.7c/0.6 范畴**，本轮不接收、不修订、不作为交付物；如需恢复由 Codex 另行派发。

---

## 0.7b 交接 · P0-PACK-1 加载器（2026-10-31，Asia/Shanghai）

- 任务/子任务：0.7b 加载器（`personas` 静态数据读取），基线 **P0-PACK-1**，提交号 **42a823f**（主人转达，未用 git 复核——本轮禁止 git 命令）。
- 获派与实际修改路径（等于获派范围，无越界）：

| 路径 | 动作 | SHA256 | 字节 |
|---|---|---|---|
| `internal/persona/meta.go` | 重作 | `4F89C5DC3E81AE1DCA52396F4E08508756117A590277D22394393A2DB74A6EC2` | 3633 |
| `internal/persona/pack.go` | 重作 | `4B4625F5583A307D3ECABC4976C5BE5CDA0FF23A4F872EA0CB3778D4F36EFDD8` | 12630 |
| `internal/persona/pack_test.go` | 重作 | `B24A6DBFEA8E42C03E91B08D3903F032353D3123DC666B43C029455B84B7393D` | 10449 |
| `internal/persona/meta_test.go` | 新增 | `DDCAE8691B5AF2445B12F29DE36338E2BF1116FB3A6A2E26AFCB3E38270FDCF1` | 5906 |
| `internal/persona/context_test.go` | 新增 | `1BD7D33C5993BF5E7DE7C352A28F66C969BC4B1EB489D79B67D4A62D277BC5CB` | 5250 |
| `personas/dafeiyu/meta.yaml` | 仅删过时解析器注释 | `9BA3F39935235877D9B1079B9ECEFA38671971A48F911F60EE602795EECD26F8` | 732 |

`internal/persona/` 现只剩上述 5 个文件；4 份旧草稿已移入 `.workbuddy/persona-eval-draft-comm-005/`，移动前后哈希见本报告上一节，逐一核对一致。

### 导出符号对照（契约 §1 ↔ 实现）

| 契约 | 实现 | 状态 |
|---|---|---|
| `ContextLive/Comment/DM/QQPrivate/QQGroup` | `pack.go` 同名常量 | 一致 |
| `type Meta`（6 字段 + yaml tag） | `meta.go`，字段名/类型/tag 完全一致 | 一致 |
| `type Pack`（Dir/Meta/Soul/Rules/Samples/Contexts） | `pack.go` 一致 | 一致 |
| `Load(ctx context.Context, dir string) (Pack, error)` | 一致 | 一致 |
| `ContextKeys() []string` | 每次返回新 slice，顺序固定 | 一致 |
| `ContextKey(channel string, private bool) (string, error)` | 一致 | 一致 |
| `(p Pack) ContextFor(...)` / `(p Pack) SystemPrompt()` | 值接收者 | 一致 |
| 不导出 `Assemble`/`Assembled`/`Generator`/`RunEvalCLI`/评分注册表 | 已随 4 份草稿移出包外 | 一致 |

### 实现要点（对齐契约 §2）

- YAML 改用 `go.yaml.in/yaml/v3` 结构化解码 + `KnownFields`；**未改 `go.mod`/`go.sum`、未执行 tidy**。拒绝未知字段、重复字段、类型错误、多文档、语法错误、空内容。
- 读取边界：仅五份静态文件；常规文件、合法 UTF-8、≤1 MiB；去一个 BOM、CRLF/孤立 CR 统一 LF；**已去掉草稿的 200/50 字最小篇幅门槛**（有专门用例证明 1~2 字可加载）。
- `Load` 在开始与每次读文件前检查 ctx；`errors.Is(context.Canceled / DeadlineExceeded)` 用例各有覆盖；所有错误返回零值 `Pack`。
- 错误脱敏：yaml 类型错误会把原始标量带进消息，已用反引号遮蔽，并有用例断言错误不含原文。
- `context.md` 切分：围栏代码块（``` / ~~~）内不切分；`## <ASCII键>` 才切分；中文说明标题与 `###` 子标题原样留在正文；重复键、未知 ASCII 键、空段、无段均报错；`channels` 与有效段集合必须相等（允许只含 live 的最小包）。
- `SystemPrompt` 严格按 `TrimSpace(Soul)+"\n\n"+TrimSpace(Rules)+"\n\n"+TrimSpace(Samples)`，纯函数、重复调用稳定、不掺场景与动态内容。

### 测试命令与结果

Windows 侧（仓库内，`RaiseMyFish` 模块）：

| 命令 | 结果 |
|---|---|
| `gofmt -l internal/persona` | 无输出 |
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./... -count=1` | 全部 ok（cmd/fishctl、cmd/fishd、config、database、infra、persona；migrations 无测试文件） |
| `go test ./internal/persona/ -cover` | ok，覆盖率 **93.8%** |
| `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./internal/persona/...` | **0 issues** |
| 用例数 | persona 包 20 个顶层测试 + 40 个子测试全 PASS |

竞态：

| 命令 | 结果 |
|---|---|
| Windows `go test -race ./internal/persona/` | **失败（环境限制）**：`cc1.exe: sorry, unimplemented: 64-bit mode not compiled in`——本机 PATH 只有 mingw32 gcc，与 Codex 报告一致，不当作通过 |
| WSL `go1.25.0 linux/amd64` + `CGO_ENABLED=1 go test -race ./internal/persona/` | **通过**（`ok RaiseMyFish/internal/persona 1.089s`） |

### 未验证项及原因

| 未做 | 原因 |
|---|---|
| 真实模型人格通过率 / 回归执行 | 属 0.7c，等 0.6 Brain 与真实模型；baseline 回放不能替代（COMM-003 V-04） |
| 真实 PG 集成 | 0.7b 是纯文件加载器，数据层未变；`go test ./...` 已确认未破坏既有包 |
| 全仓 `go test -race ./...` | 只对获派模块跑 race；全仓含 PG 集成组合属 0.1 范围（Codex 已报告 WSL 连 Windows PG 不通） |
| `go mod tidy` | 按 COMM-005 明令禁止自行 tidy |

### 风险 / 需 DSH 与 Codex 留意

1. **依赖标记过期**：`internal/persona` 现已直接 import `go.yaml.in/yaml/v3`，但 `go.mod` 里它仍标 `// indirect`。按 COMM-005「后续接收加载器时由 Codex 统一整理依赖」，白酱**未改**。请 Codex 在集成时统一 tidy。
2. **`## live 说明` 这类混合标题**：契约只规定「中文说明标题不成为新场景」「未知 ASCII 键报错」。当前实现把「ASCII 词 + 空格/中文」的标题当作普通正文（不报错也不切分）。好处是兼容 `## live 补充说明`，风险是拼错成 `## livee` 会被报错、但写成 `## live 补` 会静默并入上一段。若 DSH 认为应更严格，请裁决后改。
3. **Contexts 正文做了 TrimSpace**：契约要求「保留其他文本内容」，当前只去掉场景正文首尾空白（用于稳定 prompt 缓存）。若要求逐字保留首尾空白，需调整。
4. **流程偏差（如实交代）**：放行单要求「先运行针对性失败测试，再实现修订」。本轮是先写实现 + 边界测试再跑，测试随后抓到 1 个真实缺陷——**相邻同名场景（`## live` … `## live`）未被判重复**，会把前一段静默覆盖。已修（重复判定同时看「已落盘」与「正在写」）并复跑全绿。未走严格 TDD 顺序，记录在此以免被当成已验证先红后绿。

### 是否暂停写入

- **是。** 本轮写入已结束，`go.mod`/`go.sum`/`cmd`/`infra`/`database`/迁移/`contracts`/协作看板/其他 Agent 报告/人格正文/eval 数据均未触碰；未执行任何 git 命令、未提交、未 push。
- 交 DSH 验收范围：上述 6 个文件（5 个 Go + 1 个 yaml 注释改动），按 `contracts/persona.md` §5 逐条独立复核；`.workbuddy/persona-eval-draft-comm-005/` 里的 4 份草稿**不属于**本次交付，请勿计入 0.7b 验收。
